package merged

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/metatube-community/metatube-sdk-go/model"
	"github.com/metatube-community/metatube-sdk-go/provider"

	// Sub-providers registered for the merge tests. Only a small,
	// reliable subset is imported so the live tests stay fast; the
	// aggregator itself enumerates whatever is registered.
	_ "github.com/metatube-community/metatube-sdk-go/provider/avbase"
	_ "github.com/metatube-community/metatube-sdk-go/provider/wikipedia"
)

func TestMergedActor_SearchActor(t *testing.T) {
	m := NewActor()
	require.NotEmpty(t, m.providers)

	results, err := m.SearchActor("小野坂ゆいか")
	require.NoError(t, err)
	require.Len(t, results, 1)
	result := results[0]
	require.Equal(t, "Merged", result.Provider)
	require.Equal(t, "小野坂ゆいか", result.ID)
	require.Equal(t, "小野坂ゆいか", result.Name)
	require.True(t, result.IsValid())
	t.Logf("%+v", result)
}

func TestMergedActor_GetActorInfoByID(t *testing.T) {
	m := NewActor()

	info, err := m.GetActorInfoByID("小野坂ゆいか")
	require.NoError(t, err)
	require.True(t, info.IsValid())
	require.Equal(t, "Merged", info.Provider)
	require.Equal(t, "小野坂ゆいか", info.Name)
	require.NotEmpty(t, info.Measurements) // AVBASE wins scalars in practice.
	require.NotEmpty(t, info.Aliases)      // union across providers.
	require.NotZero(t, info.Height)
	require.NotEmpty(t, info.BloodType) // Wikipedia fills the gap AVBASE leaves.
	require.NotEmpty(t, info.Summary)   // Wikipedia REST extract.
	t.Logf("measurements=%s cup=%s blood=%s height=%d aliases=%v summary=%.80s",
		info.Measurements, info.CupSize, info.BloodType, info.Height, info.Aliases, info.Summary)
}

func TestMergedActor_GetActorInfoByID_NotFound(t *testing.T) {
	m := NewActor()
	_, err := m.GetActorInfoByID("不存在的演员名字ZZZ123")
	require.ErrorIs(t, err, provider.ErrInfoNotFound)
}

func TestMergedActor_MergeActorInfos(t *testing.T) {
	merged := mergeActorInfos("Test Actor", []*model.ActorInfo{
		{
			Homepage:     "https://example.com/1",
			Measurements: "B:97 / W:62 / H:100",
			CupSize:      "H",
			Height:       164,
			Aliases:      []string{"Alias A"},
			Images:       []string{"https://example.com/i1.jpg"},
		},
		{
			Homepage:     "https://example.com/2",
			Summary:      "Second summary",
			BloodType:    "A",
			Measurements: "B:95 / W:62 / H:90",
			CupSize:      "G",
			Height:       165,
			Aliases:      []string{"Alias A", "Alias B"},
			Images:       []string{"https://example.com/i1.jpg", "https://example.com/i2.jpg"},
			Hobby:        "Hobby B",
		},
	})
	require.Equal(t, "https://example.com/1", merged.Homepage) // first-non-empty wins.
	require.Equal(t, "B:97 / W:62 / H:100", merged.Measurements)
	require.Equal(t, "H", merged.CupSize)
	require.Equal(t, 164, merged.Height)
	require.Equal(t, "Second summary", merged.Summary) // gap filled by second.
	require.Equal(t, "A", merged.BloodType)
	require.Equal(t, "Hobby B", merged.Hobby)
	require.Equal(t, []string{"Alias A", "Alias B"}, []string(merged.Aliases)) // union, deduped.
	require.Len(t, merged.Images, 2)
}

func TestMergedActor_SameName(t *testing.T) {
	require.True(t, sameActorName("小野坂ゆいか", "小野坂ゆいか"))
	require.True(t, sameActorName("  小野坂ゆいか ", "小野坂ゆいか"))
	require.True(t, sameActorName("Yuika Onosaka", "yuika onosaka"))
	require.True(t, sameActorName("ＡＢＣ", "ABC")) // full-width folding.
	require.False(t, sameActorName("小野坂ゆいか", "小野坂美咲"))
	require.False(t, sameActorName("", "小野坂ゆいか"))
}
