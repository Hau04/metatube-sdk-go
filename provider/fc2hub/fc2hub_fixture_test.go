package fc2hub

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/text/language"

	"github.com/metatube-community/metatube-sdk-go/provider/internal/scraper"
)

// hostRewriter sends every request to the test server while keeping the
// original javten.com URL on the request colly sees.
type hostRewriter struct{ target *url.URL }

func (h hostRewriter) RoundTrip(req *http.Request) (*http.Response, error) {
	r := req.Clone(req.Context())
	r.URL.Scheme = h.target.Scheme
	r.URL.Host = h.target.Host
	r.Host = h.target.Host
	return http.DefaultTransport.RoundTrip(r)
}

func newFixtureProvider(t *testing.T, handler http.HandlerFunc) (*FC2HUB, func() []string) {
	t.Helper()
	var (
		mu    sync.Mutex
		paths []string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.URL.RequestURI())
		mu.Unlock()
		handler(w, r)
	}))
	t.Cleanup(srv.Close)
	target, _ := url.Parse(srv.URL)
	p := &FC2HUB{scraper.NewDefaultScraper(Name, baseURL, Priority, language.Japanese,
		scraper.WithTransport(hostRewriter{target}))}
	return p, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), paths...)
	}
}

func serveFile(t *testing.T, name string) http.HandlerFunc {
	body, err := os.ReadFile(name)
	require.NoError(t, err)
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(body)
	}
}

func assertResult4438561(t *testing.T, p *FC2HUB, keyword string) {
	t.Helper()
	kw := p.NormalizeMovieKeyword(keyword)
	require.Equal(t, "4438561", kw)
	results, err := p.SearchMovie(kw)
	require.NoError(t, err)
	require.Len(t, results, 1)
	r := results[0]
	assert.Equal(t, "FC2-4438561", r.Number)
	assert.Equal(t, "1818987-4438561", r.ID)
	assert.Equal(t, "17. Mio-chan Blow Swallowing Thanksgiving!! At the end, I got a **** and smiled!", r.Title)
	assert.Equal(t, "https://cdn.javten.example/uploads/fc2/4438561/cover.jpg", r.CoverURL)
	assert.True(t, r.IsValid())
}

func TestSearchMovie_RedirectToEnglishPage(t *testing.T) {
	video := serveFile(t, "testdata/video_4438561.html")
	for _, kw := range []string{"FC2PPV-4438561", "FC2-PPV-4438561", "FC2-4438561", "4438561"} {
		t.Run(kw, func(t *testing.T) {
			p, _ := newFixtureProvider(t, func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/search":
					assert.Equal(t, "4438561", r.URL.Query().Get("kw"))
					http.Redirect(w, r, "https://javten.com/en/video/1818987/id4438561/", http.StatusFound)
				case "/en/video/1818987/id4438561/":
					video(w, r)
				default:
					http.NotFound(w, r)
				}
			})
			assertResult4438561(t, p, kw)
		})
	}
}

func TestSearchMovie_ResultsPageLink(t *testing.T) {
	video := serveFile(t, "testdata/video_4438561.html")
	p, _ := newFixtureProvider(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/search":
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte(`<html><head><meta http-equiv="refresh" content="0;url=/en/video/1818987/id4438561/"></head>
<body><a href="/en/video/5/id1234567/">other</a><a href="/en/video/1818987/id4438561/">hit</a></body></html>`))
		case "/en/video/1818987/id4438561/":
			video(w, r)
		default:
			http.NotFound(w, r)
		}
	})
	assertResult4438561(t, p, "FC2PPV-4438561")
}

func TestSearchMovie_NotFound(t *testing.T) {
	p, _ := newFixtureProvider(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`<html><body>No results</body></html>`))
	})
	_, err := p.SearchMovie("4438561")
	require.Error(t, err)
}

func TestGetMovieInfoByURL_LDJSONFlexibleFields(t *testing.T) {
	p, _ := newFixtureProvider(t, serveFile(t, "testdata/video_ldjson.html"))
	info, err := p.GetMovieInfoByURL("https://javten.com/en/video/1818987/id4438561/")
	require.NoError(t, err)
	assert.Equal(t, "FC2-4438561", info.Number)
	assert.Equal(t, "LD JSON Title", info.Title)
	assert.Equal(t, "https://javten.com/img/c.jpg", info.CoverURL)
	assert.Equal(t, []string{"Mio"}, info.Actors)
	assert.Equal(t, []string{"Amateur"}, info.Genres)
	assert.Equal(t, "Seller", info.Maker)
	assert.Equal(t, "https://javten.com/video/1818987/id4438561/LD%20JSON%20Title", info.Homepage)
}

func TestCleanTitle(t *testing.T) {
	assert.Equal(t, "Title here", cleanTitle("[FC2-PPV-4438561]Title here - JAVten.com"))
	assert.Equal(t, "Title here", cleanTitle("FC2-PPV-4438561 Title here"))
	assert.True(t, isNumberOnly("FC2-PPV-4438561"))
	assert.False(t, isNumberOnly("Title"))
}
