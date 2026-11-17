package javlibrary

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// Fixtures replicate the documented page structure of javlibrary.com (see
// the selectors in javlibrary.go). They prove the parser handles the
// layout; the live site is Cloudflare-challenged from available egresses,
// so live tests below skip while blocked.

// detailFixture is a minimal detail page for SSIS-354.
const detailFixture = `<html><head><link rel="canonical" href="https://www.javlibrary.com/cn/?v=javlib999999"></head><body>
<h3 class="post-title"><a>SSIS-354 單相思的架乃爛醉後成為接吻魔</a></h3>
<div id="video_id"><table><tr><td class="header">品番:</td><td class="text">SSIS-354</td></tr></table></div>
<div id="video_date"><table><tr><td class="header">發行日期:</td><td class="text">2021-06-19</td></tr></table></div>
<div id="video_length"><table><tr><td class="header">長度:</td><td><span class="text">150 分鐘</span></td></tr></table></div>
<div id="video_director"><table><tr><td class="header">導演:</td><td class="text"><a>紋℃</a></td></tr></table></div>
<div id="video_maker"><table><tr><td class="header">製作商:</td><td class="text"><a>S1</a></td></tr></table></div>
<div id="video_label"><table><tr><td class="header">發行商:</td><td class="text"><a>S1 NO.1 STYLE</a></td></tr></table></div>
<div id="video_cast"><table><tr><td class="header">演員:</td><td><span class="cast"><span class="star"><a>架乃由羅</a></span></span></td></tr></table></div>
<div id="video_genres"><table><tr><td class="header">類別:</td><td class="text"><span class="genre"><a>接吻</a></span><span class="genre"><a>爛醉</a></span></td></tr></table></div>
<img id="video_jacket_img" src="https://pics.javlibrary.com/xx/ssis354.jpg">
<span class="score">(8.32)</span>
</body></html>`

// searchFixture is a search result list with two videos.
const searchFixture = `<html><body>
<div class="video"><a href="./?v=javlib999999" title="SSIS-354 單相思的架乃爛醉後成為接吻魔"><img src="https://pics.javlibrary.com/xx/ssis354t.jpg"></a></div>
<div class="video"><a href="./?v=javlib999998" title="SSIS-355 其他標題"><img src="https://pics.javlibrary.com/xx/ssis355t.jpg"></a></div>
</body></html>`

func fixtureServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "vl_searchbyid.php"):
			fmt.Fprint(w, searchFixture)
		case r.URL.Query().Get("v") != "":
			fmt.Fprint(w, detailFixture)
		default:
			http.NotFound(w, r)
		}
	}))
}

func TestJavLibrary_Fixture_GetMovieInfoByID(t *testing.T) {
	srv := fixtureServer(t)
	defer srv.Close()

	lib := New()
	lib.endpoint = srv.URL

	info, err := lib.GetMovieInfoByID("javlib999999")
	require.NoError(t, err)
	require.Equal(t, "SSIS-354", info.Number)
	require.Contains(t, info.Title, "單相思")
	require.Equal(t, "S1", info.Maker)
	require.Equal(t, "S1 NO.1 STYLE", info.Label)
	require.Equal(t, "紋℃", info.Director)
	require.Equal(t, []string{"架乃由羅"}, []string(info.Actors))
	require.Equal(t, []string{"接吻", "爛醉"}, []string(info.Genres))
	require.Equal(t, 150, info.Runtime)
	require.InDelta(t, 8.32, info.Score, 0.001)
	require.NotEmpty(t, info.CoverURL)
	require.True(t, info.IsValid())
}

func TestJavLibrary_Fixture_SearchMovie(t *testing.T) {
	srv := fixtureServer(t)
	defer srv.Close()

	lib := New()
	lib.endpoint = srv.URL

	results, err := lib.SearchMovie("SSIS-354")
	require.NoError(t, err)
	require.Len(t, results, 1)
	require.Equal(t, "javlib999999", results[0].ID)
	require.Equal(t, "SSIS-354", results[0].Number)
	require.True(t, results[0].IsValid())
}

func TestJavLibrary_SearchMovie_Live(t *testing.T) {
	// The live site is Cloudflare-challenged from available egresses.
	// Skip while blocked; if the challenge ever lifts, this asserts
	// real results.
	lib := New()
	results, err := lib.SearchMovie("SSIS-354")
	if err != nil && isBlockedError(err) {
		t.Skipf("javlibrary blocked: %v", err)
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
