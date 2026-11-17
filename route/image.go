package route

import (
	"bytes"
	"errors"
	"image"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/gin-gonic/gin/render"

	R "github.com/metatube-community/metatube-sdk-go/constant"
	"github.com/metatube-community/metatube-sdk-go/engine"
	"github.com/metatube-community/metatube-sdk-go/imageutil"
	"github.com/metatube-community/metatube-sdk-go/imageutil/badge"
	mt "github.com/metatube-community/metatube-sdk-go/provider"
)

type imageType uint8

const (
	primaryImageType imageType = iota
	thumbImageType
	backdropImageType
)

type imageUri struct {
	infoUri // same as info uri
}

type imageQuery struct {
	URL      string  `form:"url"`
	Ratio    float64 `form:"ratio"`
	Position float64 `form:"pos"`
	Auto     bool    `form:"auto"`
	Badge    string  `form:"badge"`
	Quality  int     `form:"quality"`
}

func getImage(app *engine.Engine, typ imageType) gin.HandlerFunc {
	var ratio float64
	switch typ {
	case primaryImageType:
		ratio = R.PrimaryImageRatio
	case thumbImageType:
		ratio = R.ThumbImageRatio
	case backdropImageType:
		ratio = R.BackdropImageRatio
	default:
		panic("invalid image type")
	}

	return func(c *gin.Context) {
		uri := &imageUri{}
		if err := c.ShouldBindUri(uri); err != nil {
			abortWithStatusMessage(c, http.StatusBadRequest, err)
			return
		}
		query := &imageQuery{
			Ratio:    -1,
			Position: -1,
			Quality:  90,
		}
		if err := c.ShouldBindQuery(query); err != nil {
			abortWithStatusMessage(c, http.StatusBadRequest, err)
			return
		}

		// A provider name may implement both the actor and the movie
		// provider interfaces (e.g. "Merged"). Actors have no
		// thumb/backdrop images, and movie codes and person names are
		// disjoint ID spaces, so resolve the kind from the image type
		// and fall back between the two lookups for primary images
		// instead of preferring one interface outright.
		isActorProvider := app.IsActorProvider(uri.Provider)
		isMovieProvider := app.IsMovieProvider(uri.Provider)
		if !isActorProvider && !isMovieProvider {
			abortWithError(c, mt.ErrProviderNotFound)
			return
		}

		var (
			img image.Image
			err error
		)
		if query.URL != "" /* specified URL */ {
			var provider mt.Provider
			if isActorProvider {
				provider = app.MustGetActorProviderByName(uri.Provider)
			} else {
				provider = app.MustGetMovieProviderByName(uri.Provider)
			}
			// query.Ratio should apply only to the primary images.
			if typ != primaryImageType || query.Ratio < 0 {
				query.Ratio = ratio
			}
			img, err = app.GetImageByURL(provider, query.URL, query.Ratio, query.Position, query.Auto)
		} else if typ != primaryImageType /* thumb/backdrop: movies only */ {
			if !isMovieProvider {
				abortWithStatusMessage(c, http.StatusBadRequest, "unsupported image type")
				return
			}
			switch typ {
			case thumbImageType:
				img, err = app.GetMovieThumbImage(uri.AsProviderID())
			case backdropImageType:
				img, err = app.GetMovieBackdropImage(uri.AsProviderID())
			}
		} else if isActorProvider && isMovieProvider /* dual provider */ {
			// Movie codes never resolve as actor names and vice
			// versa, so try the movie lookup first and fall back
			// to the actor lookup when the movie is not found.
			img, err = app.GetMoviePrimaryImage(uri.AsProviderID(), query.Ratio, query.Position)
			if errors.Is(err, mt.ErrInfoNotFound) {
				img, err = app.GetActorPrimaryImage(uri.AsProviderID())
			}
		} else if isMovieProvider /* movie only */ {
			img, err = app.GetMoviePrimaryImage(uri.AsProviderID(), query.Ratio, query.Position)
		} else /* actor only */ {
			img, err = app.GetActorPrimaryImage(uri.AsProviderID())
		}
		if err != nil {
			abortWithError(c, err)
			return
		}

		if query.Badge != "" {
			if img, err = badge.Badge(img, query.Badge); err != nil {
				abortWithError(c, err)
				return
			}
		}

		c.Header("X-MetaTube-Image-Width", strconv.Itoa(img.Bounds().Dx()))
		c.Header("X-MetaTube-Image-Height", strconv.Itoa(img.Bounds().Dy()))

		buf := &bytes.Buffer{}
		if err = imageutil.EncodeToJPEG(buf, img, query.Quality); err != nil {
			panic(err)
		}

		c.Render(http.StatusOK, render.Reader{
			ContentType:   jpegImageMIMEType,
			ContentLength: int64(buf.Len()),
			Reader:        buf,
		})
	}
}

const jpegImageMIMEType = "image/jpeg"
