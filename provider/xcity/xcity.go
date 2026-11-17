package xcity

import (
	"fmt"
	"net/url"
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
	_ provider.MovieProvider = (*XCity)(nil)
	_ provider.MovieSearcher = (*XCity)(nil)
	_ provider.Fetcher       = (*XCity)(nil)
)

const (
	Name     = "XCity"
	Priority = 1000 - 5
)

const (
	baseURL    = "https://xcity.jp"
	refererURL = "https://xcity.jp/main/"
	movieURL   = "https://xcity.jp/avod/detail/?id=%s"
	searchURL  = "https://xcity.jp/result_published/?q=%s&num=30"
)

// searchCandidates bounds how many search hits are verified against their
// detail pages: result rows carry titles only, no numbers.
const searchCandidates = 3

type XCity struct {
	*fetch.Fetcher
	*scraper.Scraper
}

func New() *XCity {
	return &XCity{
		Fetcher: fetch.Default(&fetch.Config{Referer: refererURL}),
		Scraper: scraper.NewDefaultScraper(
			Name, baseURL, Priority,
			language.Japanese,
			scraper.WithHeaders(map[string]string{
				// Search and detail pages require an on-site Referer;
				// without it the site swaps the query for a default
				// feature page and returns 200 with no results.
				"Referer": refererURL,
			})),
	}
}

func (xc *XCity) NormalizeMovieID(id string) string {
	return strings.TrimSpace(id)
}

func (xc *XCity) ParseMovieIDFromURL(rawURL string) (string, error) {
	homepage, err := url.Parse(rawURL)
	if err != nil {
		return "", err
	}
	return homepage.Query().Get("id"), nil
}

func (xc *XCity) GetMovieInfoByID(id string) (*model.MovieInfo, error) {
	return xc.GetMovieInfoByURL(fmt.Sprintf(movieURL, id))
}

func (xc *XCity) GetMovieInfoByURL(rawURL string) (info *model.MovieInfo, err error) {
	id, err := xc.ParseMovieIDFromURL(rawURL)
	if err != nil {
		return nil, err
	}
	if id == "" {
		return nil, provider.ErrInvalidURL
	}

	info = &model.MovieInfo{
		ID:            id,
		Provider:      xc.Name(),
		Homepage:      fmt.Sprintf(movieURL, id),
		Actors:        []string{},
		PreviewImages: []string{},
		Genres:        []string{},
	}

	c := xc.ClonedCollector()

	c.OnXML(`//span[@id="program_detail_title"]`, func(e *colly.XMLElement) {
		info.Title = strings.TrimSpace(e.Text)
	})
	c.OnXML(`//span[@id="hinban"]`, func(e *colly.XMLElement) {
		info.Number = strings.TrimSpace(e.Text)
	})
	c.OnXML(`//span[@id="program_detail_maker_name"]`, func(e *colly.XMLElement) {
		info.Maker = strings.TrimSpace(e.Text)
	})
	c.OnXML(`//span[@id="program_detail_label_name"]`, func(e *colly.XMLElement) {
		info.Label = strings.TrimSpace(e.Text)
	})
	c.OnXML(`//li[span[normalize-space()="出演"]]//a[contains(@href,"/idol/detail/")]`, func(e *colly.XMLElement) {
		if name := strings.TrimSpace(e.Text); name != "" {
			info.Actors = append(info.Actors, name)
		}
	})
	c.OnXML(`//li[span[normalize-space()="ジャンル"]]//a[contains(@href,"/avod/genre/")]`, func(e *colly.XMLElement) {
		if tag := strings.TrimSpace(e.Text); tag != "" {
			info.Genres = append(info.Genres, tag)
		}
	})
	c.OnXML(`//li[span[normalize-space()="シリーズ"]]//a`, func(e *colly.XMLElement) {
		if info.Series == "" {
			info.Series = strings.TrimSpace(e.Text)
		}
	})
	c.OnXML(`//li[span[normalize-space()="発売日"]]`, func(e *colly.XMLElement) {
		info.ReleaseDate = parser.ParseDate(e.Text)
	})
	c.OnXML(`//li[span[normalize-space()="収録時間"]]`, func(e *colly.XMLElement) {
		info.Runtime = parser.ParseRuntime(e.Text)
	})
	c.OnXML(`//p[contains(@class,"lead")]`, func(e *colly.XMLElement) {
		if text := strings.TrimSpace(e.Text); text != "" {
			if info.Summary != "" {
				info.Summary += "\n"
			}
			info.Summary += text
		}
	})
	// Cover: the panel img is the portrait jacket; the package-b link is
	// the landscape spread.
	c.OnXML(`//div[@class="photo"]//img`, func(e *colly.XMLElement) {
		if info.CoverURL == "" {
			cover := e.Request.AbsoluteURL(e.Attr("src"))
			info.CoverURL = cover
			info.ThumbURL = cover
		}
	})
	c.OnXML(`//div[@class="photo"]//a[contains(@href,"/package-b/")]`, func(e *colly.XMLElement) {
		if info.BigCoverURL == "" {
			info.BigCoverURL = e.Request.AbsoluteURL(e.Attr("href"))
		}
	})
	// Sample images are thumbnails; the large versions live under
	// scene/large with the same file names.
	c.OnXML(`//div[@id="sample_images"]//a[contains(@href,"/scene/")]`, func(e *colly.XMLElement) {
		still := e.Request.AbsoluteURL(e.Attr("href"))
		still = strings.Replace(still, "/scene/small/", "/scene/large/", 1)
		info.PreviewImages = append(info.PreviewImages, still)
	})

	if visitErr := c.Visit(fmt.Sprintf(movieURL, id)); visitErr != nil {
		return nil, visitErr
	}
	if info.Number == "" || info.Title == "" {
		return nil, provider.ErrInfoNotFound
	}
	return info, nil
}

func (xc *XCity) NormalizeMovieKeyword(keyword string) string {
	return strings.ToUpper(strings.TrimSpace(keyword))
}

func (xc *XCity) SearchMovie(keyword string) (results []*model.MovieSearchResult, err error) {
	// The site matches the compact (dash-less) number form.
	query := strings.ReplaceAll(keyword, "-", "")
	var hrefs []string
	seen := map[string]bool{}

	c := xc.ClonedCollector()
	c.OnXML(`//table[contains(@class,"resultList")]//a[contains(@href,"/avod/detail/")]`, func(e *colly.XMLElement) {
		href := e.Request.AbsoluteURL(e.Attr("href"))
		if !seen[href] {
			seen[href] = true
			hrefs = append(hrefs, href)
		}
	})

	if visitErr := c.Visit(fmt.Sprintf(searchURL, url.QueryEscape(query))); visitErr != nil {
		return nil, visitErr
	}

	// Result rows carry titles only; confirm each candidate's number on
	// its detail page.
	for i, href := range hrefs {
		if i >= searchCandidates {
			break
		}
		info, infoErr := xc.GetMovieInfoByURL(href)
		if infoErr != nil || !sameNumber(info.Number, keyword) {
			continue
		}
		results = append(results, info.ToSearchResult())
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

func init() {
	provider.Register(Name, New)
}
