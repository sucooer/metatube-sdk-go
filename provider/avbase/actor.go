package avbase

import (
	"encoding/json"
	"fmt"
	"net/url"
	"path"
	"strconv"
	"strings"

	"github.com/gocolly/colly/v2"
	"golang.org/x/text/language"

	"github.com/metatube-community/metatube-sdk-go/common/fetch"
	"github.com/metatube-community/metatube-sdk-go/common/parser"
	"github.com/metatube-community/metatube-sdk-go/model"
	"github.com/metatube-community/metatube-sdk-go/provider"
	"github.com/metatube-community/metatube-sdk-go/provider/internal/scraper"
)

var (
	_ provider.ActorProvider = (*AVBaseActor)(nil)
	_ provider.ActorSearcher = (*AVBaseActor)(nil)
	_ provider.Fetcher       = (*AVBaseActor)(nil)
)

const (
	// ActorPriority is separate from the movie provider's Priority: the
	// actor side carries curated profiles (birthday, height, sizes, cup,
	// blood type, hobby) and must outrank Gfriends (999) in actor
	// search so the plugin picks it first.
	ActorPriority = 1000 + 3
)

const talentURL = "https://www.avbase.net/talents/%s"

// AVBaseActor is the performer side of AVBASE. It is a separate type
// from AVBase because the two sides need different priorities and the
// movie struct carries the sub-provider map used only for works.
//
// AvBase has no performer search endpoint; the talent page is addressed
// directly by name (/talents/{name}) and returns HTTP 404 for unknown
// names, so SearchActor is a direct-URL attempt that returns a result
// only when the page is valid.
type AVBaseActor struct {
	*fetch.Fetcher
	*scraper.Scraper
}

func NewActor() *AVBaseActor {
	return &AVBaseActor{
		Fetcher: fetch.Default(&fetch.Config{SkipVerify: true}),
		Scraper: scraper.NewDefaultScraper(
			Name, baseURL, ActorPriority, language.Japanese,
			scraper.WithHeaders(map[string]string{
				"Referer": baseURL,
			})),
	}
}

func (ab *AVBaseActor) NormalizeActorID(id string) string {
	return strings.TrimSpace(id)
}

func (ab *AVBaseActor) ParseActorIDFromURL(rawURL string) (string, error) {
	homepage, err := url.Parse(rawURL)
	if err != nil {
		return "", err
	}
	name, err := url.PathUnescape(path.Base(homepage.Path))
	if err != nil {
		return "", err
	}
	return ab.NormalizeActorID(name), nil
}

func (ab *AVBaseActor) GetActorInfoByID(id string) (*model.ActorInfo, error) {
	return ab.GetActorInfoByURL(fmt.Sprintf(talentURL, url.PathEscape(id)))
}

func (ab *AVBaseActor) GetActorInfoByURL(rawURL string) (*model.ActorInfo, error) {
	var info *model.ActorInfo

	c := ab.ClonedCollector()

	c.OnXML(`//*[@id="__NEXT_DATA__"]`, func(e *colly.XMLElement) {
		data := struct {
			Props struct {
				PageProps struct {
					Talent *talentResponse `json:"talent"`
				} `json:"pageProps"`
			} `json:"props"`
		}{}
		if err := json.NewDecoder(strings.NewReader(e.Text)).Decode(&data); err != nil {
			return
		}
		if data.Props.PageProps.Talent == nil {
			return
		}
		talent := data.Props.PageProps.Talent
		info = talentToActorInfo(talent)
		if info != nil {
			info.Provider = ab.Name()
			info.Homepage = rawURL
		}
	})

	if err := c.Visit(rawURL); err != nil {
		// A 404 (unknown name) or a challenge page fails closed: the
		// talent simply does not exist from this provider's view.
		if isNotFoundError(err) {
			return nil, provider.ErrInfoNotFound
		}
		return nil, err
	}
	if info == nil || !info.IsValid() {
		return nil, provider.ErrInfoNotFound
	}
	return info, nil
}

func (ab *AVBaseActor) SearchActor(keyword string) (results []*model.ActorSearchResult, err error) {
	keyword = strings.TrimSpace(keyword)
	if keyword == "" {
		return nil, provider.ErrInvalidKeyword
	}
	info, err := ab.GetActorInfoByID(keyword)
	if err != nil {
		if err == provider.ErrInfoNotFound {
			return nil, nil
		}
		return nil, err
	}
	return []*model.ActorSearchResult{
		{
			ID:       info.ID,
			Name:     info.Name,
			Provider: ab.Name(),
			Homepage: info.Homepage,
			Aliases:  info.Aliases,
			Images:   info.Images,
		},
	}, nil
}

func isNotFoundError(err error) bool {
	msg := err.Error()
	return strings.Contains(msg, "404") ||
		strings.Contains(msg, "Not Found") ||
		strings.Contains(msg, "403") ||
		strings.Contains(msg, "Forbidden")
}

type talentResponse struct {
	ID      int     `json:"id"`
	Primary actor   `json:"primary"`
	Actors  []actor `json:"actors"`
	Meta    struct {
		BasicInfo basicInfo `json:"basic_info"`
	} `json:"meta"`
}

type actor struct {
	Name     string `json:"name"`
	Ruby     string `json:"ruby"`
	ImageURL string `json:"image_url"`
	Meta     *struct {
		Fanza *basicInfo `json:"fanza"`
	} `json:"meta"`
}

type basicInfo struct {
	Birthday   string `json:"birthday"`
	Height     string `json:"height"`
	Bust       string `json:"bust"`
	Waist      string `json:"waist"`
	Hip        string `json:"hip"`
	Cup        string `json:"cup"`
	Hobby      string `json:"hobby"`
	BloodType  string `json:"blood_type"`
	Prefecture string `json:"prefectures"`
}

func (b basicInfo) isEmpty() bool {
	return b.Birthday == "" && b.Height == "" && b.Bust == "" &&
		b.Waist == "" && b.Hip == "" && b.Cup == "" &&
		b.Hobby == "" && b.BloodType == ""
}

func talentToActorInfo(talent *talentResponse) *model.ActorInfo {
	primary := talent.Primary
	if primary.Name == "" {
		return nil
	}
	info := &model.ActorInfo{
		ID:      primary.Name,
		Name:    primary.Name,
		Aliases: []string{},
		Images:  []string{},
	}
	seen := map[string]bool{primary.Name: true}
	addAlias := func(name string) {
		if name != "" && !seen[name] {
			seen[name] = true
			info.Aliases = append(info.Aliases, name)
		}
	}
	addImage := func(imageURL string) {
		if imageURL != "" && !seen[imageURL] {
			seen[imageURL] = true
			info.Images = append(info.Images, imageURL)
		}
	}
	addAlias(primary.Ruby)
	addImage(primary.ImageURL)
	for _, actor := range talent.Actors {
		addAlias(actor.Name)
		addAlias(actor.Ruby)
		addImage(actor.ImageURL)
	}

	// The curated basic_info wins; the per-source FANZA profile is a
	// fallback when it is empty.
	profile := talent.Meta.BasicInfo
	if profile.isEmpty() && primary.Meta != nil && primary.Meta.Fanza != nil {
		profile = *primary.Meta.Fanza
	}
	if profile.Birthday != "" {
		info.Birthday = parser.ParseDate(profile.Birthday)
	}
	if height, err := strconv.Atoi(profile.Height); err == nil {
		info.Height = height
	}
	if bust, err := strconv.Atoi(profile.Bust); err == nil {
		if waist, err := strconv.Atoi(profile.Waist); err == nil {
			if hip, err := strconv.Atoi(profile.Hip); err == nil {
				info.Measurements = fmt.Sprintf("B:%d / W:%d / H:%d", bust, waist, hip)
			}
		}
	}
	if profile.Cup != "" {
		info.CupSize = strings.ToUpper(profile.Cup)
	}
	if profile.Hobby != "" {
		info.Hobby = profile.Hobby
	}
	if profile.BloodType != "" {
		info.BloodType = profile.BloodType
	}
	return info
}

func init() {
	provider.Register(Name, NewActor)
}
