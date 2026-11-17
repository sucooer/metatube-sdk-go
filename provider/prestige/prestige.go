package prestige

import (
	"encoding/json"
	"fmt"
	"net/http"
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
	_ provider.MovieProvider = (*Prestige)(nil)
	_ provider.MovieSearcher = (*Prestige)(nil)
	_ provider.Fetcher       = (*Prestige)(nil)
)

const (
	Name     = "Prestige"
	Priority = 1000
)

const baseURL = "https://www.prestige-av.com"

// NOTE: prestige-av.com is restricted to Japanese IPs (CloudFront 403 for
// other egresses), so live lookups currently fail closed from the
// egresses available to this project. The implementation below uses
// standard fetching only (no bypass tooling) against the site's JSON API
// and works as soon as it is reachable. Parsing is covered by fixture
// tests.

var uuidRe = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

type Prestige struct {
	*fetch.Fetcher
	*scraper.Scraper

	// endpoint is the site base URL; tests override it with a fixture
	// server. It defaults to baseURL.
	endpoint string
}

func New() *Prestige {
	return &Prestige{
		Fetcher: fetch.Default(&fetch.Config{Referer: baseURL}),
		Scraper: scraper.NewDefaultScraper(
			Name, baseURL, Priority,
			language.Japanese,
			scraper.WithHeaders(map[string]string{
				"Referer": baseURL,
			})),
		endpoint: baseURL,
	}
}

func (p *Prestige) skuURL(skuID string) string {
	return fmt.Sprintf("%s/api/sku/item/%s", p.endpoint, skuID)
}

func (p *Prestige) productURL(uuid string) string {
	return fmt.Sprintf("%s/api/product/%s", p.endpoint, uuid)
}

func (p *Prestige) mediaURL(path string) string {
	if path == "" {
		return ""
	}
	if strings.HasPrefix(path, "http") {
		return path
	}
	return fmt.Sprintf("%s/api/media/%s", p.endpoint, path)
}

func (p *Prestige) NormalizeMovieID(id string) string {
	return strings.TrimSpace(id)
}

func (p *Prestige) ParseMovieIDFromURL(rawURL string) (string, error) {
	homepage, err := url.Parse(rawURL)
	if err != nil {
		return "", err
	}
	return path.Base(homepage.Path), nil
}

// fetchJSON GETs pageURL with an Accept: application/json header and
// decodes the response body into out.
func (p *Prestige) fetchJSON(pageURL string, out any) error {
	c := p.ClonedCollector()
	var decodeErr error
	c.OnResponse(func(r *colly.Response) {
		if err := json.Unmarshal(r.Body, out); err != nil {
			decodeErr = err
		}
	})
	headers := http.Header{}
	headers.Set("Accept", "application/json")
	if err := c.Request(http.MethodGet, pageURL, nil, nil, headers); err != nil {
		return err
	}
	return decodeErr
}

type skuResponse struct {
	ParentProduct *struct {
		UUID string `json:"uuid"`
	} `json:"parentProduct"`
}

// resolveUUID maps a number to its product UUID via the SKU API, trying
// the dashed, dash-less and GOOE-prefixed SKU spellings in turn.
func (p *Prestige) resolveUUID(number string) (string, error) {
	number = strings.ToUpper(strings.TrimSpace(number))
	noDash := strings.ReplaceAll(number, "-", "")
	candidates := []string{number, noDash, "GOOE" + number, "GOOE" + noDash}

	var lastErr error
	anyOK := false
	for _, skuID := range candidates {
		var data skuResponse
		if err := p.fetchJSON(p.skuURL(skuID), &data); err != nil {
			lastErr = err
			continue
		}
		anyOK = true
		if data.ParentProduct != nil && data.ParentProduct.UUID != "" {
			return data.ParentProduct.UUID, nil
		}
	}
	if !anyOK && lastErr != nil {
		return "", lastErr
	}
	return "", provider.ErrInfoNotFound
}

type namedItem struct {
	Name string `json:"name"`
}

type imageItem struct {
	Path string `json:"path"`
}

type productResponse struct {
	UUID         string          `json:"uuid"`
	Title        string          `json:"title"`
	Body         string          `json:"body"`
	MgsStartAt   string          `json:"mgsStartAt"`
	CreatedAt    string          `json:"createdAt"`
	PlayTime     int             `json:"playTime"`
	SKU          []skuItem       `json:"sku"`
	Actress      []namedItem     `json:"actress"`
	Directors    []namedItem     `json:"directors"`
	Genre        []namedItem     `json:"genre"`
	Maker        json.RawMessage `json:"maker"`
	Label        json.RawMessage `json:"label"`
	Series       json.RawMessage `json:"series"`
	Thumbnail    *imageItem      `json:"thumbnail"`
	PackageImage *imageItem      `json:"packageImage"`
	Media        []imageItem     `json:"media"`
}

type skuItem struct {
	DeliveryItemID string `json:"deliveryItemId"`
}

// extractName reads a maker/label/series value that may be a dict, a list
// whose first item is a dict, or a plain string.
func extractName(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var asString string
	if err := json.Unmarshal(raw, &asString); err == nil {
		return asString
	}
	var asDict namedItem
	if err := json.Unmarshal(raw, &asDict); err == nil && asDict.Name != "" {
		return asDict.Name
	}
	var asList []namedItem
	if err := json.Unmarshal(raw, &asList); err == nil && len(asList) > 0 {
		return asList[0].Name
	}
	return ""
}

func (p *Prestige) fetchProduct(uuid string) (*model.MovieInfo, error) {
	var data productResponse
	if err := p.fetchJSON(p.productURL(uuid), &data); err != nil {
		return nil, err
	}

	// The number is the regular-edition SKU; GOOE/GOO bonus SKUs are not
	// numbers.
	number := ""
	for _, sku := range data.SKU {
		sid := sku.DeliveryItemID
		if !strings.HasPrefix(sid, "GOOE") && !strings.HasPrefix(sid, "GOO") {
			number = sid
			break
		}
	}
	if number == "" && len(data.SKU) > 0 {
		number = data.SKU[0].DeliveryItemID
	}
	if number == "" {
		return nil, provider.ErrInfoNotFound
	}

	release := data.MgsStartAt
	if release == "" {
		release = data.CreatedAt
	}
	if i := strings.Index(release, "T"); i >= 0 {
		release = release[:i]
	}

	info := &model.MovieInfo{
		ID:            data.UUID,
		Number:        number,
		Title:         data.Title,
		Summary:       data.Body,
		Provider:      p.Name(),
		Homepage:      p.productURL(data.UUID),
		Actors:        []string{},
		PreviewImages: []string{},
		Genres:        []string{},
		Runtime:       data.PlayTime,
		ReleaseDate:   parser.ParseDate(release),
	}
	for _, a := range data.Actress {
		if a.Name != "" {
			info.Actors = append(info.Actors, a.Name)
		}
	}
	if len(data.Directors) > 0 {
		info.Director = data.Directors[0].Name
	}
	for _, g := range data.Genre {
		if g.Name != "" {
			info.Genres = append(info.Genres, g.Name)
		}
	}
	if maker := extractName(data.Maker); maker != "" {
		info.Maker = maker
	} else {
		info.Maker = "プレステージ"
	}
	info.Label = extractName(data.Label)
	info.Series = extractName(data.Series)

	// thumbnail is the portrait front cover, packageImage the landscape
	// spread.
	if data.Thumbnail != nil {
		cover := p.mediaURL(data.Thumbnail.Path)
		info.CoverURL = cover
		info.ThumbURL = cover
	}
	if data.PackageImage != nil {
		info.BigCoverURL = p.mediaURL(data.PackageImage.Path)
	}
	for _, img := range data.Media {
		if u := p.mediaURL(img.Path); u != "" {
			info.PreviewImages = append(info.PreviewImages, u)
		}
	}
	return info, nil
}

func (p *Prestige) GetMovieInfoByID(id string) (*model.MovieInfo, error) {
	if !uuidRe.MatchString(id) {
		uuid, err := p.resolveUUID(id)
		if err != nil {
			return nil, provider.ErrInfoNotFound
		}
		id = uuid
	}
	info, err := p.fetchProduct(id)
	if err != nil {
		return nil, provider.ErrInfoNotFound
	}
	if !info.IsValid() {
		return nil, provider.ErrInfoNotFound
	}
	return info, nil
}

func (p *Prestige) GetMovieInfoByURL(rawURL string) (*model.MovieInfo, error) {
	id, err := p.ParseMovieIDFromURL(rawURL)
	if err != nil || id == "" {
		return nil, provider.ErrInvalidURL
	}
	return p.GetMovieInfoByID(id)
}

func (p *Prestige) NormalizeMovieKeyword(keyword string) string {
	return strings.ToUpper(strings.TrimSpace(keyword))
}

func (p *Prestige) SearchMovie(keyword string) (results []*model.MovieSearchResult, err error) {
	// The site's /api/search only indexes on-sale items with fuzzy
	// matching, so search resolves the SKU directly and returns the
	// product as the single result.
	uuid, err := p.resolveUUID(keyword)
	if err != nil {
		return nil, err
	}
	info, err := p.fetchProduct(uuid)
	if err != nil {
		return nil, err
	}
	if !info.IsValid() {
		return nil, provider.ErrInfoNotFound
	}
	return []*model.MovieSearchResult{info.ToSearchResult()}, nil
}

func init() {
	provider.Register(Name, New)
}
