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

// jsonObject returns s without surrounding whitespace when it is a valid JSON
// object literal, and reports whether it qualifies.
//
// Validating with json.Valid is what makes the data-page search below safe:
// candidates that are only *shaped* like JSON (a route name, an element id
// such as "app", or a large DOM subtree that merely starts with "{") are
// rejected instead of being handed to json.Unmarshal.
func jsonObject(s string) (string, bool) {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "{") || !json.Valid([]byte(s)) {
		return "", false
	}
	return s, true
}

// extractDataPage returns the JSON document describing the current Inertia.js
// page state.
//
// fc2cmadb.com is a Laravel + Inertia application whose embed layout has
// changed over time, so several carriers must be considered:
//
//	Inertia v1: <div id="app" data-page="{&quot;component&quot;:...}">
//	Inertia v2: <script data-page="app" type="application/json">{...}</script>
//
// In v1 the state is the (HTML-escaped) data-page *attribute* value. In v2 it
// moved into the *text content* of the script element, and the data-page
// attribute itself now only holds the element id ("app"). A page can also
// carry unrelated data-page attributes such as <body data-page="articles.show">,
// whose value is a Laravel route name rather than JSON.
//
// Matching the first element in document order therefore yields a non-JSON
// string, and reading only attribute values misses the v2 layout entirely:
// both produced "invalid character 'a' looking for beginning of value" or a
// spurious "not found" for a perfectly valid page. Every candidate is
// validated with jsonObject so the first genuine JSON object wins regardless
// of layout or document order.
func extractDataPage(body []byte) (string, error) {
	doc, err := htmlquery.Parse(bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("fc2ppvdb: failed to parse HTML: %w", err)
	}

	// Inertia v1: state in the data-page attribute of the #app root element.
	if e, err := htmlquery.Query(doc, "//*[@id='app'][@data-page]"); err == nil && e != nil {
		if val, ok := jsonObject(htmlquery.SelectAttr(e, "data-page")); ok {
			return val, nil
		}
	}

	// Inertia v2: state in the text content of a <script data-page="app">.
	// Checked before the generic sweep because a script's text is the whole
	// page state, whereas a generic element's text is rendered markup.
	if nodes, err := htmlquery.QueryAll(doc, "//script[@data-page]"); err == nil {
		for _, n := range nodes {
			if val, ok := jsonObject(htmlquery.InnerText(n)); ok {
				return val, nil
			}
		}
	}

	// Fallbacks, for layout changes that rename the app root or move the
	// state to a different element. Attribute values are preferred over text
	// content because they cannot be polluted by surrounding markup.
	nodes, err := htmlquery.QueryAll(doc, "//*[@data-page]")
	if err != nil {
		return "", fmt.Errorf("fc2ppvdb: invalid XPath expression: %w", err)
	}
	for _, n := range nodes {
		if val, ok := jsonObject(htmlquery.SelectAttr(n, "data-page")); ok {
			return val, nil
		}
	}
	for _, n := range nodes {
		if val, ok := jsonObject(htmlquery.InnerText(n)); ok {
			return val, nil
		}
	}
	return "", fmt.Errorf("fc2ppvdb: Inertia page state not found in the document (site layout changed or bot challenge page)")
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
