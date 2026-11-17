package wikipedia

import (
	"encoding/json"
	"fmt"
	"math"
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
	_ provider.ActorProvider = (*Wikipedia)(nil)
	_ provider.ActorSearcher = (*Wikipedia)(nil)
	_ provider.Fetcher       = (*Wikipedia)(nil)
)

const (
	Name     = "Wikipedia"
	Priority = 1000 + 2
)

const (
	wikidataBaseURL = "https://www.wikidata.org"
	userAgent       = "MetaTube-SDK/1.0 (https://github.com/metatube-community/metatube-sdk-go)"
)

// AV keywords for entity descriptions. CJK "女優/男優" alone must NOT
// match: in Japanese it means any actress. The industry+role pair rule
// below covers split English descriptions like "Japanese adult video
// actress".
var avKeywords = []string{
	"av idol", "av actress", "av actor", "adult actress", "adult actor",
	"adult model", "porn actress", "porn actor", "pornographic",
	"japanese idol", "gravure",
	"av女優", "av女优", "av男優", "av男优", "avアイドル", "av監督",
	"アダルト", "成人映画", "成人影片",
}

var (
	avIndustryRe = regexp.MustCompile(`(?i)\b(?:av|adult|porn|xxx)\b`)
	avRoleRe     = regexp.MustCompile(`(?i)\b(?:actress|actor|idol|model|star)\b`)
	// avOccupations are the Wikidata occupation items that mark an AV
	// performer (P106).
	avOccupations = map[string]bool{
		"Q1079215": true, // AV女優 / AV idol
		"Q8380347": true, // AV男優
		"Q488111":  true, // pornographic film actor
	}
	centimetreUnit = "http://www.wikidata.org/entity/Q174728"
)

var (
	citationRe  = regexp.MustCompile(`[\[［]\s*(?:\d+|注\s*\d*|注釈\s*\d*|要出典|要ページ番号)\s*[\]］]`)
	editLinkRe  = regexp.MustCompile(`[\[［]\s*(?:編集|編輯|edit)\s*[\]］]`)
	wdTimeRe    = regexp.MustCompile(`^[+-]?(\d{4})-(\d{2})-(\d{2})`)
	heightRe    = regexp.MustCompile(`^(\d{2,3}(?:\.\d+)?)\s*(?:cm|厘米)`)
	sizesRe     = regexp.MustCompile(`^(\d{2,3}(?:\.\d+)?)\s*-\s*(\d{2,3}(?:\.\d+)?)\s*-\s*(\d{2,3}(?:\.\d+)?)\s*(?:cm|厘米)`)
	cupRe       = regexp.MustCompile(`^([A-Za-z]{1,2})\s*-?\s*\d{0,3}$`)
	bloodTypeRe = regexp.MustCompile(`^([ABO]{1,2})型`)
	qidRe       = regexp.MustCompile(`^Q\d+$`)
)

// sectionKeywords: article sections whose paragraphs are included in the
// overview, besides the lead section.
var sectionKeywords = []string{
	"人物", "简历", "简介", "经历", "履历", "略历", "来历", "生平", "生涯",
	"背景", "私生活", "个人生活", "プロフィール", "生い立ち",
	"early life", "career", "biography", "personal life", "background",
}

const (
	overviewMaxChars = 1500
	minTailChars     = 30
)

type Wikipedia struct {
	*fetch.Fetcher
	*scraper.Scraper
}

func New() *Wikipedia {
	return &Wikipedia{
		Fetcher: fetch.Default(&fetch.Config{Referer: wikidataBaseURL}),
		Scraper: scraper.NewDefaultScraper(
			Name, wikidataBaseURL, Priority,
			language.Japanese,
			scraper.WithUserAgent(userAgent)),
	}
}

func (w *Wikipedia) NormalizeActorID(id string) string {
	return strings.ToUpper(strings.TrimSpace(id))
}

func (w *Wikipedia) ParseActorIDFromURL(rawURL string) (string, error) {
	homepage, err := url.Parse(rawURL)
	if err != nil {
		return "", err
	}
	// Wikidata URLs carry the QID in the path: /wiki/Q12345.
	for _, segment := range strings.Split(homepage.Path, "/") {
		if qidRe.MatchString(segment) {
			return segment, nil
		}
	}
	return "", nil
}

func (w *Wikipedia) GetActorInfoByID(id string) (*model.ActorInfo, error) {
	if !qidRe.MatchString(id) {
		return nil, provider.ErrInvalidID
	}
	entity, err := w.fetchEntity(id)
	if err != nil {
		return nil, err
	}
	if entity == nil {
		return nil, provider.ErrInfoNotFound
	}
	return w.buildActorInfo(id, entity)
}

func (w *Wikipedia) GetActorInfoByURL(rawURL string) (*model.ActorInfo, error) {
	id, err := w.ParseActorIDFromURL(rawURL)
	if err != nil || id == "" {
		return nil, provider.ErrInvalidURL
	}
	return w.GetActorInfoByID(id)
}

func (w *Wikipedia) SearchActor(keyword string) (results []*model.ActorSearchResult, err error) {
	keyword = strings.TrimSpace(keyword)
	if keyword == "" {
		return nil, provider.ErrInvalidKeyword
	}
	candidates, err := w.wikidataSearch(keyword)
	if err != nil {
		return nil, err
	}
	for _, qid := range candidates {
		entity, fetchErr := w.fetchEntity(qid)
		if fetchErr != nil || entity == nil {
			continue
		}
		if !isAVEntity(entity) || !nameMatches(entity, keyword) {
			continue
		}
		name := preferLabel(entity.Labels)
		if name == "" {
			continue
		}
		results = append(results, &model.ActorSearchResult{
			ID:       qid,
			Name:     name,
			Provider: w.Name(),
			Homepage: fmt.Sprintf("%s/wiki/%s", wikidataBaseURL, qid),
			Aliases:  labelAliases(entity.Labels, name),
			Images:   []string{},
		})
		// First verified entity wins; wbsearchentities is fuzzy and
		// later candidates are usually different people.
		break
	}
	return results, nil
}

// --- Wikidata API ---

type wdTerm struct {
	Value string `json:"value"`
}

type wdClaim struct {
	MainSnak struct {
		DataValue *struct {
			Value json.RawMessage `json:"value"`
		} `json:"datavalue"`
	} `json:"mainsnak"`
}

type wdSitelink struct {
	Title string `json:"title"`
}

type wdEntity struct {
	Labels       map[string]wdTerm     `json:"labels"`
	Descriptions map[string]wdTerm     `json:"descriptions"`
	Aliases      map[string][]wdTerm   `json:"aliases"`
	Claims       map[string][]wdClaim  `json:"claims"`
	Sitelinks    map[string]wdSitelink `json:"sitelinks"`
}

type wdSearchResponse struct {
	Search []struct {
		ID          string `json:"id"`
		Description string `json:"description"`
	} `json:"search"`
}

type wdEntityResponse struct {
	Entities map[string]*wdEntity `json:"entities"`
}

func (w *Wikipedia) fetchJSON(pageURL string, out any) error {
	c := w.ClonedCollector()
	var decodeErr error
	c.OnResponse(func(r *colly.Response) {
		if err := json.Unmarshal(r.Body, out); err != nil {
			decodeErr = err
		}
	})
	if err := c.Visit(pageURL); err != nil {
		return err
	}
	return decodeErr
}

func (w *Wikipedia) wikidataSearch(keyword string) ([]string, error) {
	var qids []string
	seen := map[string]bool{}
	var lastErr error
	for _, lang := range []string{"ja", "zh", "en"} {
		pageURL := fmt.Sprintf("%s/w/api.php?action=wbsearchentities&search=%s&language=%s&uselang=%s&limit=5&format=json",
			wikidataBaseURL, url.QueryEscape(keyword), lang, lang)
		var resp wdSearchResponse
		if err := w.fetchJSON(pageURL, &resp); err != nil {
			lastErr = err
			continue
		}
		for _, item := range resp.Search {
			if !qidRe.MatchString(item.ID) || seen[item.ID] {
				continue
			}
			if !matchAVKeyword(item.Description) {
				continue
			}
			seen[item.ID] = true
			qids = append(qids, item.ID)
		}
	}
	if len(qids) == 0 && lastErr != nil {
		return nil, lastErr
	}
	return qids, nil
}

func (w *Wikipedia) fetchEntity(qid string) (*wdEntity, error) {
	var resp wdEntityResponse
	if err := w.fetchJSON(fmt.Sprintf("%s/wiki/Special:EntityData/%s.json", wikidataBaseURL, qid), &resp); err != nil {
		return nil, err
	}
	return resp.Entities[qid], nil
}

func matchAVKeyword(description string) bool {
	lower := strings.ToLower(description)
	for _, keyword := range avKeywords {
		if strings.Contains(lower, strings.ToLower(keyword)) {
			return true
		}
	}
	return avIndustryRe.MatchString(description) && avRoleRe.MatchString(description)
}

func isAVEntity(entity *wdEntity) bool {
	for _, desc := range entity.Descriptions {
		if matchAVKeyword(desc.Value) {
			return true
		}
	}
	for _, claim := range entity.Claims["P106"] {
		if id := claimItemID(claim); avOccupations[id] {
			return true
		}
	}
	return false
}

func nameMatches(entity *wdEntity, keyword string) bool {
	for _, term := range entity.Labels {
		if sameName(term.Value, keyword) {
			return true
		}
	}
	for _, terms := range entity.Aliases {
		for _, term := range terms {
			if sameName(term.Value, keyword) {
				return true
			}
		}
	}
	return false
}

func sameName(a, b string) bool {
	fold := func(s string) string {
		return strings.TrimSpace(norm.NFKC.String(s))
	}
	return a != "" && strings.EqualFold(fold(a), fold(b))
}

func preferLabel(labels map[string]wdTerm) string {
	for _, lang := range []string{"ja", "zh", "zh-cn", "zh-tw", "en"} {
		if term, ok := labels[lang]; ok && term.Value != "" {
			return term.Value
		}
	}
	for _, term := range labels {
		if term.Value != "" {
			return term.Value
		}
	}
	return ""
}

func labelAliases(labels map[string]wdTerm, exclude string) []string {
	var out []string
	seen := map[string]bool{exclude: true}
	for _, lang := range []string{"ja", "zh", "zh-cn", "zh-tw", "en"} {
		if term, ok := labels[lang]; ok && term.Value != "" && !seen[term.Value] {
			seen[term.Value] = true
			out = append(out, term.Value)
		}
	}
	if out == nil {
		out = []string{}
	}
	return out
}

// claimItemID extracts the QID from an entity-valued claim.
func claimItemID(claim wdClaim) string {
	if claim.MainSnak.DataValue == nil {
		return ""
	}
	var value struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(claim.MainSnak.DataValue.Value, &value); err != nil {
		return ""
	}
	return value.ID
}

// claimTime extracts a date from a time-valued claim (P569).
func claimTime(entity *wdEntity, pid string) dt.Date {
	claims := entity.Claims[pid]
	if len(claims) == 0 || claims[0].MainSnak.DataValue == nil {
		return dt.Date{}
	}
	var value struct {
		Time string `json:"time"`
	}
	if err := json.Unmarshal(claims[0].MainSnak.DataValue.Value, &value); err != nil {
		return dt.Date{}
	}
	m := wdTimeRe.FindStringSubmatch(value.Time)
	if len(m) != 4 {
		return dt.Date{}
	}
	year, _ := strconv.Atoi(m[1])
	month, _ := strconv.Atoi(m[2])
	day, _ := strconv.Atoi(m[3])
	return parser.ParseDate(fmt.Sprintf("%04d-%02d-%02d", year, month, day))
}

// claimQuantity extracts an integer centimetre value (P2048).
func claimQuantity(entity *wdEntity, pid string) int {
	claims := entity.Claims[pid]
	if len(claims) == 0 || claims[0].MainSnak.DataValue == nil {
		return 0
	}
	var value struct {
		Amount string `json:"amount"`
		Unit   string `json:"unit"`
	}
	if err := json.Unmarshal(claims[0].MainSnak.DataValue.Value, &value); err != nil {
		return 0
	}
	if value.Unit != centimetreUnit {
		return 0
	}
	amount, err := strconv.ParseFloat(value.Amount, 64)
	if err != nil || math.Trunc(amount) != amount {
		return 0
	}
	return int(amount)
}

// claimCommonsImage builds a Commons FilePath URL from a P18 claim.
func claimCommonsImage(entity *wdEntity) string {
	claims := entity.Claims["P18"]
	if len(claims) == 0 || claims[0].MainSnak.DataValue == nil {
		return ""
	}
	var filename string
	if err := json.Unmarshal(claims[0].MainSnak.DataValue.Value, &filename); err != nil {
		return ""
	}
	filename = strings.TrimSpace(filename)
	if filename == "" {
		return ""
	}
	return fmt.Sprintf("https://commons.wikimedia.org/wiki/Special:FilePath/%s", url.PathEscape(filename))
}

// --- Info assembly ---

func (w *Wikipedia) buildActorInfo(qid string, entity *wdEntity) (*model.ActorInfo, error) {
	name := preferLabel(entity.Labels)
	if name == "" {
		return nil, provider.ErrInfoNotFound
	}
	info := &model.ActorInfo{
		ID:       qid,
		Name:     name,
		Provider: w.Name(),
		Homepage: fmt.Sprintf("%s/wiki/%s", wikidataBaseURL, qid),
		Aliases:  labelAliases(entity.Labels, name),
		Images:   []string{},
		Birthday: claimTime(entity, "P569"),
		Height:   claimQuantity(entity, "P2048"),
	}
	if image := claimCommonsImage(entity); image != "" {
		info.Images = append(info.Images, image)
	}

	page := w.fetchWikiPage(entity.Sitelinks)
	if page != nil {
		info.Homepage = page.url
		info.Summary = page.overview
		if page.height != 0 {
			info.Height = page.height // infobox wins over the P2048 claim.
		}
		if page.bust != 0 && page.waist != 0 && page.hip != 0 {
			info.Measurements = fmt.Sprintf("B:%d / W:%d / H:%d", page.bust, page.waist, page.hip)
		}
		if page.cup != "" {
			info.CupSize = page.cup
		}
		if page.bloodType != "" {
			info.BloodType = page.bloodType
		}
		for _, alias := range page.aliases {
			if alias != info.Name && !containsString(info.Aliases, alias) {
				info.Aliases = append(info.Aliases, alias)
			}
		}
		if timeIsZero(info.Birthday) && page.birthday != "" {
			info.Birthday = parser.ParseDate(page.birthday)
		}
	}
	// The REST summary extract is the primary Summary source: the
	// scraped overview above is empty in practice (the paragraph
	// selectors do not match current Wikipedia HTML), while the REST
	// API reliably returns the lead extract. Prefer the Chinese
	// article, then Japanese, then English. Any failure leaves the
	// scraped overview (possibly empty) in place.
	if summary := w.fetchSummary(entity.Sitelinks); summary != "" {
		info.Summary = summary
	}
	return info, nil
}

func timeIsZero(d dt.Date) bool {
	return time.Time(d).IsZero()
}

type wikiPage struct {
	url       string
	overview  string
	birthday  string
	height    int
	bust      int
	waist     int
	hip       int
	cup       string
	bloodType string
	aliases   []string
}

func (w *Wikipedia) fetchWikiPage(sitelinks map[string]wdSitelink) *wikiPage {
	for _, lang := range []string{"ja", "zh", "en"} {
		link, ok := sitelinks[lang+"wiki"]
		if !ok || link.Title == "" {
			continue
		}
		pageURL := wikiURL(lang, link.Title)
		if page := w.parseWikiPage(pageURL); page != nil {
			return page
		}
	}
	return nil
}

// wikiSummaryResponse is the subset of the Wikipedia REST summary API
// response we need:
// https://{lang}.wikipedia.org/api/rest_v1/page/summary/{title}
type wikiSummaryResponse struct {
	Extract string `json:"extract"`
}

// fetchSummary fetches the article lead extract via the Wikipedia REST
// summary API, preferring the Chinese article (the user reads Chinese;
// Amane's bios came from zh.wikipedia), then Japanese, then English.
// The request carries the provider's User-Agent (MetaTube-SDK/1.0).
// Any failure yields an empty summary; errors never propagate.
func (w *Wikipedia) fetchSummary(sitelinks map[string]wdSitelink) string {
	for _, lang := range []string{"zh", "ja", "en"} {
		link, ok := sitelinks[lang+"wiki"]
		if !ok || link.Title == "" {
			continue
		}
		title := strings.ReplaceAll(link.Title, " ", "_")
		pageURL := fmt.Sprintf("https://%s.wikipedia.org/api/rest_v1/page/summary/%s",
			lang, url.PathEscape(title))
		var resp wikiSummaryResponse
		if err := w.fetchJSON(pageURL, &resp); err != nil {
			continue
		}
		if extract := strings.TrimSpace(resp.Extract); extract != "" {
			return clipSummary(extract)
		}
	}
	return ""
}

// clipSummary bounds the extract to the same length budget as the
// scraped overview, cutting at a sentence boundary when possible.
func clipSummary(extract string) string {
	if len([]rune(extract)) <= overviewMaxChars {
		return extract
	}
	return cutAtSentence(extract, overviewMaxChars)
}

func wikiURL(lang, title string) string {
	prefix := "wiki"
	if lang == "zh" {
		prefix = "zh-cn" // simplified-Chinese variant path.
	}
	escaped := url.PathEscape(strings.ReplaceAll(title, " ", "_"))
	escaped = strings.ReplaceAll(escaped, "%28", "(")
	escaped = strings.ReplaceAll(escaped, "%29", ")")
	return fmt.Sprintf("https://%s.wikipedia.org/%s/%s", lang, prefix, escaped)
}

func (w *Wikipedia) parseWikiPage(pageURL string) *wikiPage {
	page := &wikiPage{url: pageURL}
	var blocks []string
	selected := true // the lead section (before any heading) is selected.

	c := w.ClonedCollector()

	// Infobox rows: first match per field wins (unit-conversion rows
	// repeat the same labels with imperial units further down).
	c.OnXML(`//table[contains(@class,"infobox")]//tr`, func(e *colly.XMLElement) {
		label := strings.TrimSpace(e.ChildText("./th"))
		if label == "" {
			label = strings.TrimSpace(e.ChildText(".//th"))
		}
		value := strings.TrimSpace(e.ChildText("./td"))
		if value == "" {
			value = strings.TrimSpace(e.ChildText(".//td"))
		}
		if label == "" || value == "" {
			return
		}
		cleaned := cleanInline(value)
		if page.height == 0 && (strings.Contains(label, "身長") || strings.Contains(label, "身高")) {
			if m := heightRe.FindStringSubmatch(cleaned); len(m) == 2 {
				page.height = roundCM(m[1])
			}
		}
		if page.bust == 0 && (strings.Contains(label, "スリーサイズ") || strings.Contains(label, "三围")) {
			if m := sizesRe.FindStringSubmatch(cleaned); len(m) == 4 {
				page.bust = roundCM(m[1])
				page.waist = roundCM(m[2])
				page.hip = roundCM(m[3])
			}
		}
		if page.cup == "" && (strings.Contains(label, "ブラサイズ") || strings.Contains(label, "罩杯")) {
			if m := cupRe.FindStringSubmatch(cleaned); len(m) == 2 {
				page.cup = strings.ToUpper(m[1])
			}
		}
		if page.bloodType == "" && strings.Contains(label, "血液型") {
			if m := bloodTypeRe.FindStringSubmatch(cleaned); len(m) == 2 {
				page.bloodType = m[1]
			}
		}
		if strings.Contains(label, "別名") {
			for _, alias := range strings.FieldsFunc(cleaned, func(r rune) bool { return r == '、' || r == '，' || r == ',' }) {
				alias = strings.TrimSpace(regexp.MustCompile(`[（(][^）)]*[）)]`).ReplaceAllString(alias, ""))
				if alias != "" {
					page.aliases = append(page.aliases, alias)
				}
			}
		}
		if page.birthday == "" && (strings.Contains(label, "生年") || strings.Contains(label, "出生")) {
			page.birthday = cleaned
		}
	})

	// Lead + whitelisted sections, in document order.
	c.OnXML(`//div[contains(@class,"mw-parser-output")]/h2 | //div[contains(@class,"mw-parser-output")]/p`, func(e *colly.XMLElement) {
		switch e.Name {
		case "h2":
			title := cleanInline(editLinkRe.ReplaceAllString(e.Text, ""))
			selected = isSection(title)
		case "p":
			if !selected {
				return
			}
			if text := cleanInline(e.Text); text != "" {
				blocks = append(blocks, text)
			}
		}
	})

	if err := c.Visit(pageURL); err != nil {
		return nil
	}
	page.overview = clipBlocks(blocks)
	return page
}

func isSection(title string) bool {
	folded := strings.ToLower(title)
	for _, keyword := range sectionKeywords {
		if strings.Contains(folded, strings.ToLower(keyword)) {
			return true
		}
	}
	return false
}

// cleanInline strips citation markers and collapses whitespace.
func cleanInline(text string) string {
	text = citationRe.ReplaceAllString(text, "")
	return strings.Join(strings.Fields(text), " ")
}

func clipBlocks(blocks []string) string {
	var out []rune
	for _, block := range blocks {
		sep := ""
		if len(out) > 0 {
			sep = "\n\n"
		}
		room := overviewMaxChars - len(out) - len([]rune(sep))
		runes := []rune(block)
		if len(runes) <= room {
			out = append(out, []rune(sep)...)
			out = append(out, runes...)
			continue
		}
		if room >= minTailChars {
			out = append(out, []rune(sep)...)
			out = append(out, []rune(cutAtSentence(block, room))...)
		}
		break
	}
	return string(out)
}

func cutAtSentence(text string, limit int) string {
	runes := []rune(text)
	head := runes[:limit-1]
	cut := -1
	for i, r := range head {
		switch r {
		case '。', '．', '.', '！', '!', '？', '?', '…':
			cut = i
		}
	}
	if cut >= 0 {
		head = head[:cut+1]
	}
	return strings.TrimSpace(string(head)) + "…"
}

func roundCM(raw string) int {
	f, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return 0
	}
	return int(math.Floor(f + 0.5))
}

func containsString(values []string, s string) bool {
	for _, v := range values {
		if v == s {
			return true
		}
	}
	return false
}

func init() {
	provider.Register(Name, New)
}
