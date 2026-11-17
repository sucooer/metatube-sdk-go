// Actor aggregator side of the merged package: a virtual actor
// provider that fans out to every other registered actor provider and
// merges their records field by field. It mirrors the movie Merged
// provider in merged.go; see the package comment there for the shared
// design notes.
//
// Actor-specific notes:
//
//   - The name is the same "Merged" as the movie aggregator, but the
//     two live in separate factory maps (movieFactories vs
//     actorFactories), exactly like the avbase/javdb packages that
//     register a movie and an actor type under one name. No register.go
//     change is needed: the merged package is already imported there.
//   - Priority is 1005, above Minnano (1004), AVBASE (1003) and
//     Wikipedia (1002), so the merged candidate ranks first in
//     SearchActorAll and the plugin (which takes the first result)
//     binds it.
//   - Gfriends is skipped in the fan-out: it carries no profile fields
//     (photos only) and the engine already injects its images into
//     every Japanese provider's result, so including it would only
//     duplicate images.
//   - Safety: a sub-provider contributes only when its top search
//     result name EXACTLY equals the queried name (NFKC, trimmed,
//     case-insensitive), and the fetched record's name must match too.
//     A wrong-person match must never poison the merge.
//   - Merging is fixed priority-order, first-non-empty, for scalar
//     fields (so Minnano wins when present, AVBASE in practice since
//     Minnano search is Cloudflare-blocked); aliases and images are the
//     priority-ordered union. Per-field configuration is out of scope.
//   - Each sub-provider fails closed independently: an error, a
//     timeout, or a name mismatch simply drops that provider from the
//     merge. If no provider yields a usable record, GetActorInfoByID
//     returns ErrInfoNotFound.
//   - Sub-provider calls bypass the engine's DB cache (the same
//     tradeoff the movie aggregator makes). The merged record itself IS
//     cached by the engine under the Merged provider name.
//   - Provider configs are NOT forwarded: a sub-provider that needs
//     configuration (e.g. ThePornDB's access token) only receives it
//     on the engine's standalone instance, so such providers
//     contribute nothing to merges until they work unconfigured.
package merged

import (
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/text/language"
	"golang.org/x/text/unicode/norm"

	"github.com/metatube-community/metatube-sdk-go/common/fetch"
	"github.com/metatube-community/metatube-sdk-go/model"
	"github.com/metatube-community/metatube-sdk-go/provider"
	"github.com/metatube-community/metatube-sdk-go/provider/gfriends"
	"github.com/metatube-community/metatube-sdk-go/provider/internal/scraper"
)

var (
	_ provider.ActorProvider        = (*MergedActor)(nil)
	_ provider.ActorSearcher        = (*MergedActor)(nil)
	_ provider.Fetcher              = (*MergedActor)(nil)
	_ provider.RequestTimeoutSetter = (*MergedActor)(nil)
	_ provider.ProxySetter          = (*MergedActor)(nil)
)

const (
	// ActorPriority outranks every actor provider (Minnano 1004,
	// AVBASE 1003, Wikipedia 1002) so the merged candidate is
	// always the first search result.
	ActorPriority = 1000 + 5
)

type MergedActor struct {
	*fetch.Fetcher
	*scraper.Scraper
	providers []provider.ActorProvider
}

func NewActor() *MergedActor {
	m := &MergedActor{
		Fetcher: fetch.Default(&fetch.Config{}),
		Scraper: scraper.NewDefaultScraper(
			Name, baseURL, ActorPriority, language.Japanese),
	}
	// Enumerate every other registered actor provider. Factories are
	// all registered by the time the engine (or a test) constructs
	// this provider. Providers with a non-positive priority are
	// skipped, mirroring the engine's disable rule.
	for name, factory := range provider.RangeActorFactory {
		if strings.EqualFold(name, Name) {
			continue // never fan out to ourselves.
		}
		if strings.EqualFold(name, gfriends.Name) {
			continue // photos only; the engine injects its images.
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
func (m *MergedActor) SetRequestTimeout(timeout time.Duration) {
	m.Scraper.SetRequestTimeout(timeout)
	for _, p := range m.providers {
		if s, ok := p.(provider.RequestTimeoutSetter); ok {
			s.SetRequestTimeout(timeout)
		}
	}
}

// SetProxy applies the proxy to the aggregator itself and forwards it
// to every sub-provider. The first error aborts the loop.
func (m *MergedActor) SetProxy(proxyURL string) error {
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
func (m *MergedActor) Fetch(rawURL string) (*http.Response, error) {
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

func (m *MergedActor) NormalizeActorID(id string) string {
	return strings.TrimSpace(id)
}

// ParseActorIDFromURL has no URL form to parse: the aggregator owns no
// site. Returning an empty ID makes the engine reject URL lookups.
func (m *MergedActor) ParseActorIDFromURL(_ string) (string, error) {
	return "", nil
}

func (m *MergedActor) GetActorInfoByURL(_ string) (*model.ActorInfo, error) {
	return nil, provider.ErrInvalidURL
}

func (m *MergedActor) SearchActor(keyword string) (results []*model.ActorSearchResult, err error) {
	keyword = strings.TrimSpace(keyword)
	if keyword == "" {
		return nil, provider.ErrInvalidKeyword
	}
	best := m.bestSearchResult(keyword)
	if best == nil {
		return nil, nil
	}
	return []*model.ActorSearchResult{
		{
			ID:       keyword,
			Name:     keyword,
			Provider: m.Name(),
			Homepage: best.Homepage,
			Aliases:  best.Aliases,
			Images:   best.Images,
		},
	}, nil
}

// bestSearchResult fans out to sub-provider searches and returns the
// highest-priority exact-name match, or nil.
func (m *MergedActor) bestSearchResult(keyword string) *model.ActorSearchResult {
	type response struct {
		index  int
		result *model.ActorSearchResult
	}
	respCh := make(chan response, len(m.providers))
	var wg sync.WaitGroup
	for i, p := range m.providers {
		wg.Add(1)
		go func(i int, p provider.ActorProvider) {
			defer wg.Done()
			if result := searchActorProvider(p, keyword); result != nil {
				respCh <- response{index: i, result: result}
			}
		}(i, p)
	}
	wg.Wait()
	close(respCh)

	ordered := make([]*model.ActorSearchResult, len(m.providers))
	for resp := range respCh {
		ordered[resp.index] = resp.result
	}
	for _, result := range ordered {
		if result != nil {
			return result
		}
	}
	return nil
}

// searchActorProvider returns the provider's first exact-name search
// result, or nil. Providers without a searcher fall back to a direct
// info lookup, mirroring the engine's search fallback.
func searchActorProvider(p provider.ActorProvider, keyword string) *model.ActorSearchResult {
	if searcher, ok := p.(provider.ActorSearcher); ok {
		results, err := searcher.SearchActor(keyword)
		if err != nil {
			return nil
		}
		for _, result := range results {
			if result != nil && sameActorName(keyword, result.Name) {
				return result
			}
		}
		return nil
	}
	info, err := p.GetActorInfoByID(keyword)
	if err != nil || info == nil || !info.IsValid() || !sameActorName(keyword, info.Name) {
		return nil
	}
	return info.ToSearchResult()
}

func (m *MergedActor) GetActorInfoByID(id string) (*model.ActorInfo, error) {
	id = m.NormalizeActorID(id)
	if id == "" {
		return nil, provider.ErrInvalidID
	}
	infos := m.fetchAll(id)
	if len(infos) == 0 {
		return nil, provider.ErrInfoNotFound
	}
	merged := mergeActorInfos(id, infos)
	merged.ID = id
	merged.Name = id
	merged.Provider = m.Name()
	if !merged.IsValid() {
		return nil, provider.ErrInfoNotFound
	}
	return merged, nil
}

// fetchAll queries every sub-provider concurrently and returns the
// usable records in priority order.
func (m *MergedActor) fetchAll(id string) []*model.ActorInfo {
	type response struct {
		index int
		info  *model.ActorInfo
	}
	respCh := make(chan response, len(m.providers))
	var wg sync.WaitGroup
	for i, p := range m.providers {
		wg.Add(1)
		go func(i int, p provider.ActorProvider) {
			defer wg.Done()
			if info := fetchActorProviderInfo(p, id); info != nil {
				respCh <- response{index: i, info: info}
			}
		}(i, p)
	}
	wg.Wait()
	close(respCh)

	ordered := make([]*model.ActorInfo, len(m.providers))
	for resp := range respCh {
		ordered[resp.index] = resp.info
	}
	var infos []*model.ActorInfo
	for _, info := range ordered {
		if info != nil {
			infos = append(infos, info)
		}
	}
	return infos
}

// fetchActorProviderInfo resolves the name to the provider's own
// record: search first (for searchers), then info by the matched
// result's ID; direct info lookup for providers without a searcher.
// An exact-name mismatch at either step discards the record: merging
// fields across different people would be worse than missing fields.
func fetchActorProviderInfo(p provider.ActorProvider, id string) *model.ActorInfo {
	if searcher, ok := p.(provider.ActorSearcher); ok {
		results, err := searcher.SearchActor(id)
		if err != nil {
			return nil
		}
		var best *model.ActorSearchResult
		for _, result := range results {
			if result != nil && sameActorName(id, result.Name) {
				best = result
				break
			}
		}
		if best == nil {
			return nil
		}
		info, err := p.GetActorInfoByID(best.ID)
		if err != nil || info == nil || !info.IsValid() || !sameActorName(id, info.Name) {
			return nil
		}
		return info
	}
	info, err := p.GetActorInfoByID(id)
	if err != nil || info == nil || !info.IsValid() || !sameActorName(id, info.Name) {
		return nil
	}
	return info
}

// mergeActorInfos merges priority-ordered records: first-non-empty for
// scalar fields, priority-ordered union for list fields.
func mergeActorInfos(id string, infos []*model.ActorInfo) *model.ActorInfo {
	merged := &model.ActorInfo{
		Aliases: []string{},
		Images:  []string{},
	}
	seenAliases := map[string]bool{}
	seenImages := map[string]bool{}
	for _, info := range infos {
		if merged.Homepage == "" {
			merged.Homepage = info.Homepage
		}
		if merged.Summary == "" {
			merged.Summary = info.Summary
		}
		if merged.Hobby == "" {
			merged.Hobby = info.Hobby
		}
		if merged.Skill == "" {
			merged.Skill = info.Skill
		}
		if merged.BloodType == "" {
			merged.BloodType = info.BloodType
		}
		if merged.CupSize == "" {
			merged.CupSize = info.CupSize
		}
		if merged.Measurements == "" {
			merged.Measurements = info.Measurements
		}
		if merged.Nationality == "" {
			merged.Nationality = info.Nationality
		}
		if merged.Height == 0 {
			merged.Height = info.Height
		}
		if time.Time(merged.Birthday).IsZero() {
			merged.Birthday = info.Birthday
		}
		if time.Time(merged.DebutDate).IsZero() {
			merged.DebutDate = info.DebutDate
		}
		for _, alias := range info.Aliases {
			if alias != "" && !sameActorName(alias, id) && !seenAliases[alias] {
				seenAliases[alias] = true
				merged.Aliases = append(merged.Aliases, alias)
			}
		}
		for _, image := range info.Images {
			if image != "" && !seenImages[image] {
				seenImages[image] = true
				merged.Images = append(merged.Images, image)
			}
		}
	}
	return merged
}

// sameActorName compares two actor names ignoring case, surrounding
// whitespace, and full/half-width variants (NFKC), so a provider's
// differently-cased or width-variant label still counts as the same
// person, while a different person never does.
func sameActorName(a, b string) bool {
	fold := func(s string) string {
		return strings.TrimSpace(norm.NFKC.String(s))
	}
	return a != "" && strings.EqualFold(fold(a), fold(b))
}

func init() {
	provider.Register(Name, NewActor)
}
