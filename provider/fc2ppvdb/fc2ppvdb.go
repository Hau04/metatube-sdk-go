package fc2ppvdb

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"strings"

	"github.com/antchfx/htmlquery"
	"github.com/gocolly/colly/v2"
	"golang.org/x/text/language"

	"github.com/metatube-community/metatube-sdk-go/common/parser"
	"github.com/metatube-community/metatube-sdk-go/model"
	"github.com/metatube-community/metatube-sdk-go/provider"
	"github.com/metatube-community/metatube-sdk-go/provider/fc2/fc2util"
	"github.com/metatube-community/metatube-sdk-go/provider/internal/scraper"
)

var _ provider.MovieProvider = (*FC2PPVDB)(nil)

const (
	Name     = "FC2PPVDB"
	Priority = 1000 - 2
)

const (
	baseURL  = "https://fc2cmadb.com/"
	movieURL = "https://fc2cmadb.com/articles/%s"
)

// defaultCoverURL is the placeholder the site serves for videos without a
// custom cover. MetaTube treats a movie as invalid without a cover
// (model.MovieInfo.IsValid requires a non-empty CoverURL), so the placeholder
// is reported instead of leaving the field empty.
const defaultCoverURL = baseURL + "storage/images/article/no-image.jpg"

// The database was rebranded from fc2ppvdb.com to fc2cmadb.com in 2026
// (see metatube-community/metatube-sdk-go#363). Movie homepages cached by
// older versions of this provider may still point at the legacy domain.
var legacyHosts = map[string]string{
	"fc2ppvdb.com":     "fc2cmadb.com",
	"www.fc2ppvdb.com": "fc2cmadb.com",
}

type FC2PPVDB struct {
	*scraper.Scraper
}

func New() *FC2PPVDB {
	return &FC2PPVDB{scraper.NewDefaultScraper(Name, baseURL, Priority, language.Japanese)}
}

func (fc2ppvdb *FC2PPVDB) NormalizeMovieID(id string) string {
	return fc2util.ParseNumber(id)
}

func (fc2ppvdb *FC2PPVDB) GetMovieInfoByID(id string) (info *model.MovieInfo, err error) {
	return fc2ppvdb.GetMovieInfoByURL(fmt.Sprintf(movieURL, id))
}

func (fc2ppvdb *FC2PPVDB) ParseMovieIDFromURL(rawURL string) (string, error) {
	homepage, err := url.Parse(rawURL)
	if err != nil {
		return "", err
	}
	return path.Base(homepage.Path), nil
}

// GetMovieInfoByURL fetches the movie page and parses the metadata from the
// Inertia.js "data-page" attribute embedded in the page (see inertiaPage).
func (fc2ppvdb *FC2PPVDB) GetMovieInfoByURL(rawURL string) (info *model.MovieInfo, err error) {
	homepage, err := url.Parse(rawURL)
	if err != nil {
		return
	}
	// Map legacy domains to the current one so that cached homepages from
	// before the rebrand keep working.
	if host, ok := legacyHosts[strings.ToLower(homepage.Hostname())]; ok {
		homepage.Host = host
	}
	homepageStr := homepage.String()

	id, err := fc2ppvdb.ParseMovieIDFromURL(homepageStr)
	if err != nil {
		return
	}
	if id == "" {
		err = provider.ErrInvalidURL
		return
	}

	// Initial info, may be overwritten below.
	info = &model.MovieInfo{
		ID:            id,
		Number:        fmt.Sprintf("FC2-%s", id),
		Provider:      fc2ppvdb.Name(),
		Homepage:      homepageStr,
		Actors:        []string{},
		PreviewImages: []string{},
		Genres:        []string{},
	}

	c := fc2ppvdb.ClonedCollector()

	var (
		parsedInfo *model.MovieInfo
		parseErr   error
		statusCode int
	)
	c.OnResponse(func(r *colly.Response) {
		dataPage, e := extractDataPage(r.Body)
		if e != nil {
			parseErr = e
			return
		}
		var page inertiaPage
		if e = json.Unmarshal([]byte(dataPage), &page); e != nil {
			parseErr = fmt.Errorf("fc2ppvdb: failed to parse data-page JSON: %w", e)
			return
		}
		parsedInfo, e = buildMovieInfo(&page, homepageStr)
		if e != nil {
			parseErr = e
			return
		}
	})

	c.OnError(func(r *colly.Response, _ error) {
		if r != nil {
			statusCode = r.StatusCode
		}
	})

	if e := c.Visit(homepageStr); e != nil {
		switch statusCode {
		case http.StatusNotFound:
			return nil, provider.ErrInfoNotFound
		case http.StatusForbidden, http.StatusNotAcceptable, http.StatusTooManyRequests:
			return nil, fmt.Errorf("fc2ppvdb: request blocked by the site (HTTP %d)",
				statusCode)
		default:
			return nil, e
		}
	}
	if parseErr != nil {
		return nil, parseErr
	}
	if parsedInfo == nil {
		return nil, fmt.Errorf("fc2ppvdb: no movie data found in page %s", homepageStr)
	}
	return parsedInfo, nil
}

// extractDataPage returns the JSON document stored in the "data-page" HTML
// attribute of the Inertia app root element.
//
// A page can carry more than one element with a data-page attribute: the
// production layout also sets <body data-page="articles.show">, whose value is
// the Laravel route name rather than JSON. Matching the first element in
// document order therefore yields "articles.show", which json.Unmarshal
// rejects with "invalid character 'a' looking for beginning of value". The
// Inertia page state always lives on the #app root element, so that element is
// preferred, and any data-page value that is not a JSON object is skipped.
func extractDataPage(body []byte) (string, error) {
	node, err := htmlquery.Parse(bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("fc2ppvdb: failed to parse HTML: %w", err)
	}
	// Preferred: the Inertia app root element.
	if e, err := htmlquery.Query(node, "//*[@id='app'][@data-page]"); err == nil && e != nil {
		if val := strings.TrimSpace(htmlquery.SelectAttr(e, "data-page")); strings.HasPrefix(val, "{") {
			return val, nil
		}
	}
	// Fallback: any element whose data-page value is a JSON object, in case
	// the app root is renamed but the page state is still embedded.
	nodes, err := htmlquery.QueryAll(node, "//*[@data-page]")
	if err != nil {
		return "", fmt.Errorf("fc2ppvdb: invalid XPath expression: %w", err)
	}
	for _, n := range nodes {
		val := strings.TrimSpace(htmlquery.SelectAttr(n, "data-page"))
		if strings.HasPrefix(val, "{") {
			return val, nil
		}
	}
	return "", fmt.Errorf("fc2ppvdb: data-page JSON attribute not found (site layout changed or bot challenge page)")
}

// buildMovieInfo maps the Inertia page state to a *model.MovieInfo.
func buildMovieInfo(page *inertiaPage, homepage string) (info *model.MovieInfo, err error) {
	a := page.Props.Article
	if a == nil {
		return nil, provider.ErrInfoNotFound
	}

	// Video ID. The server has been observed to send it either as a JSON
	// string or a number, hence flexString.
	id := string(a.VideoID)
	if id == "" {
		id = string(a.ID)
	}
	if id == "" {
		return nil, provider.ErrInfoNotFound
	}

	info = &model.MovieInfo{
		ID:            id,
		Number:        fmt.Sprintf("FC2-%s", id),
		Provider:      Name,
		Homepage:      homepage,
		Actors:        []string{},
		PreviewImages: []string{},
		Genres:        []string{},
		Title:         strings.TrimSpace(a.Title),
	}

	// Cover image. Videos without a custom cover are served the site's
	// "no image" placeholder; it is kept rather than discarded because
	// MetaTube rejects metadata with an empty CoverURL.
	if u := strings.TrimSpace(string(a.ImageURL)); u != "" {
		if abs, e := url.Parse(u); e == nil {
			if !abs.IsAbs() {
				if abs, e = url.Parse(baseURL + strings.TrimLeft(u, "/")); e == nil {
					info.CoverURL = abs.String()
				}
			} else {
				info.CoverURL = abs.String()
			}
		}
	}
	if info.CoverURL == "" {
		info.CoverURL = defaultCoverURL
	}

	// Maker (writer).
	if w := a.Writer; w != nil {
		info.Maker = strings.TrimSpace(string(w.Name))
	}

	info.ReleaseDate = parser.ParseDate(string(a.ReleaseDate))
	info.Runtime = parser.ParseRuntime(string(a.Duration))

	// Genres (tags).
	for _, t := range a.Tags {
		if n := strings.TrimSpace(string(t.Name)); n != "" {
			info.Genres = append(info.Genres, n)
		}
	}

	// Actors. Note: on the current site the actresses list is a lazy-loaded
	// Inertia prop that is only included for logged-in sessions, so this is
	// best-effort and typically empty for anonymous scraping.
	for _, act := range page.Props.Actresses {
		if act == nil {
			continue
		}
		if n := strings.TrimSpace(string(act.Name)); n != "" {
			info.Actors = append(info.Actors, n)
		}
	}

	return
}

func init() {
	provider.Register(Name, New)
}
