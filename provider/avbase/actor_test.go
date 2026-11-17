package avbase

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/metatube-community/metatube-sdk-go/provider/internal/testkit"
)

func TestAVBaseActor_GetActorInfoByID(t *testing.T) {
	testkit.Test(t, NewActor, []string{
		"三上悠亜",
		"愛沢さら",
	})
}

func TestAVBaseActor_GetActorInfoByURL(t *testing.T) {
	testkit.Test(t, NewActor, []string{
		"https://www.avbase.net/talents/三上悠亜",
	})
}

func TestAVBaseActor_SearchActor(t *testing.T) {
	actor := NewActor()

	results, err := actor.SearchActor("三上悠亜")
	require.NoError(t, err)
	require.Len(t, results, 1)
	require.True(t, results[0].IsValid())
	t.Logf("%+v", results)

	// Unknown names fail closed with no results.
	results, err = actor.SearchActor("ThisNameDoesNotExistXYZ")
	require.NoError(t, err)
	require.Empty(t, results)
}

func TestAVBaseActor_ProfileFields(t *testing.T) {
	actor := NewActor()
	info, err := actor.GetActorInfoByID("三上悠亜")
	require.NoError(t, err)
	require.Equal(t, "三上悠亜", info.Name)
	require.Equal(t, 159, info.Height)
	require.Equal(t, "B:90 / W:57 / H:88", info.Measurements)
	require.Equal(t, "H", info.CupSize)
	require.Equal(t, "A", info.BloodType)
	require.NotEmpty(t, info.Images)
	t.Logf("birthday=%v hobby=%s aliases=%v", info.Birthday, info.Hobby, info.Aliases)
}
