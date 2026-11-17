package javdb

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/metatube-community/metatube-sdk-go/provider/internal/testkit"
)

func TestJavDBActor_GetActorInfoByID(t *testing.T) {
	testkit.Test(t, NewActor, []string{
		"Av2e", // 三上悠亜
	})
}

func TestJavDBActor_GetActorInfoByURL(t *testing.T) {
	testkit.Test(t, NewActor, []string{
		"https://javdb.com/actors/Av2e",
	})
}

func TestJavDBActor_SearchActor(t *testing.T) {
	// Actor search shares JavDB's throttled /search endpoint: skip on
	// throttle errors; an empty result for these known names is a failure.
	db := NewActor()
	for _, keyword := range []string{"三上悠亜", "架乃由羅"} {
		t.Run(keyword, func(t *testing.T) {
			results, err := db.SearchActor(keyword)
			if err != nil && isThrottleError(err) {
				t.Skipf("javdb search throttled: %v", err)
			}
			require.NoError(t, err)
			require.NotEmpty(t, results)
			for _, r := range results {
				require.True(t, r.IsValid())
			}
			t.Logf("%+v", results)
		})
	}
}
