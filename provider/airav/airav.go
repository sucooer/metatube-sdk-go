package airav

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
	_ provider.MovieProvider = (*AiRAV)(nil)
	_ provider.MovieSearcher = (*AiRAV)(nil)
	_ provider.Fetcher       = (*AiRAV)(nil)
)

const (
	Name     = "AiRAV"
	Priority = 1000 - 6
)

const (
	baseURL   = "https://airav.io"
	movieURL  = "https://airav.io/video?hid=%s"
	searchURL = "https://airav.io/search_result?kw=%s"
)

// sourceLabels are site-source prefixes the site prepends to some numbers
// (e.g. "caribbeancom_051318-665"); they are stripped before comparing.
var sourceLabels = []string{"caribbeancom", "1pondo", "10musume", "pacopacomama"}

var fc2IDRe = regexp.MustCompile(`(?i)^FC2(?:-?PPV)?[-_]?\d+$`)

type AiRAV struct {
	*fetch.Fetcher
	*scraper.Scraper
}

func New() *AiRAV {
	return &AiRAV{
		Fetcher: fetch.Default(&fetch.Config{Referer: baseURL}),
		Scraper: scraper.NewDefaultScraper(
			Name, baseURL, Priority,
			language.Chinese,
			scraper.WithHeaders(map[string]string{
				"Referer": baseURL,
			})),
	}
}

func (av *AiRAV) NormalizeMovieID(id string) string {
	return strings.TrimSpace(id)
}

func (av *AiRAV) ParseMovieIDFromURL(rawURL string) (string, error) {
	homepage, err := url.Parse(rawURL)
	if err != nil {
		return "", err
	}
	return homepage.Query().Get("hid"), nil
}

func (av *AiRAV) GetMovieInfoByID(id string) (*model.MovieInfo, error) {
	return av.GetMovieInfoByURL(fmt.Sprintf(movieURL, id))
}

func (av *AiRAV) GetMovieInfoByURL(rawURL string) (info *model.MovieInfo, err error) {
	id, err := av.ParseMovieIDFromURL(rawURL)
	if err != nil {
		return nil, err
	}
	if id == "" {
		return nil, provider.ErrInvalidURL
	}

	info = &model.MovieInfo{
		ID:            id,
		Provider:      av.Name(),
		Homepage:      fmt.Sprintf(movieURL, id),
		Actors:        []string{},
		PreviewImages: []string{},
		Genres:        []string{},
	}

	var heading string
	c := av.ClonedCollector()

	// Number lives in the info list ("番號" row).
	c.OnXML(`//li[contains(text(),"番")]/span`, func(e *colly.XMLElement) {
		if info.Number == "" {
			info.Number = stripSourceLabel(strings.TrimSpace(e.Text))
		}
	})
	// Heading holds "NUMBER title" in one h1.
	c.OnXML(`//div[contains(@class,"video-title")]//h1`, func(e *colly.XMLElement) {
		heading = strings.TrimSpace(e.Text)
	})
	c.OnXML(`//meta[@property="og:image"]`, func(e *colly.XMLElement) {
		if info.CoverURL == "" {
			cover := e.Request.AbsoluteURL(e.Attr("content"))
			info.CoverURL = cover
			info.ThumbURL = cover
		}
	})
	c.OnXML(`//a[contains(@href,"actor?id=")]`, func(e *colly.XMLElement) {
		if name := strings.TrimSpace(e.Text); name != "" {
			info.Actors = append(info.Actors, name)
		}
	})
	c.OnXML(`//a[contains(@href,"tag?fid=")]`, func(e *colly.XMLElement) {
		if info.Maker == "" {
			info.Maker = strings.TrimSpace(e.Text)
		}
	})
	c.OnXML(`//a[contains(@href,"tag?tid=")]`, func(e *colly.XMLElement) {
		if tag := strings.TrimSpace(e.Text); tag != "" {
			info.Genres = append(info.Genres, tag)
		}
	})
	c.OnXML(`//div[contains(@class,"video-item")]//i[contains(@class,"fa-clock")]/parent::*`, func(e *colly.XMLElement) {
		info.ReleaseDate = parser.ParseDate(e.Text)
	})
	c.OnXML(`//div[contains(@class,"video-info")]/p`, func(e *colly.XMLElement) {
		if text := strings.TrimSpace(e.Text); text != "" {
			if info.Summary != "" {
				info.Summary += "\n"
			}
			info.Summary += text
		}
	})

	c.OnScraped(func(_ *colly.Response) {
		// Split the heading into its leading number token and the title.
		if heading != "" {
			token := leadingToken(heading)
			if info.Number != "" && sameNumber(stripSourceLabel(token), info.Number) {
				info.Title = strings.TrimSpace(strings.TrimPrefix(heading, token))
			} else {
				info.Title = heading
			}
		}
	})

	if visitErr := c.Visit(fmt.Sprintf(movieURL, id)); visitErr != nil {
		return nil, visitErr
	}
	if info.Number == "" || heading == "" {
		return nil, provider.ErrInfoNotFound
	}
	return info, nil
}

func (av *AiRAV) NormalizeMovieKeyword(keyword string) string {
	return strings.ToUpper(strings.TrimSpace(keyword))
}

func (av *AiRAV) SearchMovie(keyword string) (results []*model.MovieSearchResult, err error) {
	// FC2 videos are always listed as "FC2-PPV-<id>"; try that form
	// first, then the keyword as given.
	terms := []string{keyword}
	if fc2IDRe.MatchString(keyword) {
		if digits := regexp.MustCompile(`\d+$`).FindString(keyword); digits != "" {
			terms = append([]string{"FC2-PPV-" + digits}, terms...)
		}
	}
	seen := map[string]bool{}
	for _, term := range terms {
		termResults, termErr := av.searchTerm(term, keyword)
		if termErr != nil {
			return nil, termErr
		}
		for _, r := range termResults {
			if !seen[r.ID] {
				seen[r.ID] = true
				results = append(results, r)
			}
		}
		if len(results) > 0 {
			break
		}
	}
	return results, nil
}

func (av *AiRAV) searchTerm(term, number string) (results []*model.MovieSearchResult, err error) {
	c := av.ClonedCollector()
	c.OnXML(`//div[contains(@class,"oneVideo")]`, func(e *colly.XMLElement) {
		href := e.ChildAttr(".//a", "href")
		if href == "" {
			return
		}
		homepage := e.Request.AbsoluteURL(href)
		id, _ := av.ParseMovieIDFromURL(homepage)
		if id == "" {
			return
		}
		title := strings.TrimSpace(e.ChildText(".//h5"))
		token := stripSourceLabel(leadingToken(title))
		if !sameNumber(token, number) {
			return // titles start with the number; anything else is a miss.
		}
		thumb := e.Request.AbsoluteURL(e.ChildAttr(".//img", "src"))
		results = append(results, &model.MovieSearchResult{
			ID:       id,
			Number:   strings.ToUpper(token),
			Title:    strings.TrimSpace(strings.TrimPrefix(title, leadingToken(title))),
			Provider: av.Name(),
			Homepage: homepage,
			ThumbURL: thumb,
			CoverURL: thumb,
		})
	})

	if visitErr := c.Visit(fmt.Sprintf(searchURL, url.QueryEscape(term))); visitErr != nil {
		return nil, visitErr
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

// stripSourceLabel removes a site-source prefix such as
// "caribbeancom_051318-665" -> "051318-665". Numbers that merely start
// with a label word (e.g. "HEYZO-3607") are left untouched.
func stripSourceLabel(number string) string {
	folded := strings.ToLower(number)
	for _, label := range sourceLabels {
		if !strings.HasPrefix(folded, label) {
			continue
		}
		rest := number[len(label):]
		if len(rest) == 0 {
			continue
		}
		switch rest[0] {
		case '_', '-':
			rest = rest[1:]
		default:
			if rest[0] < '0' || rest[0] > '9' {
				continue
			}
		}
		if rest != "" {
			return rest
		}
	}
	return number
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
