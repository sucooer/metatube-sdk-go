package javdb

import (
	"fmt"
	"net/url"
	"path"
	"regexp"
	"strings"

	"github.com/gocolly/colly/v2"
	"golang.org/x/text/language"
	"golang.org/x/text/unicode/norm"

	"github.com/metatube-community/metatube-sdk-go/common/fetch"
	"github.com/metatube-community/metatube-sdk-go/model"
	"github.com/metatube-community/metatube-sdk-go/provider"
	"github.com/metatube-community/metatube-sdk-go/provider/internal/scraper"
)

var (
	_ provider.ActorProvider = (*JavDBActor)(nil)
	_ provider.ActorSearcher = (*JavDBActor)(nil)
	_ provider.Fetcher       = (*JavDBActor)(nil)
)

// ActorPriority outranks Gfriends (999) so actor lookups prefer JavDB's
// names and aliases, but stays below the profile-rich providers (Minnano,
// AVBASE, Wikipedia) which fill measurements and birthdays that JavDB's
// actor pages do not carry.
const ActorPriority = 1000 + 1

const (
	actorURL       = "https://javdb.com/actors/%s?locale=zh"
	actorSearchURL = "https://javdb.com/search?q=%s&f=actor&locale=zh"
)

var (
	// actorIndexSlugs are the /actors/ index pages, not actor profiles.
	actorIndexSlugs = map[string]bool{
		"censored":   true,
		"uncensored": true,
		"western":    true,
	}
	// filmCountRe matches the "N 部影片" / "N movie(s)" meta token so it is
	// never mistaken for an alias.
	filmCountRe = regexp.MustCompile(`(?i)\d+\s*(?:部影片|movie\(s\))`)
	// uncensoredMarkerRe strips the "(無碼)" / "(Uncensored)" suffix JavDB
	// appends to uncensored actors' names.
	uncensoredMarkerRe = regexp.MustCompile(`(?i)\s*\((?:無碼|无码|uncensored)\)\s*$`)
	// bgImageRe extracts the avatar URL from a CSS background-image style.
	bgImageRe = regexp.MustCompile(`(?i)url\(\s*['"]?(https?://[^)'"]+)['"]?\s*\)`)
)

type JavDBActor struct {
	*fetch.Fetcher
	*scraper.Scraper
}

func NewActor() *JavDBActor {
	return &JavDBActor{
		Fetcher: fetch.Default(&fetch.Config{Referer: baseURL}),
		Scraper: scraper.NewDefaultScraper(
			Name, baseURL, ActorPriority,
			language.Japanese,
			scraper.WithHeaders(map[string]string{
				"Referer": baseURL,
			})),
	}
}

// NormalizeActorID keeps the ID as-is: actor slugs are case-sensitive.
func (db *JavDBActor) NormalizeActorID(id string) string {
	return strings.TrimSpace(id)
}

func (db *JavDBActor) ParseActorIDFromURL(rawURL string) (string, error) {
	homepage, err := url.Parse(rawURL)
	if err != nil {
		return "", err
	}
	slug := db.NormalizeActorID(path.Base(homepage.Path))
	if actorIndexSlugs[strings.ToLower(slug)] {
		return "", nil
	}
	return slug, nil
}

func (db *JavDBActor) GetActorInfoByID(id string) (*model.ActorInfo, error) {
	info, loginWalled, err := db.fetchActorInfo(fmt.Sprintf(actorURL, id))
	if err != nil {
		return nil, err
	}
	if loginWalled {
		return nil, provider.ErrInfoNotFound
	}
	return info, nil
}

func (db *JavDBActor) GetActorInfoByURL(rawURL string) (*model.ActorInfo, error) {
	id, err := db.ParseActorIDFromURL(rawURL)
	if err != nil || id == "" {
		return nil, provider.ErrInvalidURL
	}
	return db.GetActorInfoByID(id)
}

func (db *JavDBActor) fetchActorInfo(pageURL string) (info *model.ActorInfo, loginWalled bool, err error) {
	info = &model.ActorInfo{
		Provider: db.Name(),
		Homepage: pageURL,
		Aliases:  []string{},
		Images:   []string{},
	}

	c := db.ClonedCollector()

	c.OnXML(loginFormXPath, func(*colly.XMLElement) {
		loginWalled = true
	})

	// Names: "三上悠亜, 三上悠亞" — first is canonical, the rest aliases.
	c.OnXML(`//span[contains(@class,"actor-section-name")]`, func(e *colly.XMLElement) {
		names := splitActorNames(e.Text)
		if len(names) == 0 {
			return
		}
		info.Name = names[0]
		info.Aliases = append(info.Aliases, names[1:]...)
		if id, parseErr := db.ParseActorIDFromURL(e.Request.URL.String()); parseErr == nil {
			info.ID = id
		}
	})

	// Meta tokens: former names land here too; film counts and gender
	// markers are filtered out.
	c.OnXML(`//span[contains(@class,"section-meta")]`, func(e *colly.XMLElement) {
		for _, token := range splitActorNames(e.Text) {
			switch token {
			case "男優", "男优", "Male", "女優", "女优", "Female":
				continue // gender is not part of ActorInfo.
			}
			if filmCountRe.MatchString(token) {
				continue
			}
			info.Aliases = append(info.Aliases, token)
		}
	})

	// Avatar: CSS background image on the avatar span.
	c.OnXML(`//div[contains(@class,"actor-avatar")]//span[contains(@class,"avatar")]`, func(e *colly.XMLElement) {
		if m := bgImageRe.FindStringSubmatch(e.Attr("style")); len(m) == 2 {
			avatar := m[1]
			if !strings.Contains(strings.ToLower(avatar), "actor_unknow") {
				info.Images = append(info.Images, e.Request.AbsoluteURL(avatar))
			}
		}
	})

	c.OnScraped(func(r *colly.Response) {
		if id := info.ID; id != "" {
			info.Homepage = fmt.Sprintf("https://javdb.com/actors/%s", id)
		}
		info.Aliases = dedupePreserve(info.Aliases, info.Name)
	})

	if visitErr := c.Visit(pageURL); visitErr != nil {
		return nil, loginWalled, visitErr
	}
	if loginWalled || info.Name == "" {
		return nil, loginWalled, provider.ErrInfoNotFound
	}
	return info, false, nil
}

func (db *JavDBActor) SearchActor(keyword string) (results []*model.ActorSearchResult, err error) {
	keyword = strings.TrimSpace(keyword)
	if keyword == "" {
		return nil, provider.ErrInvalidKeyword
	}

	c := db.ClonedCollector()

	loginWalled := false
	c.OnXML(loginFormXPath, func(*colly.XMLElement) {
		loginWalled = true
	})

	// An exact hit sometimes lands directly on the detail page.
	detailPage := false
	c.OnXML(`//span[contains(@class,"actor-section-name")]`, func(*colly.XMLElement) {
		detailPage = true
	})

	c.OnXML(`//div[contains(@class,"actor-box")]//a[@href]`, func(e *colly.XMLElement) {
		href := e.Attr("href")
		id, _ := db.ParseActorIDFromURL(e.Request.AbsoluteURL(href))
		if id == "" {
			return
		}
		canonical := foldName(e.ChildText(".//strong"))
		if canonical == "" {
			canonical = foldName(strings.TrimSpace(e.Text))
		}
		candidates := []string{canonical}
		candidates = append(candidates, splitActorNames(e.Attr("title"))...)
		matched := false
		for _, candidate := range candidates {
			if sameActorName(candidate, keyword) {
				matched = true
				break
			}
		}
		if !matched {
			return // exact match only; never fall back to the first box.
		}
		results = append(results, &model.ActorSearchResult{
			ID:       id,
			Name:     canonical,
			Provider: db.Name(),
			Homepage: e.Request.AbsoluteURL(href),
			Aliases:  []string{},
			Images:   []string{},
		})
	})

	searchURL := fmt.Sprintf(actorSearchURL, url.QueryEscape(keyword))
	if visitErr := c.Visit(searchURL); visitErr != nil {
		return nil, visitErr
	}
	if loginWalled {
		return nil, provider.ErrInfoNotFound
	}
	if detailPage && len(results) == 0 {
		// The search redirected to a detail page: fetch it as the hit.
		if info, _, fetchErr := db.fetchActorInfo(searchURL); fetchErr == nil && info.IsValid() {
			results = append(results, info.ToSearchResult())
		}
	}
	return results, nil
}

// splitActorNames splits a comma-separated name list and strips the
// uncensored marker suffix.
func splitActorNames(raw string) []string {
	var names []string
	for _, part := range strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == '，'
	}) {
		name := foldName(uncensoredMarkerRe.ReplaceAllString(part, ""))
		if name != "" {
			names = append(names, name)
		}
	}
	return names
}

// foldName applies NFKC folding and trims whitespace so full/half-width
// variants of a name compare equal.
func foldName(s string) string {
	return strings.TrimSpace(norm.NFKC.String(s))
}

func sameActorName(a, b string) bool {
	return a != "" && strings.EqualFold(foldName(a), foldName(b))
}

func dedupePreserve(values []string, exclude string) []string {
	seen := map[string]bool{exclude: true}
	var out []string
	for _, v := range values {
		if v == "" || seen[v] {
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
	provider.Register(Name, NewActor)
}
