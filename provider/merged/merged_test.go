package merged

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/metatube-community/metatube-sdk-go/model"
	"github.com/metatube-community/metatube-sdk-go/provider"

	// Sub-providers registered for the merge tests. Only a small,
	// reliable subset is imported so the live tests stay fast; the
	// aggregator itself enumerates whatever is registered.
	_ "github.com/metatube-community/metatube-sdk-go/provider/duga"
	_ "github.com/metatube-community/metatube-sdk-go/provider/jav321"
)

func TestMerged_SearchMovie(t *testing.T) {
	m := New()
	require.NotEmpty(t, m.providers)

	results, err := m.SearchMovie("SSIS-354")
	require.NoError(t, err)
	require.Len(t, results, 1)
	result := results[0]
	require.Equal(t, "Merged", result.Provider)
	require.Equal(t, "SSIS-354", result.ID)
	require.Equal(t, "SSIS-354", result.Number)
	require.True(t, result.IsValid())
	t.Logf("%+v", result)
}

func TestMerged_GetMovieInfoByID(t *testing.T) {
	m := New()

	info, err := m.GetMovieInfoByID("SSIS-354")
	require.NoError(t, err)
	require.True(t, info.IsValid())
	require.Equal(t, "Merged", info.Provider)
	require.Equal(t, "SSIS-354", info.Number)
	require.NotEmpty(t, info.Title)
	require.NotEmpty(t, info.CoverURL)
	require.NotEmpty(t, info.Actors)
	t.Logf("title=%s actors=%v genres=%v previews=%d maker=%s", info.Title, info.Actors, info.Genres, len(info.PreviewImages), info.Maker)
}

func TestMerged_GetMovieInfoByID_NotFound(t *testing.T) {
	m := New()
	_, err := m.GetMovieInfoByID("ZZZZ-999999")
	require.ErrorIs(t, err, provider.ErrInfoNotFound)
}

func TestMerged_MergeMovieInfos(t *testing.T) {
	merged := mergeMovieInfos("SSIS-354", []*model.MovieInfo{
		{
			Title:         "First Title",
			CoverURL:      "https://example.com/cover.jpg",
			Homepage:      "https://example.com/1",
			Actors:        []string{"Actor A"},
			Genres:        []string{"Genre A"},
			PreviewImages: []string{"https://example.com/p1.jpg"},
			Maker:         "Maker A",
		},
		{
			Title:         "Second Title",
			Summary:       "Second summary",
			CoverURL:      "https://example.com/cover2.jpg",
			Homepage:      "https://example.com/2",
			Actors:        []string{"Actor A", "Actor B"},
			Genres:        []string{"Genre B"},
			PreviewImages: []string{"https://example.com/p1.jpg", "https://example.com/p2.jpg"},
			Label:         "Label B",
		},
	})
	require.Equal(t, "First Title", merged.Title) // first-non-empty wins.
	require.Equal(t, "Second summary", merged.Summary)
	require.Equal(t, "https://example.com/cover.jpg", merged.CoverURL)
	require.Equal(t, "Maker A", merged.Maker)
	require.Equal(t, "Label B", merged.Label)
	require.Equal(t, []string{"Actor A", "Actor B"}, []string(merged.Actors)) // union, deduped.
	require.Equal(t, []string{"Genre A", "Genre B"}, []string(merged.Genres))
	require.Len(t, merged.PreviewImages, 2)
}

func TestMerged_SameNumber(t *testing.T) {
	require.True(t, sameNumber("SSIS-354", "ssis354"))
	require.True(t, sameNumber("ABP-030", "ABP-30"))
	require.True(t, sameNumber("ABP-0030", "abp30"))
	require.False(t, sameNumber("SSIS-354", "SSIS-35"))
	require.False(t, sameNumber("SSIS-354", "SSIS-355"))
}
