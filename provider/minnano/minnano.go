package minnano

import (
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/gocolly/colly/v2"
	"golang.org/x/text/language"
	"golang.org/x/text/unicode/norm"
	dt "gorm.io/datatypes"

	"github.com/metatube-community/metatube-sdk-go/common/fetch"
	"github.com/metatube-community/metatube-sdk-go/common/parser"
	"github.com/metatube-community/metatube-sdk-go/model"
	"github.com/metatube-community/metatube-sdk-go/provider"
	"github.com/metatube-community/metatube-sdk-go/provider/internal/scraper"
)

var (
	_ provider.ActorProvider = (*Minnano)(nil)
	_ provider.ActorSearcher = (*Minnano)(nil)
	_ provider.Fetcher       = (*Minnano)(nil)
)

const (
	Name     = "Minnano"
	Priority = 1000 + 4
)

const baseURL = "https://www.minnano-av.com"

// NOTE: the actress search endpoint (search_result.php) sits behind a
// Cloudflare challenge from the egresses available to this project, while
// detail pages are reachable from some egresses. Search is implemented
// with standard fetching only and fails closed while challenged; detail
// parsing is covered by fixture tests made from real pages.

var (
	actressIDRe = regexp.MustCompile(`(?i)actress(\d+)\.html`)
	figureRe    = regexp.MustCompile(`(?i)(?:^|[^A-Za-z])([TBWH])\s*(\d+)`)
	cupRe       = regexp.MustCompile(`(?i)([A-Z]+)カップ`)
	// readingRe matches a name reading: kana-only, or a latin name.
	readingRe = regexp.MustCompile(`^(?:[ぁ-んァ-ヶー・\s]+|[A-Za-z0-9][A-Za-z0-9 .'\-]*)$`)
	// eraDateRe matches Japanese era dates, e.g. "平成7年8月30日".
	eraDateRe = regexp.MustCompile(`(令和|平成|昭和)(元|\d+)年\s*(\d+)月\s*(\d+)日`)
)

var eraBaseYear = map[string]int{
	"令和": 2019,
	"平成": 1989,
	"昭和": 1926,
}

type Minnano struct {
	*fetch.Fetcher
	*scraper.Scraper

	// endpoint is the site base URL; tests override it with a fixture
	// server. It defaults to baseURL.
	endpoint string
}

func New() *Minnano {
	return &Minnano{
		Fetcher: fetch.Default(&fetch.Config{Referer: baseURL}),
		Scraper: scraper.NewDefaultScraper(
			Name, baseURL, Priority,
			language.Japanese,
			scraper.WithHeaders(map[string]string{
				"Referer": baseURL,
			}),
			scraper.WithCookies(baseURL, []*http.Cookie{
				// The site injects an age-confirmation layer via JS
				// when this cookie is absent; preset it.
				{Name: "age_verified", Value: "1"},
			})),
		endpoint: baseURL,
	}
}

func (m *Minnano) detailURL(id string) string {
	return fmt.Sprintf("%s/actress%s.html", m.endpoint, id)
}

func (m *Minnano) searchURL(keyword string) string {
	return fmt.Sprintf("%s/search_result.php?search_scope=actress&search_word=%s&search=Go",
		m.endpoint, url.QueryEscape(keyword))
}

func (m *Minnano) NormalizeActorID(id string) string {
	return strings.TrimSpace(id)
}

func (m *Minnano) ParseActorIDFromURL(rawURL string) (string, error) {
	homepage, err := url.Parse(rawURL)
	if err != nil {
		return "", err
	}
	if match := actressIDRe.FindStringSubmatch(homepage.Path); len(match) == 2 {
		return match[1], nil
	}
	return "", nil
}

func (m *Minnano) GetActorInfoByID(id string) (*model.ActorInfo, error) {
	return m.GetActorInfoByURL(m.detailURL(id))
}

func (m *Minnano) GetActorInfoByURL(rawURL string) (info *model.ActorInfo, err error) {
	id, err := m.ParseActorIDFromURL(rawURL)
	if err != nil {
		return nil, err
	}
	if id == "" {
		return nil, provider.ErrInvalidURL
	}

	info = &model.ActorInfo{
		ID:       id,
		Provider: m.Name(),
		Homepage: m.detailURL(id),
		Aliases:  []string{},
		Images:   []string{},
	}

	var (
		tags       = map[string]bool{}
		rawName    string
		birthday   string
		figureRaw  string
		ogImage    string
		canonical  string
		aliasRows  []string
		hasProfile bool
	)

	c := m.ClonedCollector()

	c.OnXML(`//div[contains(@class,"act-profile")]`, func(*colly.XMLElement) {
		hasProfile = true
	})
	// Name line: the first h2 inside the profile block.
	c.OnXML(`//div[contains(@class,"act-profile")]//h2`, func(e *colly.XMLElement) {
		if rawName == "" {
			rawName = strings.TrimSpace(e.Text)
		}
	})
	// Tag names: used to filter site/series names out of readings.
	c.OnXML(`//div[contains(@class,"act-profile")]//tr[td/span[normalize-space()="タグ"]]//div[contains(@class,"tagarea")]//a`, func(e *colly.XMLElement) {
		if tag := foldName(e.Text); tag != "" {
			tags[tag] = true
		}
	})
	// Profile field rows.
	c.OnXML(`//div[contains(@class,"act-profile")]//tr`, func(e *colly.XMLElement) {
		key := strings.TrimSpace(e.ChildText("./td/span"))
		if key == "" {
			return
		}
		value := strings.TrimSpace(e.ChildText("./td/p"))
		if value == "" {
			value = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(e.Text), key))
		}
		switch key {
		case "別名":
			aliasRows = append(aliasRows, value)
		case "生年月日":
			birthday = value
		case "サイズ":
			figureRaw = value
		}
	})
	c.OnXML(`//meta[@property="og:image"]`, func(e *colly.XMLElement) {
		if ogImage == "" {
			ogImage = e.Attr("content")
		}
	})
	c.OnXML(`//link[@rel="canonical"]`, func(e *colly.XMLElement) {
		if canonical == "" {
			canonical = e.Attr("href")
		}
	})
	// Fallback portrait when og:image is absent.
	c.OnXML(`//div[contains(@class,"thumb")]//img[contains(@src,"p_actress")]`, func(e *colly.XMLElement) {
		if ogImage == "" {
			ogImage = e.Attr("src")
		}
	})

	if visitErr := c.Visit(m.detailURL(id)); visitErr != nil {
		return nil, visitErr
	}
	if !hasProfile || rawName == "" {
		return nil, provider.ErrInfoNotFound
	}

	name, aliases := parseNameLine(rawName, tags)
	if name == "" {
		return nil, provider.ErrInfoNotFound
	}
	info.Name = name
	info.Aliases = append(info.Aliases, aliases...)
	for _, row := range aliasRows {
		aliasName, aliasReadings := parseNameLine(row, tags)
		if aliasName != "" && !tags[aliasName] {
			info.Aliases = append(info.Aliases, aliasName)
		}
		info.Aliases = append(info.Aliases, aliasReadings...)
	}
	info.Aliases = dedupePreserve(info.Aliases, info.Name)

	if birthday != "" {
		info.Birthday = parseBirthday(birthday)
	}
	height, bust, waist, hip, cup := parseFigure(figureRaw)
	info.Height = height
	info.CupSize = cup
	if bust != 0 && waist != 0 && hip != 0 {
		info.Measurements = fmt.Sprintf("B:%d / W:%d / H:%d", bust, waist, hip)
	}
	if ogImage != "" {
		if u, parseErr := url.Parse(ogImage); parseErr == nil {
			u.RawQuery = "" // stable URL: drop cache-busting queries.
			ogImage = u.String()
		}
		info.Images = append(info.Images, ogImage)
	}
	if canonical != "" {
		info.Homepage = canonical
	}
	return info, nil
}

func (m *Minnano) SearchActor(keyword string) (results []*model.ActorSearchResult, err error) {
	keyword = strings.TrimSpace(keyword)
	if keyword == "" {
		return nil, provider.ErrInvalidKeyword
	}

	c := m.ClonedCollector()

	detailPage := false
	c.OnXML(`//div[contains(@class,"act-profile")]`, func(*colly.XMLElement) {
		detailPage = true
	})
	var detailURL string
	c.OnXML(`//link[@rel="canonical"]`, func(e *colly.XMLElement) {
		if detailURL == "" {
			detailURL = e.Attr("href")
		}
	})
	c.OnXML(`//meta[@property="og:url"]`, func(e *colly.XMLElement) {
		if detailURL == "" {
			detailURL = e.Attr("content")
		}
	})

	c.OnXML(`//table[contains(@class,"tbllist") and contains(@class,"actress")]//tr[td]`, func(e *colly.XMLElement) {
		title := strings.TrimSpace(e.ChildText(`.//h2[contains(@class,"ttl")]/a`))
		href := e.ChildAttr(`.//h2[contains(@class,"ttl")]/a`, "href")
		if title == "" || href == "" || !actressIDRe.MatchString(href) {
			return
		}
		// The row title must equal the keyword once annotation parens
		// are stripped; unconfirmed rows are never returned.
		if !sameActorName(stripAnnotations(title), keyword) {
			return
		}
		homepage := e.Request.AbsoluteURL(strings.SplitN(href, "?", 2)[0])
		id, _ := m.ParseActorIDFromURL(homepage)
		if id == "" {
			return
		}
		results = append(results, &model.ActorSearchResult{
			ID:       id,
			Name:     stripAnnotations(title),
			Provider: m.Name(),
			Homepage: homepage,
			Aliases:  []string{},
			Images:   []string{},
		})
	})

	if visitErr := c.Visit(m.searchURL(keyword)); visitErr != nil {
		return nil, visitErr
	}
	if detailPage && len(results) == 0 && detailURL != "" {
		// An exact hit redirects to the detail page.
		if info, infoErr := m.GetActorInfoByURL(detailURL); infoErr == nil && info.IsValid() {
			results = append(results, info.ToSearchResult())
		}
	}
	return results, nil
}

// parseNameLine splits a name line such as "森末くるみ （もりすえくるみ /
// Morisue Kurumi）" into the name and its readings. Trailing parenthesized
// groups are peeled right to left; only the rightmost group's kana/latin
// fragments count as readings, the rest are annotations (e.g. source
// sites) and are dropped. Fragments matching page tags are dropped too.
func parseNameLine(raw string, tags map[string]bool) (string, []string) {
	text := foldName(raw)
	var groups []string
	for {
		head, inner, ok := splitTrailingGroup(text)
		if !ok {
			break
		}
		if head == "" {
			return "", nil
		}
		groups = append(groups, inner)
		text = head
	}
	if len(groups) == 0 {
		return text, nil
	}
	var readings []string
	for _, token := range strings.FieldsFunc(groups[0], func(r rune) bool { return r == '/' || r == '／' }) {
		token = strings.TrimSpace(token)
		if token == "" || tags[token] {
			continue
		}
		if readingRe.MatchString(token) && !containsString(readings, token) {
			readings = append(readings, token)
		}
	}
	return text, readings
}

// splitTrailingGroup splits "head (inner)" at the last parenthesized
// group, returning ok=false when the text does not end with a group.
func splitTrailingGroup(s string) (head, inner string, ok bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", "", false
	}
	r := []rune(s)
	last := r[len(r)-1]
	if last != ')' && last != '）' {
		return "", "", false
	}
	open := -1
	for i := len(r) - 1; i >= 0; i-- {
		if r[i] == '(' || r[i] == '（' {
			open = i
			break
		}
	}
	if open < 0 {
		return "", "", false
	}
	inner = strings.TrimSpace(string(r[open+1 : len(r)-1]))
	head = strings.TrimSpace(string(r[:open]))
	if strings.ContainsAny(inner, "()（）") {
		return "", "", false
	}
	return head, inner, true
}

// stripAnnotations removes all trailing parenthesized groups.
func stripAnnotations(name string) string {
	for {
		head, _, ok := splitTrailingGroup(name)
		if !ok {
			return foldName(name)
		}
		name = head
	}
}

// parseBirthday parses a birthday, including Japanese era dates.
func parseBirthday(raw string) dt.Date {
	raw = strings.TrimSpace(norm.NFKC.String(raw))
	if m := eraDateRe.FindStringSubmatch(raw); len(m) == 5 {
		yearNum := 1
		if m[2] != "元" {
			yearNum, _ = strconv.Atoi(m[2])
		}
		month, _ := strconv.Atoi(m[3])
		day, _ := strconv.Atoi(m[4])
		return dt.Date(time.Date(eraBaseYear[m[1]]+yearNum-1, time.Month(month), day, 0, 0, 0, 0, time.UTC))
	}
	return parser.ParseDate(raw)
}

// parseFigure parses a サイズ value such as "T160 B85(Fカップ) W60 H80".
// Missing slots stay zero: labels without values must not steal the
// values of other slots.
func parseFigure(raw string) (height, bust, waist, hip int, cup string) {
	text := norm.NFKC.String(raw)
	sizes := map[string]int{}
	for _, m := range figureRe.FindAllStringSubmatch(text, -1) {
		key := strings.ToUpper(m[1])
		if _, exists := sizes[key]; exists {
			continue
		}
		if v, err := strconv.Atoi(m[2]); err == nil {
			sizes[key] = v
		}
	}
	if m := cupRe.FindStringSubmatch(text); len(m) == 2 {
		cup = strings.ToUpper(m[1])
	}
	return sizes["T"], sizes["B"], sizes["W"], sizes["H"], cup
}

func foldName(s string) string {
	return strings.TrimSpace(norm.NFKC.String(s))
}

func sameActorName(a, b string) bool {
	return a != "" && strings.EqualFold(foldName(a), foldName(b))
}

func containsString(values []string, s string) bool {
	for _, v := range values {
		if v == s {
			return true
		}
	}
	return false
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
	provider.Register(Name, New)
}
