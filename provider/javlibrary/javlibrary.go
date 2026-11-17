package javlibrary

import (
	"fmt"
	"net/url"
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
	_ provider.MovieProvider = (*JavLibrary)(nil)
	_ provider.MovieSearcher = (*JavLibrary)(nil)
	_ provider.Fetcher       = (*JavLibrary)(nil)
)

const (
	Name     = "JavLibrary"
	Priority = 1000 - 4
)

const baseURL = "https://www.javlibrary.com"

// NOTE: javlibrary.com sits behind a Cloudflare challenge ("Just a
// moment...") from the egresses available to this project, so live lookups
// currently fail closed. The implementation below uses standard fetching
// only (no bypass tooling) and works as soon as the site is reachable
// without a challenge. Parsing is covered by fixture tests.

var scoreRe = regexp.MustCompile(`\(([\d.]+)\)`)

type JavLibrary struct {
	*fetch.Fetcher
	*scraper.Scraper

	// endpoint is the site base URL; tests override it with a fixture
	// server. It defaults to baseURL.
	endpoint string
}

func New() *JavLibrary {
	return &JavLibrary{
		Fetcher: fetch.Default(&fetch.Config{Referer: baseURL}),
		Scraper: scraper.NewDefaultScraper(
			Name, baseURL, Priority,
			language.Chinese,
			scraper.WithHeaders(map[string]string{
				"Referer": baseURL,
			})),
		endpoint: baseURL,
	}
}

func (lib *JavLibrary) movieURL(id string) string {
	return fmt.Sprintf("%s/cn/?v=%s", lib.endpoint, id)
}

func (lib *JavLibrary) searchURL(keyword string) string {
	return fmt.Sprintf("%s/cn/vl_searchbyid.php?keyword=%s", lib.endpoint, url.QueryEscape(keyword))
}

// NormalizeMovieID keeps the ID as-is: detail IDs are opaque "v" params.
func (lib *JavLibrary) NormalizeMovieID(id string) string {
	return strings.TrimSpace(id)
}

func (lib *JavLibrary) ParseMovieIDFromURL(rawURL string) (string, error) {
	homepage, err := url.Parse(rawURL)
	if err != nil {
		return "", err
	}
	return homepage.Query().Get("v"), nil
}

func (lib *JavLibrary) GetMovieInfoByID(id string) (*model.MovieInfo, error) {
	info, err := lib.GetMovieInfoByURL(lib.movieURL(id))
	if err == nil {
		return info, nil
	}
	// A plain number is not a detail ID: resolve it via search, unless the
	// ID already looks like a detail ID.
	if strings.HasPrefix(strings.ToLower(id), "javlib") {
		return nil, provider.ErrInfoNotFound
	}
	results, searchErr := lib.SearchMovie(lib.NormalizeMovieKeyword(id))
	if searchErr != nil || len(results) == 0 {
		return nil, provider.ErrInfoNotFound
	}
	return lib.GetMovieInfoByURL(results[0].Homepage)
}

func (lib *JavLibrary) GetMovieInfoByURL(rawURL string) (info *model.MovieInfo, err error) {
	id, err := lib.ParseMovieIDFromURL(rawURL)
	if err != nil {
		return nil, err
	}
	if id == "" {
		return nil, provider.ErrInvalidURL
	}

	info = &model.MovieInfo{
		ID:            id,
		Provider:      lib.Name(),
		Homepage:      lib.movieURL(id),
		Actors:        []string{},
		PreviewImages: []string{},
		Genres:        []string{},
	}

	c := lib.ClonedCollector()

	c.OnXML(`//div[@id="video_id"]//td[@class="text"]`, func(e *colly.XMLElement) {
		if info.Number == "" {
			info.Number = strings.TrimSpace(e.Text)
		}
	})
	c.OnXML(`//h3[contains(@class,"post-title")]/a`, func(e *colly.XMLElement) {
		if info.Title == "" {
			info.Title = strings.TrimSpace(e.Text)
		}
	})
	c.OnXML(`//div[@id="video_date"]//td[@class="text"]`, func(e *colly.XMLElement) {
		info.ReleaseDate = parser.ParseDate(e.Text)
	})
	c.OnXML(`//div[@id="video_length"]//td/span[@class="text"]`, func(e *colly.XMLElement) {
		info.Runtime = parser.ParseRuntime(e.Text)
	})
	c.OnXML(`//div[@id="video_director"]//td[@class="text"]//a`, func(e *colly.XMLElement) {
		if info.Director == "" {
			info.Director = strings.TrimSpace(e.Text)
		}
	})
	c.OnXML(`//div[@id="video_maker"]//td[@class="text"]//a`, func(e *colly.XMLElement) {
		if info.Maker == "" {
			info.Maker = strings.TrimSpace(e.Text)
		}
	})
	c.OnXML(`//div[@id="video_label"]//td[@class="text"]//a`, func(e *colly.XMLElement) {
		if info.Label == "" {
			info.Label = strings.TrimSpace(e.Text)
		}
	})
	c.OnXML(`//span[@class="cast"]/span[@class="star"]/a`, func(e *colly.XMLElement) {
		if name := strings.TrimSpace(e.Text); name != "" {
			info.Actors = append(info.Actors, name)
		}
	})
	c.OnXML(`//div[@id="video_genres"]//td[@class="text"]/span[@class="genre"]/a`, func(e *colly.XMLElement) {
		if tag := strings.TrimSpace(e.Text); tag != "" {
			info.Genres = append(info.Genres, tag)
		}
	})
	c.OnXML(`//img[@id="video_jacket_img"]`, func(e *colly.XMLElement) {
		if info.CoverURL == "" {
			cover := e.Request.AbsoluteURL(e.Attr("src"))
			info.CoverURL = cover
			info.ThumbURL = cover
		}
	})
	c.OnXML(`//span[@class="score"]`, func(e *colly.XMLElement) {
		if m := scoreRe.FindStringSubmatch(e.Text); len(m) == 2 {
			info.Score = parser.ParseScore(m[1])
		}
	})

	if visitErr := c.Visit(lib.movieURL(id)); visitErr != nil {
		return nil, visitErr
	}
	if info.Number == "" {
		return nil, provider.ErrInfoNotFound
	}
	return info, nil
}

func (lib *JavLibrary) NormalizeMovieKeyword(keyword string) string {
	return strings.ToUpper(strings.TrimSpace(keyword))
}

func (lib *JavLibrary) SearchMovie(keyword string) (results []*model.MovieSearchResult, err error) {
	c := lib.ClonedCollector()

	sawResults := false
	var fallback *model.MovieSearchResult

	c.OnXML(`//div[@class="video"]`, func(e *colly.XMLElement) {
		sawResults = true
		href := e.ChildAttr(".//a[@href]", "href")
		if href == "" {
			return
		}
		// Links are relative to the /cn/ directory of the search URL.
		homepage := e.Request.AbsoluteURL(href)
		id, _ := lib.ParseMovieIDFromURL(homepage)
		if id == "" {
			return
		}
		title := strings.TrimSpace(e.ChildAttr(".//a[@href]", "title"))
		result := &model.MovieSearchResult{
			ID:       id,
			Number:   leadingToken(title),
			Title:    strings.TrimSpace(strings.TrimPrefix(title, leadingToken(title))),
			Provider: lib.Name(),
			Homepage: homepage,
			ThumbURL: e.Request.AbsoluteURL(e.ChildAttr(".//img", "src")),
		}
		result.CoverURL = result.ThumbURL
		if fallback == nil {
			fallback = result
		}
		if sameNumber(result.Number, keyword) || strings.Contains(strings.ToUpper(title), strings.ToUpper(keyword)) {
			results = append(results, result)
		}
	})

	// A unique hit redirects straight to the detail page: no result list,
	// the detail page itself is the answer (verified by number below).
	var detailNumber, canonical string
	c.OnXML(`//div[@id="video_id"]//td[@class="text"]`, func(e *colly.XMLElement) {
		detailNumber = strings.TrimSpace(e.Text)
	})
	c.OnXML(`//link[@rel="canonical"]`, func(e *colly.XMLElement) {
		canonical = e.Attr("href")
	})

	if visitErr := c.Visit(lib.searchURL(keyword)); visitErr != nil {
		return nil, visitErr
	}
	if sawResults {
		if len(results) == 0 && fallback != nil {
			results = append(results, fallback)
		}
		return results, nil
	}
	if canonical != "" && sameNumber(detailNumber, keyword) {
		if info, infoErr := lib.GetMovieInfoByURL(canonical); infoErr == nil && info.IsValid() {
			results = append(results, info.ToSearchResult())
		}
	}
	return results, nil
}

// leadingToken returns the first whitespace-separated token of s.
func leadingToken(s string) string {
	if fields := strings.Fields(s); len(fields) > 0 {
		return fields[0]
	}
	return ""
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

func init() {
	provider.Register(Name, New)
}
