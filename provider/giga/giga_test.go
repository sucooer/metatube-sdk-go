package giga

import (
	"testing"

	"github.com/metatube-community/metatube-sdk-go/provider/internal/testkit"
)

func TestGiga_GetMovieInfoByID(t *testing.T) {
	testkit.Test(t, New, []string{
		"7740", // SPSF-40
	})
}

func TestGiga_GetMovieInfoByURL(t *testing.T) {
	testkit.Test(t, New, []string{
		"https://www.giga-web.jp/product/index.php?product_id=7740",
	})
}

func TestGiga_SearchMovie(t *testing.T) {
	testkit.Test(t, New, []string{
		"SPSF-40",
	})
}
