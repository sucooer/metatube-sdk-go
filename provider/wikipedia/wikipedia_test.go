package wikipedia

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/metatube-community/metatube-sdk-go/provider/internal/testkit"
)

func TestWikipedia_GetActorInfoByID(t *testing.T) {
	testkit.Test(t, New, []string{
		"Q22128111", // 三上悠亜
	})
}

func TestWikipedia_GetActorInfoByURL(t *testing.T) {
	testkit.Test(t, New, []string{
		"https://www.wikidata.org/wiki/Q22128111",
	})
}

func TestWikipedia_SearchActor(t *testing.T) {
	w := New()

	// Positive: a well-known AV performer with a Wikidata entity.
	results, err := w.SearchActor("三上悠亜")
	require.NoError(t, err)
	require.NotEmpty(t, results)
	require.Equal(t, "Q22128111", results[0].ID)
	require.True(t, results[0].IsValid())
	t.Logf("%+v", results)

	// Negative: a historical figure must not match the AV filters.
	results, err = w.SearchActor("織田信長")
	require.NoError(t, err)
	require.Empty(t, results)
}

func TestWikipedia_InfoboxFields(t *testing.T) {
	// 三上悠亜's Japanese article carries height and measurements in
	// its infobox; the provider must surface them as structured fields.
	w := New()
	info, err := w.GetActorInfoByID("Q22128111")
	require.NoError(t, err)
	require.Equal(t, "三上悠亜", info.Name)
	require.NotZero(t, info.Height)
	require.NotEmpty(t, info.Measurements)
	t.Logf("height=%d measurements=%s cup=%s birthday=%v", info.Height, info.Measurements, info.CupSize, info.Birthday)
}
