package prestige

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// Fixtures replicate the documented JSON API shapes of prestige-av.com
// (see prestige.go). They prove the parser handles the layout; the live
// site is restricted to Japanese IPs, so live tests below skip while
// blocked.

const skuFixture = `{"parentProduct": {"uuid": "11111111-2222-3333-4444-555555555555"}}`

const productFixture = `{
  "uuid": "11111111-2222-3333-4444-555555555555",
  "title": "ABW-350 テストタイトル",
  "body": "テストあらすじ",
  "mgsStartAt": "2024-01-15T00:00:00.000Z",
  "createdAt": "2024-01-10T00:00:00.000Z",
  "playTime": 120,
  "sku": [
    {"deliveryItemId": "GOOEABW-350"},
    {"deliveryItemId": "ABW-350"}
  ],
  "actress": [{"name": "テスト女優"}],
  "directors": [{"name": "テスト監督"}],
  "genre": [{"name": "巨乳"}, {"name": "単体作品"}],
  "maker": {"name": "プレステージ"},
  "label": [{"name": "ABW"}],
  "series": "テストシリーズ",
  "thumbnail": {"path": "goods/prestige/abw350/thumb.jpg"},
  "packageImage": {"path": "goods/prestige/abw350/package.jpg"},
  "media": [{"path": "goods/prestige/abw350/sample1.jpg"}]
}`

func fixtureServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasPrefix(r.URL.Path, "/api/sku/item/ABW-350"):
			fmt.Fprint(w, skuFixture)
		case strings.HasPrefix(r.URL.Path, "/api/product/"):
			fmt.Fprint(w, productFixture)
		default:
			http.NotFound(w, r)
		}
	}))
}

func TestPrestige_Fixture_GetMovieInfoByID(t *testing.T) {
	srv := fixtureServer(t)
	defer srv.Close()

	p := New()
	p.endpoint = srv.URL

	// By UUID.
	info, err := p.GetMovieInfoByID("11111111-2222-3333-4444-555555555555")
	require.NoError(t, err)
	require.Equal(t, "ABW-350", info.Number)
	require.Contains(t, info.Title, "ABW-350")
	require.Equal(t, "テストあらすじ", info.Summary)
	require.Equal(t, "プレステージ", info.Maker)
	require.Equal(t, "ABW", info.Label)
	require.Equal(t, "テストシリーズ", info.Series)
	require.Equal(t, "テスト監督", info.Director)
	require.Equal(t, []string{"テスト女優"}, []string(info.Actors))
	require.Equal(t, []string{"巨乳", "単体作品"}, []string(info.Genres))
	require.Equal(t, 120, info.Runtime)
	require.Contains(t, info.CoverURL, "thumb.jpg")
	require.Contains(t, info.BigCoverURL, "package.jpg")
	require.Len(t, info.PreviewImages, 1)
	require.True(t, info.IsValid())

	// By SKU (resolves via the SKU API).
	info, err = p.GetMovieInfoByID("ABW-350")
	require.NoError(t, err)
	require.Equal(t, "ABW-350", info.Number)
}

func TestPrestige_Fixture_SearchMovie(t *testing.T) {
	srv := fixtureServer(t)
	defer srv.Close()

	p := New()
	p.endpoint = srv.URL

	results, err := p.SearchMovie("ABW-350")
	require.NoError(t, err)
	require.Len(t, results, 1)
	require.Equal(t, "ABW-350", results[0].Number)
	require.True(t, results[0].IsValid())
}

func TestPrestige_SearchMovie_Live(t *testing.T) {
	// The live site is restricted to Japanese IPs (CloudFront 403).
	// Skip while blocked; if it ever becomes reachable, this asserts
	// real results.
	p := New()
	results, err := p.SearchMovie("ABW-350")
	if err != nil && isBlockedError(err) {
		t.Skipf("prestige blocked: %v", err)
	}
	require.NoError(t, err)
	require.NotEmpty(t, results)
}

func isBlockedError(err error) bool {
	msg := err.Error()
	for _, s := range []string{"Forbidden", "Too Many Requests", "Bad Gateway", "Service Unavailable", "Gateway Timeout", "403", "429", "502", "503", "504"} {
		if strings.Contains(msg, s) {
			return true
		}
	}
	return false
}
