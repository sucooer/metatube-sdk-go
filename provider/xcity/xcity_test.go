package xcity

import (
	"testing"

	"github.com/metatube-community/metatube-sdk-go/provider/internal/testkit"
)

func TestXCity_GetMovieInfoByID(t *testing.T) {
	testkit.Test(t, New, []string{
		"214338", // BDST-131
	})
}

func TestXCity_GetMovieInfoByURL(t *testing.T) {
	testkit.Test(t, New, []string{
		"https://xcity.jp/avod/detail/?id=214338",
	})
}

func TestXCity_SearchMovie(t *testing.T) {
	testkit.Test(t, New, []string{
		"BDST-131",
	})
}
