package javdb

import (
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/metatube-community/metatube-sdk-go/provider"
	"github.com/metatube-community/metatube-sdk-go/provider/internal/testkit"
)

func TestJavDB_GetMovieInfoByID(t *testing.T) {
	// Slug-based lookup needs no search, so it is throttle-proof.
	testkit.Test(t, New, []string{
		"YYdEb", // SSIS-354
	})
}

func TestJavDB_GetMovieInfoByID_Number(t *testing.T) {
	// Number-based lookup resolves via search, which JavDB throttles per IP
	// (HTTP 403). These codes are known to exist, so ErrInfoNotFound here
	// can only mean throttling: skip instead of failing.
	db := New()
	for _, id := range []string{"SSIS-354", "ABP-588"} {
		t.Run(id, func(t *testing.T) {
			info, err := db.GetMovieInfoByID(id)
			if errors.Is(err, provider.ErrInfoNotFound) {
				t.Skip("javdb search throttled or number missing")
			}
			require.NoError(t, err)
			require.NotNil(t, info)
			require.True(t, info.IsValid())
			t.Logf("%+v", info)
		})
	}
}

func TestJavDB_GetMovieInfoByURL(t *testing.T) {
	testkit.Test(t, New, []string{
		"https://javdb.com/v/YYdEb",
	})
}

func TestJavDB_SearchMovie(t *testing.T) {
	// JavDB throttles anonymous search per IP (HTTP 403 "Forbidden").
	// Skip on throttle errors; an empty result for these known-good codes
	// is a real failure.
	db := New()
	for _, keyword := range []string{"SSIS-354", "ABP-588"} {
		t.Run(keyword, func(t *testing.T) {
			results, err := db.SearchMovie(db.NormalizeMovieKeyword(keyword))
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

func isThrottleError(err error) bool {
	msg := err.Error()
	return strings.Contains(msg, "Forbidden") ||
		strings.Contains(msg, "Too Many Requests") ||
		strings.Contains(msg, "403") ||
		strings.Contains(msg, "429")
}
