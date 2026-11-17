package javdb

import (
	"fmt"
	"net/url"
	"path"
	"regexp"
	"strings"

	"github.com/gocolly/colly/v2"
	"golang.org/x/text/language"

	"github.com/metatube-community/metatube-sdk-go/common/fetch"
	"github.com/metatube-community/metatube-sdk-go/common/parser"
	"github.com/metatube-community/metatube-sdk-go/model"
	"github.com/metatube-community/metatube-sdk-go/provider"
	"github.com/metatube-community/metatube-sdk-go/provider/internal/scraper"
)

var (
	_ provider.MovieProvider = (*JavDB)(nil)
	_ provider.MovieSearcher = (*JavDB)(nil)
	_ provider.Fetcher       = (*JavDB)(nil)
)

const (
	Name     = "JavDB"
	Priority = 1000 - 4
)

const (
	baseURL   = "https://javdb.com"
	movieURL  = "https://javdb.com/v/%s?locale=zh"
	searchURL = "https://javdb.com/search?q=%s&locale=zh"
)

// loginFormXPath matches the full-page login panel. Every page carries the
// same form inside the navigation modal, so the modal ancestor must be
// excluded: only a standalone (non-modal) auth form means the page requires
// login. JavDB is accessed anonymously ONLY: when this matches, fail closed
// and never attempt any authentication.
const loginFormXPath = `//form[contains(@class,"auth-view")][.//input[@type="password"]][not(ancestor::div[contains(@class,"modal")])]`

// slugRe matches JavDB's opaque detail slugs (e.g. "YYdEb", "8p7NO"): short
// mixed-case alphanumerics without dashes. Numbers always contain a dash or
// match a letter+digits shape, so they never collide with this.
var slugRe = regexp.MustCompile(`^[A-Za-z0-9]{4,8}$`)

type JavDB struct {
	*fetch.Fetcher
	*scraper.Scraper
}

func New() *JavDB {
	return &JavDB{
		Fetcher: fetch.Default(&fetch.Config{Referer: baseURL}),
		Scraper: scraper.NewDefaultScraper(
			Name, baseURL, Priority,
			language.Chinese,
			scraper.WithHeaders(map[string]string{
				"Referer": baseURL,
			})),
	}
}

// NormalizeMovieID keeps the ID as-is: detail slugs are case-sensitive
// (e.g. "YYdEb") and must not be uppercased.
func (db *JavDB) NormalizeMovieID(id string) string {
	return strings.TrimSpace(id)
}

func (db *JavDB) ParseMovieIDFromURL(rawURL string) (string, error) {
	homepage, err := url.Parse(rawURL)
	if err != nil {
		return "", err
	}
	return db.NormalizeMovieID(path.Base(homepage.Path)), nil
}

func (db *JavDB) GetMovieInfoByID(id string) (info *model.MovieInfo, err error) {
	// Detail pages are addressed by opaque slugs, not numbers, so a plain
	// number cannot be fetched directly. Try the direct URL first (covers
	// slugs and any ID the site accepts), then fall back to a search that
	// resolves the number to its detail page.
	info, loginWalled, err := db.fetchMovieInfo(fmt.Sprintf(movieURL, id))
	if err == nil && info.IsValid() {
		return info, nil
	}
	if loginWalled {
		// The entry exists but is login-walled (e.g. uncensored). A search
		// would only lead back to the same walled page.
		return nil, provider.ErrInfoNotFound
	}
	results, searchErr := db.SearchMovie(db.NormalizeMovieKeyword(id))
	if searchErr != nil || len(results) == 0 {
		// Either the number does not exist or anonymous search is being
		// throttled (HTTP 403); both fail closed the same way.
		return nil, provider.ErrInfoNotFound
	}
	info, _, err = db.fetchMovieInfo(results[0].Homepage)
	if err != nil {
		return nil, provider.ErrInfoNotFound
	}
	if !info.IsValid() {
		return nil, provider.ErrInfoNotFound
	}
	return info, nil
}

func (db *JavDB) GetMovieInfoByURL(rawURL string) (info *model.MovieInfo, err error) {
	id, err := db.ParseMovieIDFromURL(rawURL)
	if err != nil {
		return nil, err
	}
	info, _, err = db.fetchMovieInfo(fmt.Sprintf(movieURL, id))
	if err != nil {
		return nil, err
	}
	if !info.IsValid() {
		return nil, provider.ErrInfoNotFound
	}
	return info, nil
}

// fetchMovieInfo parses a detail page. It reports whether the page was
// login-walled so callers can skip pointless retries.
func (db *JavDB) fetchMovieInfo(pageURL string) (info *model.MovieInfo, loginWalled bool, err error) {
	info = &model.MovieInfo{
		Provider:      db.Name(),
		Homepage:      pageURL,
		Actors:        []string{},
		PreviewImages: []string{},
		Genres:        []string{},
	}

	c := db.ClonedCollector()

	c.OnXML(loginFormXPath, func(*colly.XMLElement) {
		loginWalled = true
	})

	// Number: the copy button carries the canonical number.
	c.OnXML(`//a[contains(@class,"copy-to-clipboard")]`, func(e *colly.XMLElement) {
		if info.Number == "" {
			info.Number = e.Attr("data-clipboard-text")
		}
		if id, parseErr := db.ParseMovieIDFromURL(e.Request.URL.String()); parseErr == nil && id != "" {
			info.ID = id
		}
	})

	// Title + cover.
	c.OnXML(`//h2[@class="title is-4"]/strong[@class="current-title"]`, func(e *colly.XMLElement) {
		info.Title = strings.TrimSpace(e.Text)
	})
	c.OnXML(`//img[@class="video-cover"]`, func(e *colly.XMLElement) {
		info.CoverURL = e.Request.AbsoluteURL(e.Attr("src"))
	})

	// Labeled fields: <strong>label</strong><span>value</span> pairs.
	c.OnXML(`//strong[contains(text(),"片商:")]/following-sibling::span[1]`, func(e *colly.XMLElement) {
		info.Maker = firstNonEmpty(e.ChildText(".//a"), strings.TrimSpace(e.Text))
	})
	c.OnXML(`//strong[contains(text(),"發行:")]/following-sibling::span[1]`, func(e *colly.XMLElement) {
		info.Label = firstNonEmpty(e.ChildText(".//a"), strings.TrimSpace(e.Text))
	})
	c.OnXML(`//strong[contains(text(),"時長")]/following-sibling::span[1]`, func(e *colly.XMLElement) {
		info.Runtime = parser.ParseRuntime(e.Text)
	})
	c.OnXML(`//strong[contains(text(),"日期:")]/following-sibling::span[1]`, func(e *colly.XMLElement) {
		info.ReleaseDate = parser.ParseDate(e.Text)
	})
	c.OnXML(`//strong[contains(text(),"系列:")]/following-sibling::span[1]`, func(e *colly.XMLElement) {
		info.Series = firstNonEmpty(e.ChildText(".//a"), strings.TrimSpace(e.Text))
	})
	c.OnXML(`//strong[contains(text(),"導演:")]/following-sibling::span[1]`, func(e *colly.XMLElement) {
		info.Director = e.ChildText(".//a")
	})
	c.OnXML(`//strong[contains(text(),"類別:")]/following-sibling::span[1]`, func(e *colly.XMLElement) {
		for _, tag := range e.ChildTexts(".//a") {
			if tag = strings.TrimSpace(tag); tag != "" {
				info.Genres = append(info.Genres, tag)
			}
		}
	})
	c.OnXML(`//strong[contains(text(),"演員")]/following-sibling::span[1]`, func(e *colly.XMLElement) {
		for _, name := range e.ChildTexts("./a") {
			if name = strings.TrimSpace(name); name != "" {
				info.Actors = append(info.Actors, name)
			}
		}
	})

	// Score: the text right after the star icons, e.g. "4.08分, 由355人評價".
	c.OnXML(`//span[span[@class="score-stars"]]`, func(e *colly.XMLElement) {
		info.Score = parser.ParseScore(e.Text)
	})

	// Preview images + trailer.
	c.OnXML(`//div[contains(@class,"preview-images")]//a[@class="tile-item"]`, func(e *colly.XMLElement) {
		info.PreviewImages = append(info.PreviewImages, e.Request.AbsoluteURL(e.Attr("href")))
	})
	c.OnXML(`//video[@id="preview-video"]/source`, func(e *colly.XMLElement) {
		info.PreviewVideoURL = e.Request.AbsoluteURL(e.Attr("src"))
	})

	c.OnScraped(func(r *colly.Response) {
		// Homepage without the locale query: the canonical detail URL.
		if id := info.ID; id != "" {
			info.Homepage = fmt.Sprintf("https://javdb.com/v/%s", id)
		}
	})

	if visitErr := c.Visit(pageURL); visitErr != nil {
		return nil, loginWalled, visitErr
	}
	if loginWalled || info.Number == "" {
		return nil, loginWalled, provider.ErrInfoNotFound
	}
	return info, false, nil
}

func (db *JavDB) NormalizeMovieKeyword(keyword string) string {
	return strings.ToUpper(strings.TrimSpace(keyword))
}

func (db *JavDB) SearchMovie(keyword string) (results []*model.MovieSearchResult, err error) {
	c := db.ClonedCollector()

	loginWalled := false
	c.OnXML(loginFormXPath, func(*colly.XMLElement) {
		loginWalled = true
	})

	c.OnXML(`//a[@class="box"]`, func(e *colly.XMLElement) {
		homepage := e.Request.AbsoluteURL(e.Attr("href"))
		id, _ := db.ParseMovieIDFromURL(homepage)
		if id == "" || !slugRe.MatchString(id) {
			return
		}
		number := strings.TrimSpace(e.ChildText(`div[@class="video-title"]/strong`))
		if number == "" || !sameNumber(number, keyword) {
			return // JavDB search is fuzzy; keep exact matches only.
		}
		fullTitle := strings.TrimSpace(e.ChildText(`div[@class="video-title"]`))
		title := strings.TrimSpace(strings.TrimPrefix(fullTitle, number))
		cover := e.Request.AbsoluteURL(e.ChildAttr(".//img", "src"))
		results = append(results, &model.MovieSearchResult{
			ID:       id,
			Number:   number,
			Title:    title,
			Provider: db.Name(),
			Homepage: homepage,
			ThumbURL: cover,
			CoverURL: cover,
		})
	})

	if visitErr := c.Visit(fmt.Sprintf(searchURL, url.QueryEscape(keyword))); visitErr != nil {
		return nil, visitErr
	}
	if loginWalled {
		return nil, provider.ErrInfoNotFound
	}
	return results, nil
}

// sameNumber compares two movie numbers ignoring case, dashes/underscores/
// spaces, and leading zeros (JavDB omits leading zero segments in some
// numbers, e.g. western date codes).
func sameNumber(a, b string) bool {
	normalize := func(s string) string {
		s = strings.ToUpper(s)
		s = strings.ReplaceAll(s, "-", "")
		s = strings.ReplaceAll(s, "_", "")
		s = strings.ReplaceAll(s, " ", "")
		return strings.TrimLeft(s, "0")
	}
	return normalize(a) != "" && normalize(a) == normalize(b)
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

func init() {
	provider.Register(Name, New)
}
