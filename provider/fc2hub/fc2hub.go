package fc2hub

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/gocolly/colly/v2"
	"golang.org/x/net/html"
	"golang.org/x/text/language"

	"github.com/metatube-community/metatube-sdk-go/common/parser"
	"github.com/metatube-community/metatube-sdk-go/model"
	"github.com/metatube-community/metatube-sdk-go/provider"
	"github.com/metatube-community/metatube-sdk-go/provider/fc2/fc2util"
	"github.com/metatube-community/metatube-sdk-go/provider/internal/scraper"
)

var (
	_ provider.MovieProvider = (*FC2HUB)(nil)
	_ provider.MovieSearcher = (*FC2HUB)(nil)
)

const (
	Name     = "fc2hub"
	Priority = 1000 - 1
)

const (
	baseURL   = "https://javten.com/"
	movieURL  = "https://javten.com/video/%s/id%s/%s"
	searchURL = "https://javten.com/search?kw=%s"
)

type FC2HUB struct {
	*scraper.Scraper
}

func New() *FC2HUB {
	return &FC2HUB{scraper.NewDefaultScraper(Name, baseURL, Priority, language.Japanese)}
}

func (fc2hub *FC2HUB) GetMovieInfoByID(id string) (info *model.MovieInfo, err error) {
	ss := strings.SplitN(id, "-", 2)
	if len(ss) != 2 {
		return nil, provider.ErrInvalidID
	}
	const padding = "%20" // use padding to fix weird colly trailing path issue.
	return fc2hub.GetMovieInfoByURL(fmt.Sprintf(movieURL, ss[0], ss[1], padding))
}

func (fc2hub *FC2HUB) ParseMovieIDFromURL(rawURL string) (string, error) {
	homepage, err := url.Parse(rawURL)
	if err != nil {
		return "", err
	}
	if ss := videoPathPattern.FindStringSubmatch(homepage.Path); len(ss) == 3 {
		return fmt.Sprintf("%s-%s", ss[1], ss[2]), nil
	}
	return "", provider.ErrInvalidURL
}

func (fc2hub *FC2HUB) GetMovieInfoByURL(rawURL string) (info *model.MovieInfo, err error) {
	id, err := fc2hub.ParseMovieIDFromURL(rawURL)
	if err != nil {
		return
	}

	info = &model.MovieInfo{
		ID:            id, // Dual-ID (id+number)
		Provider:      fc2hub.Name(),
		Homepage:      rawURL,
		Actors:        []string{},
		PreviewImages: []string{},
		Genres:        []string{},
	}

	c := fc2hub.ClonedCollector()
	// Allow redirecting, for cases like http -> https
	c.SetRedirectHandler(func(req *http.Request, via []*http.Request) error {
		return nil
	})

	// Title
	c.OnXML(`//*[@id="content"]/div/div[2]/div[1]/div[1]/div[2]/h1`, func(e *colly.XMLElement) {
		if title := cleanTitle(e.Text); title != "" && !isNumberOnly(title) {
			info.Title = title
		}
	})

	// Head metadata, used when the page layout no longer matches the XPaths
	// above (for example when the product is marked unavailable).
	var metaTitle, metaImage string
	c.OnXML(`//meta[@property="og:title" or @name="twitter:title"]`, func(e *colly.XMLElement) {
		if metaTitle == "" {
			metaTitle = cleanTitle(e.Attr("content"))
		}
	})
	c.OnXML(`/html/head/title`, func(e *colly.XMLElement) {
		if metaTitle == "" {
			metaTitle = cleanTitle(e.Text)
		}
	})
	c.OnXML(`//meta[@property="og:image" or @name="twitter:image"]`, func(e *colly.XMLElement) {
		if metaImage == "" && strings.TrimSpace(e.Attr("content")) != "" {
			metaImage = e.Request.AbsoluteURL(strings.TrimSpace(e.Attr("content")))
		}
	})

	// Summary
	c.OnXML(`//*[@id="content"]/div/div[2]/div[1]/div[1]/div[2]/div[2]/div`, func(e *colly.XMLElement) {
		info.Summary = strings.TrimSpace(e.Text)
	})

	// Number
	c.OnXML(`//*[@id="content"]/div/div[2]/div[1]/div[1]/div[2]/div[1]/div[2]/h1`, func(e *colly.XMLElement) {
		if num := fc2util.ParseNumber(strings.TrimSpace(e.Text)); num != "" {
			info.Number = fmt.Sprintf("FC2-%s", num)
		}
	})

	// Genres
	c.OnXML(`//*[@id="content"]/div/div[2]/div[1]/div[1]/div[2]/p/a`, func(e *colly.XMLElement) {
		if genre := strings.TrimSpace(e.Text); genre != "" {
			info.Genres = append(info.Genres, genre)
		}
	})

	// Maker
	c.OnXML(`//*[@id="content"]/div/div[2]/div[1]/div[3]/div/div[2]/div/div[2]`, func(e *colly.XMLElement) {
		// info.Maker = strings.TrimSpace(strings.Split(e.Text, "\n")[0])
		for n := e.DOM.(*html.Node).FirstChild; n != nil; n = n.NextSibling {
			if n.Type == html.TextNode {
				info.Maker = strings.TrimSpace(n.Data)
				break
			}
		}
	})

	// Preview Images
	c.OnXML(`//*[@id="content"]/div/div[2]/div[1]/div[2]/div[3]/div/div//a[@data-fancybox="gallery"]`, func(e *colly.XMLElement) {
		if href := e.Attr("href"); href != "" {
			info.PreviewImages = append(info.PreviewImages, e.Request.AbsoluteURL(href))
		}
	})

	// Fields
	c.OnXML(`//script[@type="application/ld+json"]`, func(e *colly.XMLElement) {
		data := struct {
			Type string `json:"@type"`
			// `Movie`
			Name          string   `json:"name"`
			Description   string   `json:"description"`
			Image         flexStrings `json:"image"`
			Identifier    flexStrings `json:"identifier"`
			DatePublished string      `json:"datePublished"`
			Duration      string      `json:"duration"`
			Actor         flexStrings `json:"actor"`
			Genre         flexStrings `json:"genre"`
			Director      flexStrings `json:"director"`
			// `CreativeWorkSeries`
			AggregateRating struct {
				BestRating  float64 `json:"bestRating"`
				WorstRating float64 `json:"worstRating"`
				RatingCount int     `json:"ratingCount"`
				RatingValue float64 `json:"ratingValue"`
			}
			// `WebPage`
			URL string `json:"url"`
		}{}
		if json.Unmarshal([]byte(strings.TrimSpace(e.Text)), &data) == nil {
			switch data.Type {
			case "Movie":
				if data.Name != "" {
					info.Title = data.Name
				}
				if info.Summary == "" {
					info.Summary = data.Description
				}
				if len(data.Director) > 0 && data.Director[0] != "" {
					// Use director as maker.
					info.Maker = data.Director[0]
				}
				if len(info.Genres) == 0 {
					info.Genres = removeEmpty([]string(data.Genre))
				}
				if len(data.Actor) > 0 {
					info.Actors = removeEmpty([]string(data.Actor))
				}
				for _, identifier := range data.Identifier {
					if num := fc2util.ParseNumber(identifier); num != "" {
						info.Number = fmt.Sprintf("FC2-%s", num)
						break
					}
				}
				if len(data.Image) > 0 && data.Image[0] != "" {
					info.CoverURL = e.Request.AbsoluteURL(data.Image[0])
				}
				info.ReleaseDate = parser.ParseDate(data.DatePublished)
				info.Runtime = parser.ParseRuntime(data.Duration)
			case "CreativeWorkSeries":
				// Average rating score.
				info.Score = data.AggregateRating.RatingValue
			case "WebPage":
				//if data.URL != "" {
				//	// Update homepage URL.
				//	info.Homepage = data.URL
				//}
			}
		}
	})

	// Cover (fallback)
	c.OnScraped(func(_ *colly.Response) {
		if info.Title == "" || isNumberOnly(info.Title) {
			if metaTitle != "" && !isNumberOnly(metaTitle) {
				info.Title = metaTitle
			}
		}
		if info.Number == "" {
			// The URL always carries the FC2 id: /video/{vid}/id{number}.
			if ss := strings.SplitN(id, "-", 2); len(ss) == 2 {
				if num := fc2util.ParseNumber(ss[1]); num != "" {
					info.Number = fmt.Sprintf("FC2-%s", num)
				}
			}
		}
		if info.CoverURL == "" {
			info.CoverURL = metaImage
		}
		if info.CoverURL == "" && len(info.PreviewImages) > 0 {
			info.CoverURL = info.PreviewImages[0]
		}
		// cover as thumb image.
		info.ThumbURL = info.CoverURL
	})

	// Homepage (update)
	c.OnScraped(func(_ *colly.Response) {
		ss := strings.SplitN(info.ID, "-", 2)
		num := fc2util.ParseNumber(info.Number)
		if len(ss) == 2 && ss[0] != "" && num != "" && info.Title != "" {
			info.Homepage = fmt.Sprintf(movieURL, ss[0], num, url.PathEscape(info.Title))
		}
	})

	err = c.Visit(info.Homepage)
	return
}

func (fc2hub *FC2HUB) NormalizeMovieKeyword(keyword string) string {
	return fc2util.ParseNumber(keyword)
}

func (fc2hub *FC2HUB) SearchMovie(keyword string) (results []*model.MovieSearchResult, err error) {
	number := fc2util.ParseNumber(keyword)
	if number == "" {
		return nil, provider.ErrInvalidKeyword
	}

	var videoURL string
	c := fc2hub.ClonedCollector()
	c.ParseHTTPErrorResponse = true
	c.SetRedirectHandler(func(req *http.Request, via []*http.Request) error {
		// Record a redirect straight to the video page, but keep following
		// so a 200 search page (or a /en/ hop) is still handled below.
		if videoURL == "" && matchVideoPath(req.URL.Path, number) {
			videoURL = req.URL.String()
		}
		if len(via) >= 10 {
			return http.ErrUseLastResponse
		}
		return nil
	})

	c.OnResponse(func(r *colly.Response) {
		if videoURL != "" {
			return
		}
		if loc := r.Headers.Get("Location"); loc != "" {
			if u, e := url.Parse(r.Request.AbsoluteURL(loc)); e == nil && matchVideoPath(u.Path, number) {
				videoURL = u.String()
				return
			}
		}
		if matchVideoPath(r.Request.URL.Path, number) {
			videoURL = r.Request.URL.String()
			return
		}
		// Search results page, meta refresh or script redirect: take the
		// first link to /video/{vid}/id{number}.
		if m := videoLinkPattern.FindAllSubmatch(r.Body, -1); m != nil {
			for _, sm := range m {
				if string(sm[2]) == number {
					videoURL = r.Request.AbsoluteURL(string(sm[0]))
					return
				}
			}
		}
	})

	if e := c.Visit(fmt.Sprintf(searchURL, url.QueryEscape(number))); e != nil && videoURL == "" {
		return nil, e
	}
	if videoURL == "" {
		return nil, provider.ErrInfoNotFound
	}
	info, err := fc2hub.GetMovieInfoByURL(videoURL)
	if err != nil {
		return nil, err
	}
	if !info.IsValid() {
		return nil, provider.ErrIncompleteMetadata
	}
	return []*model.MovieSearchResult{info.ToSearchResult()}, nil
}

var (
	videoPathPattern = regexp.MustCompile(`/video/(\d+)/id(\d+)`)
	videoLinkPattern = regexp.MustCompile(`(?:https?://[^"'\s<>]+)?(?:/[a-z]{2})?/video/(\d+)/id(\d+)/?`)
	titleNumberHead  = regexp.MustCompile(`(?i)^\s*[\[【(]?\s*FC2[-_\s]*(?:PPV[-_\s]*)?\d+\s*[\]】)]?\s*`)
	titleSiteTail    = regexp.MustCompile(`(?i)\s*[-|｜]\s*(?:JAVten(?:\.com)?|FC2HUB(?:\.com)?)\s*$`)
)

func matchVideoPath(p, number string) bool {
	m := videoPathPattern.FindStringSubmatch(p)
	return m != nil && (number == "" || m[2] == number)
}

// cleanTitle strips the "[FC2-PPV-123]" prefix and the site suffix that the
// page <title> and og:title carry.
func cleanTitle(s string) string {
	s = strings.TrimSpace(s)
	s = titleSiteTail.ReplaceAllString(s, "")
	if t := strings.TrimSpace(titleNumberHead.ReplaceAllString(s, "")); t != "" {
		s = t
	}
	return strings.TrimSpace(s)
}

func isNumberOnly(s string) bool {
	return titleNumberHead.ReplaceAllString(strings.TrimSpace(s), "") == ""
}

// flexStrings accepts a JSON string, an array of strings, or objects with a
// name/url field (as schema.org allows for actor, genre and image).
type flexStrings []string

func (f *flexStrings) UnmarshalJSON(b []byte) error {
	var one any
	if err := json.Unmarshal(b, &one); err != nil {
		return err
	}
	*f = nil
	var walk func(v any)
	walk = func(v any) {
		switch t := v.(type) {
		case string:
			if t = strings.TrimSpace(t); t != "" {
				*f = append(*f, t)
			}
		case []any:
			for _, x := range t {
				walk(x)
			}
		case map[string]any:
			for _, k := range []string{"name", "url", "contentUrl", "value"} {
				if s, ok := t[k].(string); ok && strings.TrimSpace(s) != "" {
					*f = append(*f, strings.TrimSpace(s))
					return
				}
			}
		case float64:
			*f = append(*f, fmt.Sprintf("%.0f", t))
		}
	}
	walk(one)
	return nil
}

func removeEmpty(in []string) (out []string) {
	if len(in) == 0 {
		return in
	}
	out = make([]string, 0, len(in))
	for _, elem := range in {
		if strings.TrimSpace(elem) != "" {
			out = append(out, elem)
		}
	}
	return
}

func init() {
	provider.Register(Name, New)
}
