// Package merged implements the Merged aggregator provider: a virtual
// movie provider that fans out to every other registered movie provider
// and merges their results field by field.
//
// Design notes (see also the engine package):
//
//   - The plugin is unchanged and takes the first search result, so the
//     aggregator carries the top priority (1002, above FANZA's 1001) and
//     its search result rebrands the best sub-provider match under the
//     Merged name. The merge itself happens in GetMovieInfoByID.
//   - Merging is fixed priority-order, first-non-empty, for scalar
//     fields; list fields (actors, genres, preview images) are the
//     priority-ordered union of all providers. Per-field configuration
//     is deliberately out of scope.
//   - Each sub-provider fails closed independently: an error, a timeout,
//     or a number mismatch simply drops that provider from the merge. If
//     no provider yields a usable record, GetMovieInfoByID returns
//     ErrInfoNotFound.
//   - Sub-provider calls bypass the engine's DB cache (the same tradeoff
//     the AVBASE provider makes by instantiating other providers
//     directly). The merged record itself IS cached by the engine under
//     the Merged provider name, so repeat fetches are free.
//   - The aggregator forwards the engine's request timeout and proxy to
//     its sub-providers, but it does NOT forward provider configs: a
//     sub-provider that needs configuration (e.g. ThePornDB's access
//     token) only receives it on the engine's standalone instance, so
//     such providers contribute nothing to merges until they work
//     without configuration.
//   - SearchMovie fans out to sub-provider searches, so a global search
//     queries each site twice (once directly, once through the
//     aggregator). This is the price of only ever surfacing a Merged
//     result when a real record exists behind it.
package merged

import (
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/text/language"

	"github.com/metatube-community/metatube-sdk-go/common/comparer"
	"github.com/metatube-community/metatube-sdk-go/common/fetch"
	"github.com/metatube-community/metatube-sdk-go/model"
	"github.com/metatube-community/metatube-sdk-go/provider"
	"github.com/metatube-community/metatube-sdk-go/provider/internal/scraper"
)

var (
	_ provider.MovieProvider        = (*Merged)(nil)
	_ provider.MovieSearcher        = (*Merged)(nil)
	_ provider.Fetcher              = (*Merged)(nil)
	_ provider.RequestTimeoutSetter = (*Merged)(nil)
	_ provider.ProxySetter          = (*Merged)(nil)
)

const (
	Name     = "Merged"
	Priority = 1000 + 2
	// baseURL is a placeholder: the aggregator has no site of its own.
	// The host never matches a real URL, so GetMovieProviderByURL can
	// never resolve a URL to this provider.
	baseURL = "https://merged.local/"
)

type Merged struct {
	*fetch.Fetcher
	*scraper.Scraper
	providers []provider.MovieProvider
}

func New() *Merged {
	m := &Merged{
		Fetcher: fetch.Default(&fetch.Config{}),
		Scraper: scraper.NewDefaultScraper(
			Name, baseURL, Priority, language.Japanese),
	}
	// Enumerate every other registered movie provider. Factories are
	// all registered by the time the engine (or a test) constructs this
	// provider. Providers with a non-positive priority are skipped,
	// mirroring the engine's disable rule.
	for name, factory := range provider.RangeMovieFactory {
		if strings.EqualFold(name, Name) {
			continue // never fan out to ourselves.
		}
		p := factory()
		if p.Priority() <= 0 {
			continue
		}
		m.providers = append(m.providers, p)
	}
	sort.SliceStable(m.providers, func(i, j int) bool {
		if m.providers[i].Priority() != m.providers[j].Priority() {
			return m.providers[i].Priority() > m.providers[j].Priority()
		}
		return m.providers[i].Name() < m.providers[j].Name()
	})
	return m
}

// SetRequestTimeout applies the timeout to the aggregator itself and
// forwards it to every sub-provider.
func (m *Merged) SetRequestTimeout(timeout time.Duration) {
	m.Scraper.SetRequestTimeout(timeout)
	for _, p := range m.providers {
		if s, ok := p.(provider.RequestTimeoutSetter); ok {
			s.SetRequestTimeout(timeout)
		}
	}
}

// SetProxy applies the proxy to the aggregator itself and forwards it
// to every sub-provider. The first error aborts the loop.
func (m *Merged) SetProxy(proxyURL string) error {
	if err := m.Scraper.SetProxy(proxyURL); err != nil {
		return err
	}
	for _, p := range m.providers {
		if s, ok := p.(provider.ProxySetter); ok {
			if err := s.SetProxy(proxyURL); err != nil {
				return err
			}
		}
	}
	return nil
}

// Fetch delegates to the sub-provider that owns the URL's host (so
// Referer-bearing fetchers keep working for hotlink-protected images),
// falling back to the default fetcher.
func (m *Merged) Fetch(rawURL string) (*http.Response, error) {
	if u, err := url.Parse(rawURL); err == nil {
		for _, p := range m.providers {
			if strings.EqualFold(p.URL().Hostname(), u.Hostname()) {
				if fetcher, ok := p.(provider.Fetcher); ok {
					return fetcher.Fetch(rawURL)
				}
			}
		}
	}
	return m.Fetcher.Fetch(rawURL)
}

func (m *Merged) NormalizeMovieID(id string) string {
	return strings.ToUpper(strings.TrimSpace(id))
}

func (m *Merged) NormalizeMovieKeyword(keyword string) string {
	return strings.ToUpper(strings.TrimSpace(keyword))
}

// ParseMovieIDFromURL has no URL form to parse: the aggregator owns no
// site. Returning an empty ID makes the engine reject URL lookups.
func (m *Merged) ParseMovieIDFromURL(_ string) (string, error) {
	return "", nil
}

func (m *Merged) GetMovieInfoByURL(_ string) (*model.MovieInfo, error) {
	return nil, provider.ErrInvalidURL
}

func (m *Merged) SearchMovie(keyword string) (results []*model.MovieSearchResult, err error) {
	keyword = m.NormalizeMovieKeyword(keyword)
	if keyword == "" {
		return nil, provider.ErrInvalidKeyword
	}
	best := m.bestSearchResult(keyword)
	if best == nil {
		return nil, nil
	}
	return []*model.MovieSearchResult{
		{
			ID:          keyword,
			Number:      keyword,
			Title:       best.Title,
			Provider:    m.Name(),
			Homepage:    best.Homepage,
			ThumbURL:    best.ThumbURL,
			CoverURL:    best.CoverURL,
			Score:       best.Score,
			Actors:      best.Actors,
			ReleaseDate: best.ReleaseDate,
		},
	}, nil
}

// bestSearchResult fans out to sub-provider searches and returns the
// highest-weighted result whose number matches the keyword.
func (m *Merged) bestSearchResult(keyword string) *model.MovieSearchResult {
	type response struct {
		result *model.MovieSearchResult
		weight float64
	}
	respCh := make(chan response, len(m.providers))
	var wg sync.WaitGroup
	for _, p := range m.providers {
		wg.Add(1)
		go func(p provider.MovieProvider) {
			defer wg.Done()
			result := searchProvider(p, keyword)
			if result == nil {
				return
			}
			respCh <- response{
				result: result,
				weight: comparer.Compare(keyword, result.Number) * p.Priority(),
			}
		}(p)
	}
	wg.Wait()
	close(respCh)

	var best *model.MovieSearchResult
	bestWeight := 0.0
	for resp := range respCh {
		if resp.weight > bestWeight {
			best, bestWeight = resp.result, resp.weight
		}
	}
	return best
}

// searchProvider returns the provider's best number-matching search
// result, or nil. Providers without a searcher fall back to a direct
// info lookup, mirroring the engine's searchMovie fallback.
func searchProvider(p provider.MovieProvider, keyword string) *model.MovieSearchResult {
	if searcher, ok := p.(provider.MovieSearcher); ok {
		normalized := searcher.NormalizeMovieKeyword(keyword)
		if normalized == "" {
			return nil
		}
		results, err := searcher.SearchMovie(normalized)
		if err != nil {
			return nil
		}
		var best *model.MovieSearchResult
		bestScore := 0.0
		for _, result := range results {
			if result == nil || !sameNumber(keyword, result.Number) {
				continue
			}
			if score := comparer.Compare(keyword, result.Number); score > bestScore {
				best, bestScore = result, score
			}
		}
		return best
	}
	info, err := p.GetMovieInfoByID(keyword)
	if err != nil || info == nil || !info.IsValid() || !sameNumber(keyword, info.Number) {
		return nil
	}
	return info.ToSearchResult()
}

func (m *Merged) GetMovieInfoByID(id string) (*model.MovieInfo, error) {
	id = m.NormalizeMovieID(id)
	if id == "" {
		return nil, provider.ErrInvalidID
	}
	infos := m.fetchAll(id)
	if len(infos) == 0 {
		return nil, provider.ErrInfoNotFound
	}
	merged := mergeMovieInfos(id, infos)
	merged.ID = id
	merged.Number = id
	merged.Provider = m.Name()
	if merged == nil || !merged.IsValid() {
		return nil, provider.ErrInfoNotFound
	}
	return merged, nil
}

// fetchAll queries every sub-provider concurrently and returns the
// usable records in priority order.
func (m *Merged) fetchAll(id string) []*model.MovieInfo {
	type response struct {
		index int
		info  *model.MovieInfo
	}
	respCh := make(chan response, len(m.providers))
	var wg sync.WaitGroup
	for i, p := range m.providers {
		wg.Add(1)
		go func(i int, p provider.MovieProvider) {
			defer wg.Done()
			info := fetchProviderInfo(p, id)
			if info != nil {
				respCh <- response{index: i, info: info}
			}
		}(i, p)
	}
	wg.Wait()
	close(respCh)

	ordered := make([]*model.MovieInfo, len(m.providers))
	for resp := range respCh {
		ordered[resp.index] = resp.info
	}
	var infos []*model.MovieInfo
	for _, info := range ordered {
		if info != nil {
			infos = append(infos, info)
		}
	}
	return infos
}

// fetchProviderInfo resolves the number to the provider's own record:
// search first (for searchers), then info by the matched result's ID;
// direct info lookup for providers without a searcher. A number
// mismatch discards the record: merging fields across different videos
// would be worse than missing fields.
func fetchProviderInfo(p provider.MovieProvider, id string) *model.MovieInfo {
	if searcher, ok := p.(provider.MovieSearcher); ok {
		normalized := searcher.NormalizeMovieKeyword(id)
		if normalized == "" {
			return nil
		}
		results, err := searcher.SearchMovie(normalized)
		if err != nil {
			return nil
		}
		var best *model.MovieSearchResult
		bestScore := 0.0
		for _, result := range results {
			if result == nil || !sameNumber(id, result.Number) {
				continue
			}
			if score := comparer.Compare(id, result.Number); score > bestScore {
				best, bestScore = result, score
			}
		}
		if best == nil {
			return nil
		}
		info, err := p.GetMovieInfoByID(best.ID)
		if err != nil || info == nil || !info.IsValid() || !sameNumber(id, info.Number) {
			return nil
		}
		return info
	}
	info, err := p.GetMovieInfoByID(id)
	if err != nil || info == nil || !info.IsValid() || !sameNumber(id, info.Number) {
		return nil
	}
	return info
}

// mergeMovieInfos merges priority-ordered records: first-non-empty for
// scalar fields, priority-ordered union for list fields.
func mergeMovieInfos(id string, infos []*model.MovieInfo) *model.MovieInfo {
	merged := &model.MovieInfo{
		Actors:        []string{},
		PreviewImages: []string{},
		Genres:        []string{},
	}
	seenActors := map[string]bool{}
	seenPreviews := map[string]bool{}
	seenGenres := map[string]bool{}
	for _, info := range infos {
		if merged.Title == "" {
			merged.Title = info.Title
		}
		if merged.Summary == "" {
			merged.Summary = info.Summary
		}
		if merged.Homepage == "" {
			merged.Homepage = info.Homepage
		}
		if merged.Director == "" {
			merged.Director = info.Director
		}
		if merged.ThumbURL == "" {
			merged.ThumbURL = info.ThumbURL
		}
		if merged.BigThumbURL == "" {
			merged.BigThumbURL = info.BigThumbURL
		}
		if merged.CoverURL == "" {
			merged.CoverURL = info.CoverURL
		}
		if merged.BigCoverURL == "" {
			merged.BigCoverURL = info.BigCoverURL
		}
		if merged.PreviewVideoURL == "" {
			merged.PreviewVideoURL = info.PreviewVideoURL
		}
		if merged.PreviewVideoHLSURL == "" {
			merged.PreviewVideoHLSURL = info.PreviewVideoHLSURL
		}
		if merged.Maker == "" {
			merged.Maker = info.Maker
		}
		if merged.Label == "" {
			merged.Label = info.Label
		}
		if merged.Series == "" {
			merged.Series = info.Series
		}
		if merged.Score == 0 {
			merged.Score = info.Score
		}
		if merged.Runtime == 0 {
			merged.Runtime = info.Runtime
		}
		if time.Time(merged.ReleaseDate).IsZero() {
			merged.ReleaseDate = info.ReleaseDate
		}
		for _, actor := range info.Actors {
			if actor != "" && !seenActors[actor] {
				seenActors[actor] = true
				merged.Actors = append(merged.Actors, actor)
			}
		}
		for _, image := range info.PreviewImages {
			if image != "" && !seenPreviews[image] {
				seenPreviews[image] = true
				merged.PreviewImages = append(merged.PreviewImages, image)
			}
		}
		for _, genre := range info.Genres {
			if genre != "" && !seenGenres[genre] {
				seenGenres[genre] = true
				merged.Genres = append(merged.Genres, genre)
			}
		}
	}
	return merged
}

// sameNumber compares two numbers ignoring case, dashes, underscores,
// spaces, and leading zeros in the trailing numeric part, so SSIS-354,
// ssis354 and SSIS-0354 all match.
func sameNumber(a, b string) bool {
	return normalizeNumber(a) == normalizeNumber(b)
}

func normalizeNumber(s string) string {
	var b strings.Builder
	for _, r := range strings.ToUpper(s) {
		if r == '-' || r == '_' || r == ' ' {
			continue
		}
		b.WriteRune(r)
	}
	s = b.String()
	// Strip leading zeros from the trailing digit run.
	i := len(s)
	for i > 0 && s[i-1] >= '0' && s[i-1] <= '9' {
		i--
	}
	head, tail := s[:i], strings.TrimLeft(s[i:], "0")
	return fmt.Sprintf("%s%s", head, tail)
}

func init() {
	provider.Register(Name, New)
}
