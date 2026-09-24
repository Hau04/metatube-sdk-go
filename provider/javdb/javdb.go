package javdb

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strings"
	"time"

	"github.com/gocolly/colly/v2"
	"golang.org/x/text/language"

	"github.com/metatube-community/metatube-sdk-go/common/parser"
	"github.com/metatube-community/metatube-sdk-go/model"
	"github.com/metatube-community/metatube-sdk-go/provider"
	"github.com/metatube-community/metatube-sdk-go/provider/internal/scraper"
)

var (
	_ provider.MovieProvider = (*JavDB)(nil)
	_ provider.MovieSearcher = (*JavDB)(nil)
)

const (
	Name = "JavDB"
	// Priority 994 places JavDB below the dedicated sources - the FC2 family
	// (998-1000) and JavBus (995) - and on the same tier as JAV321, which is
	// also 994. Both are general purpose databases of equal standing, so they
	// share a tier deliberately; a deployment that wants a strict order can
	// reorder them without touching either provider.
	Priority = 1000 - 6
)

const (
	// baseURL is the public site, used for the provider's URL() and for the
	// Homepage of every result.
	baseURL = "https://javdb.com/"
	// moviePageURL addresses a movie by its internal id.
	moviePageURL = "https://javdb.com/v/%s"
)

// movieIDPattern matches the opaque internal ids JavDB uses in its URLs, such
// as "82BkzE". digitsPattern matches a plain digit run, which is always a
// printed number rather than an internal id and must take the search path.
var (
	movieIDPattern = regexp.MustCompile(`^[A-Za-z0-9]{5,9}$`)
	digitsPattern  = regexp.MustCompile(`^[0-9]+$`)
)

// JavDB reads the public JSON API that the JavDB mobile app talks to.
type JavDB struct {
	*scraper.Scraper
}

// New returns a new JavDB provider.
func New() *JavDB {
	return &JavDB{scraper.NewDefaultScraper(Name, baseURL, Priority, language.Japanese)}
}

// looksLikeMovieID reports whether id addresses a movie directly, as opposed
// to being a printed number that has to be resolved through a search first.
func looksLikeMovieID(id string) bool {
	return movieIDPattern.MatchString(id) && !digitsPattern.MatchString(id)
}

// NormalizeMovieID trims the id. Both internal ids ("82BkzE", case sensitive)
// and printed numbers ("FC2-4925979", "SONE-123") are accepted here; the
// distinction is made in GetMovieInfoByID.
func (javdb *JavDB) NormalizeMovieID(id string) string {
	return strings.TrimSpace(id)
}

// ParseMovieIDFromURL parses the internal movie id from a javdb.com URL, e.g.
// https://javdb.com/v/82BkzE.
func (javdb *JavDB) ParseMovieIDFromURL(rawURL string) (string, error) {
	homepage, err := url.Parse(rawURL)
	if err != nil {
		return "", err
	}
	switch id := path.Base(strings.TrimSuffix(homepage.Path, "/")); id {
	case "", ".", "/":
		return "", nil
	default:
		return id, nil
	}
}

// GetMovieInfoByURL impls MovieProvider.GetMovieInfoByURL.
func (javdb *JavDB) GetMovieInfoByURL(rawURL string) (info *model.MovieInfo, err error) {
	id, err := javdb.ParseMovieIDFromURL(rawURL)
	if err != nil {
		return nil, err
	}
	if id == "" {
		return nil, provider.ErrInvalidURL
	}
	return javdb.GetMovieInfoByID(id)
}

// GetMovieInfoByID impls MovieProvider.GetMovieInfoByID.
//
// An internal id is fetched directly. Anything else is treated as a printed
// number and resolved through the search endpoint first, because the detail
// endpoint only accepts internal ids.
func (javdb *JavDB) GetMovieInfoByID(id string) (*model.MovieInfo, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, provider.ErrInvalidID
	}
	if looksLikeMovieID(id) {
		if info, err := javdb.getMovieInfoByMovieID(id); err == nil {
			return info, nil
		}
	}
	movieID, err := javdb.resolveMovieID(id)
	if err != nil {
		return nil, err
	}
	return javdb.getMovieInfoByMovieID(movieID)
}

// getMovieInfoByMovieID fetches and maps the detail endpoint for an internal
// JavDB movie id.
func (javdb *JavDB) getMovieInfoByMovieID(movieID string) (info *model.MovieInfo, err error) {
	c := javdb.ClonedCollector()

	c.OnResponse(func(r *colly.Response) {
		var resp movieResponse
		if e := json.Unmarshal(r.Body, &resp); e != nil {
			err = fmt.Errorf("javdb: failed to parse the movie response: %w", e)
			return
		}
		// Failures are delivered as HTTP 200 with success 0.
		if !bool(resp.Success) && resp.Action != "" {
			err = resp.apiError()
			return
		}
		if resp.Data.Movie == nil {
			err = provider.ErrInfoNotFound
			return
		}
		info, err = buildMovieInfo(resp.Data.Movie)
	})

	c.OnError(func(r *colly.Response, _ error) {
		if r == nil {
			return
		}
		switch r.StatusCode {
		case http.StatusNotFound:
			err = provider.ErrInfoNotFound
		case http.StatusUnauthorized, http.StatusForbidden, http.StatusTooManyRequests:
			err = fmt.Errorf("javdb: request rejected by the api (HTTP %d)", r.StatusCode)
		default:
			err = fmt.Errorf("javdb: unexpected http status %d", r.StatusCode)
		}
	})

	if e := javdb.do(c, fmt.Sprintf(apiMoviePath, url.PathEscape(movieID)), nil); e != nil {
		if err != nil {
			return nil, err
		}
		return nil, e
	}
	return info, err
}

// NormalizeMovieKeyword impls MovieSearcher.NormalizeMovieKeyword.
//
// The engine treats an empty result as "this provider cannot handle the
// keyword". JavDB indexes censored, uncensored and FC2 titles alike, so
// nothing is declined here and the keyword is only trimmed. Its case is left
// alone: number searches are case-insensitive, and a title or actor search
// should not be rewritten on the way in.
func (javdb *JavDB) NormalizeMovieKeyword(keyword string) string {
	return strings.TrimSpace(keyword)
}

// SearchMovie impls MovieSearcher.SearchMovie.
func (javdb *JavDB) SearchMovie(keyword string) (results []*model.MovieSearchResult, err error) {
	c := javdb.ClonedCollector()

	c.OnResponse(func(r *colly.Response) {
		var resp searchResponse
		if e := json.Unmarshal(r.Body, &resp); e != nil {
			err = fmt.Errorf("javdb: failed to parse the search response: %w", e)
			return
		}
		if !bool(resp.Success) && resp.Action != "" {
			err = resp.apiError()
			return
		}
		for _, m := range resp.Data.Movies {
			if m == nil || strings.TrimSpace(m.ID) == "" {
				continue
			}
			results = append(results, &model.MovieSearchResult{
				ID:          strings.TrimSpace(m.ID),
				Number:      strings.TrimSpace(m.Number),
				Title:       strings.TrimSpace(m.Title),
				Provider:    Name,
				Homepage:    fmt.Sprintf(moviePageURL, strings.TrimSpace(m.ID)),
				ThumbURL:    strings.TrimSpace(m.ThumbURL),
				CoverURL:    strings.TrimSpace(m.CoverURL),
				Score:       float64(m.Score),
				ReleaseDate: parser.ParseDate(m.ReleaseDate),
			})
		}
	})

	c.OnError(func(r *colly.Response, _ error) {
		if r == nil {
			return
		}
		switch r.StatusCode {
		case http.StatusNotFound:
			err = provider.ErrInfoNotFound
		case http.StatusUnauthorized, http.StatusForbidden, http.StatusTooManyRequests:
			err = fmt.Errorf("javdb: request rejected by the api (HTTP %d)", r.StatusCode)
		default:
			err = fmt.Errorf("javdb: unexpected http status %d", r.StatusCode)
		}
	})

	params := url.Values{}
	params.Set("q", keyword)
	params.Set("page", "1")
	if e := javdb.do(c, apiSearchPath, params); e != nil {
		if err != nil {
			return nil, err
		}
		return nil, e
	}
	return results, err
}

// resolveMovieID maps a printed number, e.g. "FC2-4925979", to the internal
// movie id that the detail endpoint expects.
//
// The search route is a keyword search, so its results can contain loosely
// related entries. A match is only accepted when it is unambiguous: an exact
// case-insensitive number match wins, otherwise a single result that is
// equivalent once separators and an FC2 prefix are ignored. A first hit is
// never picked, so an ambiguous keyword fails instead of returning the
// metadata of an unrelated movie.
func (javdb *JavDB) resolveMovieID(number string) (string, error) {
	results, err := javdb.SearchMovie(number)
	if err != nil {
		return "", err
	}

	var (
		movieID  string
		bestRank int
		conflict bool
	)
	for _, result := range results {
		if result == nil {
			continue
		}
		rank := matchRank(result.Number, number)
		switch {
		case rank == 0:
			continue
		case rank > bestRank:
			movieID, bestRank, conflict = result.ID, rank, false
		case rank == bestRank && result.ID != movieID:
			conflict = true
		}
	}
	if movieID == "" {
		return "", provider.ErrInfoNotFound
	}
	if conflict {
		return "", fmt.Errorf("javdb: ambiguous number %q matches several movies", number)
	}
	return movieID, nil
}

// matchRank scores how well a JavDB number matches the requested keyword.
// Higher is better, and 0 means "not a match". Ranks are ordered so that an
// exact match always beats a formatting-equivalent one.
func matchRank(number, want string) int {
	n := strings.ToUpper(strings.TrimSpace(number))
	w := strings.ToUpper(strings.TrimSpace(want))
	if n == "" || w == "" {
		return 0
	}
	switch {
	case n == w:
		return 3
	case compactNumber(n) == compactNumber(w):
		return 2
	default:
		return 0
	}
}

// compactNumber reduces a printed number to uppercase letters and digits and
// drops an FC2 prefix, so that a bare digits keyword ("4925979") matches the
// number JavDB prints for it ("FC2-4925979") and formatting-only differences
// such as separators are ignored.
func compactNumber(s string) string {
	var b strings.Builder
	for _, r := range strings.ToUpper(strings.TrimSpace(s)) {
		if (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	c := b.String()
	return strings.TrimPrefix(strings.TrimPrefix(c, "FC2PPV"), "FC2")
}

// buildMovieInfo maps a JavDB movie object to a *model.MovieInfo.
func buildMovieInfo(m *apiMovie) (*model.MovieInfo, error) {
	if m == nil {
		return nil, provider.ErrInfoNotFound
	}
	id := strings.TrimSpace(m.ID)
	if id == "" {
		return nil, provider.ErrInfoNotFound
	}

	// The API reports the runtime in minutes, which is the unit
	// model.MovieInfo.Runtime uses, and the date as "YYYY-MM-DD".
	info := &model.MovieInfo{
		ID:              id,
		Number:          strings.TrimSpace(m.Number),
		Provider:        Name,
		Homepage:        fmt.Sprintf(moviePageURL, id),
		Title:           strings.TrimSpace(m.Title),
		Summary:         strings.TrimSpace(m.Summary),
		ThumbURL:        strings.TrimSpace(m.ThumbURL),
		CoverURL:        strings.TrimSpace(m.CoverURL),
		Score:           float64(m.Score),
		Maker:           strings.TrimSpace(m.MakerName),
		Director:        strings.TrimSpace(m.DirectorName),
		Series:          strings.TrimSpace(m.SeriesName),
		Runtime:         int(m.Duration),
		ReleaseDate:     parser.ParseDate(m.ReleaseDate),
		PreviewVideoURL: strings.TrimSpace(m.PreviewVideoURL),
		PreviewImages:   []string{},
		Actors:          []string{},
		Genres:          []string{},
	}
	// MetaTube rejects metadata without a number or a title
	// (model.MovieInfo.IsValid), so fall back to the id and the original
	// title rather than dropping an otherwise complete entry.
	if info.Number == "" {
		info.Number = info.ID
	}
	if info.Title == "" {
		info.Title = strings.TrimSpace(m.OriginTitle)
	}

	for _, name := range m.Actors {
		if n := strings.TrimSpace(name); n != "" {
			info.Actors = append(info.Actors, n)
		}
	}
	for _, name := range m.Tags {
		if n := strings.TrimSpace(name); n != "" {
			info.Genres = append(info.Genres, n)
		}
	}
	for _, u := range m.PreviewImages {
		if s := strings.TrimSpace(u); s != "" {
			info.PreviewImages = append(info.PreviewImages, s)
		}
	}

	return info, nil
}

// do performs a signed GET request against the JavDB app API.
//
// Every request carries the official Android app's identification (channel,
// version, platform, device) as query parameters, plus the time-based
// jdsignature header and the Flutter/Dart User-Agent the app itself sends.
// No account or cookie is involved: the signature is the only credential,
// and it is derived from the current time.
func (javdb *JavDB) do(c *colly.Collector, endpoint string, params url.Values) error {
	q := appIdentity()
	for key, values := range params {
		for _, value := range values {
			q.Add(key, value)
		}
	}
	requestURL := apiHost + endpoint + "?" + q.Encode()

	headers := http.Header{}
	headers.Set("jdsignature", jdSignature(time.Now().Unix()))
	headers.Set("accept", "application/json")
	headers.Set("accept-language", appAcceptLanguage)
	// The header map replaces the collector's, so the Flutter/Dart UA must
	// be set explicitly; otherwise colly would fill in a randomised browser
	// agent that the API has never been shown to accept.
	headers.Set("user-agent", appUserAgent)
	return c.Request(http.MethodGet, requestURL, nil, nil, headers)
}

func init() {
	provider.Register(Name, New)
}
