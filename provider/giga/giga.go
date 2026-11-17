package giga

import (
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/gocolly/colly/v2"
	"golang.org/x/text/language"

	"github.com/metatube-community/metatube-sdk-go/common/fetch"
	"github.com/metatube-community/metatube-sdk-go/common/parser"
	"github.com/metatube-community/metatube-sdk-go/common/random"
	"github.com/metatube-community/metatube-sdk-go/model"
	"github.com/metatube-community/metatube-sdk-go/provider"
	"github.com/metatube-community/metatube-sdk-go/provider/internal/scraper"
)

var (
	_ provider.MovieProvider        = (*Giga)(nil)
	_ provider.MovieSearcher        = (*Giga)(nil)
	_ provider.Fetcher              = (*Giga)(nil)
	_ provider.RequestTimeoutSetter = (*Giga)(nil)
)

const (
	Name     = "GIGA"
	Priority = 1000
)

const (
	baseURL    = "https://www.giga-web.jp"
	refererURL = baseURL + "/"
	ageGateURL = baseURL + "/cookie_set.php"
	movieURL   = baseURL + "/product/index.php?product_id=%s"
	searchURL  = baseURL + "/search/?keyword=%s"
)

// ageGateXPath matches the "はい" link on the age-verification page. The
// site answers content URLs with the age page (still HTTP 200) when the
// session is not confirmed, so the page must be detected by content.
const ageGateXPath = `//a[contains(@href,"/cookie_set.php")]`

var numberInParensRe = regexp.MustCompile(`（([^）]+)）`)

type Giga struct {
	*fetch.Fetcher
	*scraper.Scraper

	httpClient *http.Client
	userAgent  string
	timeout    time.Duration
}

func New() *Giga {
	ua := random.UserAgent()
	return &Giga{
		Fetcher: fetch.Default(&fetch.Config{Referer: refererURL}),
		Scraper: scraper.NewDefaultScraper(
			Name, baseURL, Priority,
			language.Japanese,
			scraper.WithHeaders(map[string]string{
				"Referer": refererURL,
			})),
		httpClient: &http.Client{
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
			Timeout:       30 * time.Second,
		},
		userAgent: ua,
		timeout:   30 * time.Second,
	}
}

func (g *Giga) SetRequestTimeout(timeout time.Duration) {
	g.timeout = timeout
	g.httpClient.Timeout = timeout
	g.Scraper.SetRequestTimeout(timeout)
}

// claimSession visits the age-confirmation entry point and returns all
// cookies it sets: PHPSESSID plus the age-confirmation marker (old_check),
// layout and load-balancer cookies. A fresh client (no jar) always gets a
// new session on the first attempt; an old session would only be deleted,
// so no cookies are reused.
func (g *Giga) claimSession() ([]*http.Cookie, error) {
	req, err := http.NewRequest(http.MethodGet, ageGateURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Referer", refererURL)
	req.Header.Set("User-Agent", g.userAgent)
	resp, err := g.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var cookies []*http.Cookie
	hasSession := false
	for _, cookie := range resp.Cookies() {
		if cookie.Value == "" || cookie.Value == "deleted" {
			continue
		}
		if cookie.Name == "PHPSESSID" {
			hasSession = true
		}
		cookies = append(cookies, cookie)
	}
	if !hasSession {
		return nil, provider.ErrInfoNotFound
	}
	return cookies, nil
}

// newSessionCollector returns a collector carrying a freshly claimed age
// gate session.
func (g *Giga) newSessionCollector() (*colly.Collector, error) {
	cookies, err := g.claimSession()
	if err != nil {
		return nil, err
	}
	c := g.ClonedCollector()
	if err := c.SetCookies(baseURL, cookies); err != nil {
		return nil, err
	}
	return c, nil
}

// visit fetches pageURL with a fresh age-gate session, retrying once with a
// new session if the age page is served instead of content. Handlers
// registered by setup run on each attempt; setup must be idempotent with
// respect to the result it populates.
func (g *Giga) visit(pageURL string, setup func(*colly.Collector)) error {
	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		c, err := g.newSessionCollector()
		if err != nil {
			return err
		}
		ageGate := false
		c.OnXML(ageGateXPath, func(*colly.XMLElement) { ageGate = true })
		setup(c)
		if lastErr = c.Visit(pageURL); lastErr != nil {
			return lastErr
		}
		if !ageGate {
			return nil
		}
	}
	return provider.ErrInfoNotFound
}

func (g *Giga) NormalizeMovieID(id string) string {
	return strings.TrimSpace(id)
}

func (g *Giga) ParseMovieIDFromURL(rawURL string) (string, error) {
	homepage, err := url.Parse(rawURL)
	if err != nil {
		return "", err
	}
	return homepage.Query().Get("product_id"), nil
}

func (g *Giga) GetMovieInfoByID(id string) (*model.MovieInfo, error) {
	return g.GetMovieInfoByURL(fmt.Sprintf(movieURL, id))
}

func (g *Giga) GetMovieInfoByURL(rawURL string) (info *model.MovieInfo, err error) {
	id, err := g.ParseMovieIDFromURL(rawURL)
	if err != nil {
		return nil, err
	}
	if id == "" {
		return nil, provider.ErrInvalidURL
	}

	info = &model.MovieInfo{
		ID:            id,
		Provider:      g.Name(),
		Homepage:      fmt.Sprintf(movieURL, id),
		Actors:        []string{},
		PreviewImages: []string{},
		Genres:        []string{},
		Maker:         "GIGA",
	}

	setup := func(c *colly.Collector) {
		// Title.
		c.OnXML(`//div[@id="works_pic"]//h5`, func(e *colly.XMLElement) {
			if info.Title == "" {
				info.Title = strings.TrimSpace(e.Text)
			}
		})
		// Cover.
		c.OnXML(`//div[@id="works_pic"]//img`, func(e *colly.XMLElement) {
			if info.CoverURL == "" {
				info.CoverURL = e.Request.AbsoluteURL(e.Attr("src"))
			}
		})
		// Work info fields: <dl><dt>label</dt><dd>value</dd></dl>.
		workField := func(label string) string {
			return fmt.Sprintf(`//div[@id="works_txt"]//dl[dt[text()="%s"]]/dd`, label)
		}
		c.OnXML(workField("作品番号"), func(e *colly.XMLElement) {
			if info.Number == "" {
				info.Number = strings.TrimSpace(e.Text)
			}
		})
		c.OnXML(workField("DVDリリース日"), func(e *colly.XMLElement) {
			info.ReleaseDate = parser.ParseDate(e.Text)
		})
		c.OnXML(workField("収録時間"), func(e *colly.XMLElement) {
			info.Runtime = parser.ParseRuntime(e.Text)
		})
		c.OnXML(workField("監督"), func(e *colly.XMLElement) {
			names := e.ChildTexts(".//a")
			if len(names) == 0 {
				names = []string{strings.TrimSpace(e.Text)}
			}
			if len(names) > 0 {
				info.Director = strings.TrimSpace(names[0])
			}
		})
		// Actors.
		c.OnXML(`//div[@id="works_txt"]//a[contains(@href,"actor_id")]`, func(e *colly.XMLElement) {
			if name := strings.TrimSpace(e.Text); name != "" {
				info.Actors = append(info.Actors, name)
			}
		})
		// Sample images.
		c.OnXML(`//div[contains(@class,"gasatsu_images_pc")]//img`, func(e *colly.XMLElement) {
			info.PreviewImages = append(info.PreviewImages, e.Request.AbsoluteURL(e.Attr("src")))
		})
		// Genre tags: the first div#tag block holds the full set; later
		// blocks are subsets, so duplicates are dropped.
		c.OnXML(`//div[@id="tag"]//a[contains(@href,"tag_id=")]`, func(e *colly.XMLElement) {
			if tag := strings.TrimSpace(e.Text); tag != "" {
				info.Genres = append(info.Genres, tag)
			}
		})
		// Plot: story_list2 holds the full text; the toggle link inside
		// each item is not part of the text content.
		c.OnXML(`//div[@id="story_list2"]//li[contains(@class,"story_window")]`, func(e *colly.XMLElement) {
			if text := strings.TrimSpace(e.Text); text != "" {
				if info.Summary != "" {
					info.Summary += "\n"
				}
				info.Summary += text
			}
		})
	}

	if err := g.visit(fmt.Sprintf(movieURL, id), setup); err != nil {
		return nil, err
	}
	if info.Number == "" || info.Title == "" {
		return nil, provider.ErrInfoNotFound
	}
	info.Genres = dedupeStrings(info.Genres)
	info.Actors = dedupeStrings(info.Actors)
	return info, nil
}

func (g *Giga) NormalizeMovieKeyword(keyword string) string {
	return strings.ToUpper(strings.TrimSpace(keyword))
}

func (g *Giga) SearchMovie(keyword string) (results []*model.MovieSearchResult, err error) {
	setup := func(c *colly.Collector) {
		c.OnXML(`//div[contains(@class,"thumBox")]`, func(e *colly.XMLElement) {
			href := e.ChildAttr(`.//a[contains(@href,"/product/index.php")]`, "href")
			if href == "" {
				return
			}
			homepage := e.Request.AbsoluteURL(href)
			id, _ := g.ParseMovieIDFromURL(homepage)
			if id == "" {
				return
			}
			// The number follows the title link in full-width parens.
			m := numberInParensRe.FindStringSubmatch(e.Text)
			if len(m) != 2 || !sameNumber(m[1], keyword) {
				return
			}
			title := strings.TrimSpace(e.ChildText(`.//span`))
			thumb := e.Request.AbsoluteURL(e.ChildAttr(`.//img`, "src"))
			results = append(results, &model.MovieSearchResult{
				ID:       id,
				Number:   strings.ToUpper(strings.TrimSpace(m[1])),
				Title:    title,
				Provider: g.Name(),
				Homepage: homepage,
				ThumbURL: thumb,
				CoverURL: thumb,
			})
		})
	}

	if err := g.visit(fmt.Sprintf(searchURL, url.QueryEscape(keyword)), setup); err != nil {
		return nil, err
	}
	return results, nil
}

// sameNumber compares two movie numbers ignoring case and dashes.
func sameNumber(a, b string) bool {
	normalize := func(s string) string {
		s = strings.ToUpper(s)
		s = strings.ReplaceAll(s, "-", "")
		s = strings.ReplaceAll(s, "_", "")
		return strings.TrimSpace(s)
	}
	return normalize(a) != "" && normalize(a) == normalize(b)
}

func dedupeStrings(values []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, v := range values {
		if seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	if out == nil {
		out = []string{}
	}
	return out
}

func init() {
	provider.Register(Name, New)
}
