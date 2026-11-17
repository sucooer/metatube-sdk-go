package airav

import (
	"testing"

	"github.com/metatube-community/metatube-sdk-go/provider/internal/testkit"
)

func TestAiRAV_GetMovieInfoByID(t *testing.T) {
	testkit.Test(t, New, []string{
		"99-21-52184", // SSIS-354
	})
}

func TestAiRAV_GetMovieInfoByURL(t *testing.T) {
	testkit.Test(t, New, []string{
		"https://airav.io/video?hid=99-21-52184",
	})
}

func TestAiRAV_SearchMovie(t *testing.T) {
	testkit.Test(t, New, []string{
		"SSIS-354",
	})
}
