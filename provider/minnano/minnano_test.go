package minnano

import (
	_ "embed"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Fixtures are real detail pages saved from minnano-av.com (2026-11-17).
//
//go:embed testdata/actress1.html
var actress1Fixture string

//go:embed testdata/actress822325.html
var actress822325Fixture string

// searchFixture is a minimal search result list in the site's layout.
const searchFixture = `<html><body><table class="tbllist actress">
<tr><td><h2 class="ttl"><a href="actress1.html">森末くるみ （もりすえくるみ / Morisue Kurumi）</a></h2></td></tr>
<tr><td><h2 class="ttl"><a href="actress822325.html">渡会日菜 （わたらいひな / Watarai Hina）</a></h2></td></tr>
</table></body></html>`

func fixtureServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "search_result.php"):
			fmt.Fprint(w, searchFixture)
		case strings.HasSuffix(r.URL.Path, "actress1.html"):
			fmt.Fprint(w, actress1Fixture)
		case strings.HasSuffix(r.URL.Path, "actress822325.html"):
			fmt.Fprint(w, actress822325Fixture)
		default:
			http.NotFound(w, r)
		}
	}))
}

func TestMinnano_Fixture_GetActorInfoByID(t *testing.T) {
	srv := fixtureServer(t)
	defer srv.Close()

	m := New()
	m.endpoint = srv.URL

	info, err := m.GetActorInfoByID("1")
	require.NoError(t, err)
	require.Equal(t, "森末くるみ", info.Name)
	require.Contains(t, []string(info.Aliases), "もりすえくるみ")
	require.Contains(t, []string(info.Aliases), "Morisue Kurumi")
	require.Len(t, info.Images, 1)
	require.NotContains(t, info.Images[0], "?new") // query stripped
	require.True(t, info.IsValid())

	// Sparse profile: no measurements, but the cup-only size row parses.
	info, err = m.GetActorInfoByID("822325")
	require.NoError(t, err)
	require.Equal(t, "渡会日菜", info.Name)
	require.Equal(t, "F", info.CupSize)
	require.Equal(t, 0, info.Height)
	require.Equal(t, "", info.Measurements)
	require.True(t, info.IsValid())
}

func TestMinnano_Fixture_SearchActor(t *testing.T) {
	srv := fixtureServer(t)
	defer srv.Close()

	m := New()
	m.endpoint = srv.URL

	results, err := m.SearchActor("森末くるみ")
	require.NoError(t, err)
	require.Len(t, results, 1)
	require.Equal(t, "1", results[0].ID)
	require.Equal(t, "森末くるみ", results[0].Name)
	require.True(t, results[0].IsValid())

	// No exact match: nothing is returned.
	results, err = m.SearchActor("不存在的名字")
	require.NoError(t, err)
	require.Empty(t, results)
}

func TestMinnano_ParseFigure(t *testing.T) {
	h, b, w, hp, cup := parseFigure("T160 B85(Fカップ) W60 H80")
	require.Equal(t, 160, h)
	require.Equal(t, 85, b)
	require.Equal(t, 60, w)
	require.Equal(t, 80, hp)
	require.Equal(t, "F", cup)

	// Empty slots stay zero and steal nothing.
	h, b, w, hp, cup = parseFigure("T / B / W / H / S")
	require.Equal(t, 0, h)
	require.Equal(t, 0, b)
	require.Equal(t, 0, w)
	require.Equal(t, 0, hp)
	require.Equal(t, "", cup)

	// Cup without numbers.
	_, _, _, _, cup = parseFigure("T / B(|Fカップ|) / W / H / S")
	require.Equal(t, "F", cup)
}

func TestMinnano_ParseBirthday(t *testing.T) {
	require.Equal(t, "1995-08-30", time.Time(parseBirthday("平成7年8月30日")).Format("2006-01-02"))
	require.Equal(t, "2019-05-01", time.Time(parseBirthday("令和元年5月1日")).Format("2006-01-02"))
	require.Equal(t, "1995-08-30", time.Time(parseBirthday("1995年8月30日")).Format("2006-01-02"))
}

func TestMinnano_ParseNameLine(t *testing.T) {
	name, readings := parseNameLine("森末くるみ （もりすえくるみ / Morisue Kurumi）", nil)
	require.Equal(t, "森末くるみ", name)
	require.Equal(t, []string{"もりすえくるみ", "Morisue Kurumi"}, readings)

	// A site name in the reading position is dropped when it matches a
	// page tag.
	name, readings = parseNameLine("相川美羽(tenshigao)", map[string]bool{"tenshigao": true})
	require.Equal(t, "相川美羽", name)
	require.Empty(t, readings)

	// Only the rightmost group is the reading; the rest are annotations.
	name, readings = parseNameLine("名前 (注記) （よみ / Yomi）", nil)
	require.Equal(t, "名前", name)
	require.Equal(t, []string{"よみ", "Yomi"}, readings)
}

func TestMinnano_Live(t *testing.T) {
	// Detail pages are reachable from some egresses, the search endpoint
	// is Cloudflare-challenged from all available ones. Skip while
	// blocked.
	m := New()
	info, err := m.GetActorInfoByID("1")
	if err != nil && isBlockedError(err) {
		t.Skipf("minnano blocked: %v", err)
	}
	require.NoError(t, err)
	require.NotEmpty(t, info.Name)

	results, err := m.SearchActor("森末くるみ")
	if err != nil && isBlockedError(err) {
		t.Skipf("minnano search blocked: %v", err)
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
