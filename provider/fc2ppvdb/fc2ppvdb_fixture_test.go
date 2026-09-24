package fc2ppvdb

import (
	"encoding/json"
	"fmt"
	"html"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/metatube-community/metatube-sdk-go/provider"
)

// makePageHTML wraps an Inertia page JSON document in a minimal HTML document
// that mimics the production page layout: the JSON is stored, HTML-escaped, in
// the data-page attribute of the #app element, and <body> carries its own
// non-JSON data-page attribute holding the Laravel route name.
func makePageHTML(t *testing.T, pageJSON string) []byte {
	t.Helper()
	return []byte(fmt.Sprintf(
		`<!DOCTYPE html><html lang="ja"><head><meta charset="utf-8">`+
			`<title>4925979 Sample - FC2CMADB</title></head>`+
			`<body data-page="articles.show">`+
			`<div id="app" data-page="%s"></div>`+
			`<script type="module" src="/build/assets/app-BkK2ozc3.js"></script>`+
			`</body></html>`,
		html.EscapeString(pageJSON),
	))
}

// baseArticleJSON is a realistic Articles/Show page state, with field names
// verified against the production frontend bundle of fc2cmadb.com.
const baseArticleJSON = `{
  "component": "Articles/Show",
  "props": {
    "article": {
      "id": 1045543,
      "video_id": "4925979",
      "title": "Sample Title 作品",
      "image_url": "/storage/images/article/4925979.jpg",
      "not_found": false,
      "censored": "無",
      "release_date": "2026-06-26",
      "duration": "01:25:07",
      "writer": {"id": 100, "name": "Sample Writer", "slug": "ptpt", "uncensored_image": 0},
      "tags": [{"id": 1, "name": "素人"}, {"id": 2, "name": "美乳"}],
      "affiliate_links": null,
      "sale_limited_date": null,
      "sale_percentage": null
    },
    "actresses": null,
    "comments": {"count": 0, "data": []},
    "reacter": null,
    "auth": {"user": null},
    "locale": {"language": "ja"}
  },
  "url": "/articles/4925979",
  "version": "6e099b2303f883846b05a489300609e2"
}`

func parsePage(t *testing.T, body []byte) *inertiaPage {
	t.Helper()
	dataPage, err := extractDataPage(body)
	require.NoError(t, err)
	var page inertiaPage
	require.NoError(t, json.Unmarshal([]byte(dataPage), &page))
	return &page
}

func TestExtractDataPage(t *testing.T) {
	body := makePageHTML(t, baseArticleJSON)

	dataPage, err := extractDataPage(body)
	require.NoError(t, err)
	// Regression: the <body data-page="articles.show"> route name must never
	// be returned; the value must always be the JSON page state.
	assert.NotEqual(t, "articles.show", dataPage)
	var v map[string]any
	require.NoError(t, json.Unmarshal([]byte(dataPage), &v))
	assert.Equal(t, "Articles/Show", v["component"])

	// Missing attribute.
	_, err = extractDataPage([]byte("<html><body><div id=\"app\"></div></body></html>"))
	assert.Error(t, err)

	// Invalid HTML.
	_, err = extractDataPage([]byte{0xff, 0xfe, 0xfd})
	assert.Error(t, err)
}

func TestExtractDataPage_SkipsNonJSONDataPage(t *testing.T) {
	const escaped = `{&quot;component&quot;:&quot;Articles/Show&quot;,&quot;props&quot;:{},&quot;url&quot;:&quot;/articles/4925979&quot;}`

	for name, tt := range map[string]struct {
		html string
		want string // expected JSON, empty means "expect an error"
	}{
		// Production layout: <body> comes first in document order.
		"body route name first": {
			html: `<html><body data-page="articles.show"><div id="app" data-page="` + escaped + `"></div></body></html>`,
			want: `{"component":"Articles/Show","props":{},"url":"/articles/4925979"}`,
		},
		// Only the route name is present (e.g. an error/bot page).
		"only route name": {
			html: `<html><body data-page="articles.show"><div id="app"></div></body></html>`,
		},
		// The app root has no data-page; fall back to another element.
		"json on non-app element": {
			html: `<html><body data-page="articles.show"><div id="app"></div>` +
				`<div id="inertia-root" data-page="` + escaped + `"></div></body></html>`,
			want: `{"component":"Articles/Show","props":{},"url":"/articles/4925979"}`,
		},
		// The app root carries the route name instead; fall back to the JSON.
		"app root holds route name": {
			html: `<html><body><div id="app" data-page="articles.show"></div>` +
				`<div id="inertia-root" data-page="` + escaped + `"></div></body></html>`,
			want: `{"component":"Articles/Show","props":{},"url":"/articles/4925979"}`,
		},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := extractDataPage([]byte(tt.html))
			if tt.want == "" {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
			// The returned value must be usable as JSON.
			var v map[string]any
			require.NoError(t, json.Unmarshal([]byte(got), &v))
			assert.Equal(t, "Articles/Show", v["component"])
		})
	}
}

func TestBuildMovieInfo(t *testing.T) {
	page := parsePage(t, makePageHTML(t, baseArticleJSON))

	info, err := buildMovieInfo(page, "https://fc2cmadb.com/articles/4925979")
	require.NoError(t, err)

	assert.Equal(t, "4925979", info.ID)
	assert.Equal(t, "FC2-4925979", info.Number)
	assert.Equal(t, Name, info.Provider)
	assert.Equal(t, "https://fc2cmadb.com/articles/4925979", info.Homepage)
	assert.Equal(t, "Sample Title 作品", info.Title)
	assert.Equal(t, "Sample Writer", info.Maker)
	assert.Equal(t, "https://fc2cmadb.com/storage/images/article/4925979.jpg", info.CoverURL)
	assert.Equal(t, 85, info.Runtime) // 01:25:07
	assert.Equal(t, 2026, time.Time(info.ReleaseDate).Year())
	assert.Equal(t, time.Month(6), time.Time(info.ReleaseDate).Month())
	assert.Equal(t, 26, time.Time(info.ReleaseDate).Day())
	assert.ElementsMatch(t, []string{"素人", "美乳"}, info.Genres)
	assert.Empty(t, info.Actors)
	assert.True(t, info.IsValid())
}

func TestBuildMovieInfo_NumberVideoID(t *testing.T) {
	// The server may also send video_id as a JSON number.
	pageJSON := strings.Replace(baseArticleJSON, `"video_id": "4925979"`, `"video_id": 4925979`, 1)
	page := parsePage(t, makePageHTML(t, pageJSON))

	info, err := buildMovieInfo(page, "https://fc2cmadb.com/articles/4925979")
	require.NoError(t, err)
	assert.Equal(t, "4925979", info.ID)
	assert.Equal(t, "FC2-4925979", info.Number)
}

func TestBuildMovieInfo_PlaceholderImage(t *testing.T) {
	// Videos without a custom cover use the site's "no image" placeholder.
	// It is reported as the cover because MetaTube discards metadata whose
	// CoverURL is empty (model.MovieInfo.IsValid).
	pageJSON := strings.Replace(baseArticleJSON,
		`"image_url": "/storage/images/article/4925979.jpg"`,
		`"image_url": "/storage/images/article/no-image.jpg"`, 1)
	page := parsePage(t, makePageHTML(t, pageJSON))

	info, err := buildMovieInfo(page, "https://fc2cmadb.com/articles/4925979")
	require.NoError(t, err)
	assert.Equal(t, "https://fc2cmadb.com/storage/images/article/no-image.jpg", info.CoverURL)
	assert.True(t, info.IsValid())
}

func TestBuildMovieInfo_MissingImage(t *testing.T) {
	// No image_url at all: fall back to the placeholder so the metadata is
	// still considered valid.
	pageJSON := strings.Replace(baseArticleJSON,
		`"image_url": "/storage/images/article/4925979.jpg"`,
		`"image_url": null`, 1)
	page := parsePage(t, makePageHTML(t, pageJSON))

	info, err := buildMovieInfo(page, "https://fc2cmadb.com/articles/4925979")
	require.NoError(t, err)
	assert.Equal(t, defaultCoverURL, info.CoverURL)
	assert.True(t, info.IsValid())
}

func TestBuildMovieInfo_NotFound(t *testing.T) {
	// Deleted FC2 videos are still listed by the database (with a
	// not_found flag); their metadata should still be returned.
	pageJSON := strings.Replace(baseArticleJSON, `"not_found": false`, `"not_found": true`, 1)
	page := parsePage(t, makePageHTML(t, pageJSON))

	info, err := buildMovieInfo(page, "https://fc2cmadb.com/articles/4925979")
	require.NoError(t, err)
	assert.Equal(t, "Sample Title 作品", info.Title)
	assert.True(t, info.IsValid())
}

func TestBuildMovieInfo_WithActresses(t *testing.T) {
	pageJSON := strings.Replace(baseArticleJSON, `"actresses": null,`,
		`"actresses": [{"id": 1, "name": "Sample Actress A"}, {"id": 2, "name": "Sample Actress B"}],`, 1)
	page := parsePage(t, makePageHTML(t, pageJSON))

	info, err := buildMovieInfo(page, "https://fc2cmadb.com/articles/4925979")
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"Sample Actress A", "Sample Actress B"}, info.Actors)
}

func TestBuildMovieInfo_NoArticle(t *testing.T) {
	// A page without the article prop (e.g. an error page).
	pageJSON := `{"component":"Error","props":{"errors":{"message":"Not found"}},"url":"/articles/1","version":"x"}`
	page := parsePage(t, makePageHTML(t, pageJSON))

	_, err := buildMovieInfo(page, "https://fc2cmadb.com/articles/1")
	assert.ErrorIs(t, err, provider.ErrInfoNotFound)
}

func TestFlexString(t *testing.T) {
	for name, tt := range map[string]struct {
		json string
		want string
		err  bool
	}{
		"string":  {`"4925979"`, "4925979", false},
		"number":  {`4925979`, "4925979", false},
		"negnum":  {`-1`, "-1", false},
		"null":    {`null`, "", false},
		"boolean": {`true`, "", true},
	} {
		t.Run(name, func(t *testing.T) {
			var s flexString
			err := json.Unmarshal([]byte(tt.json), &s)
			if tt.err {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, string(s))
		})
	}
}

func TestNormalizeMovieID(t *testing.T) {
	p := New()
	assert.Equal(t, "4925979", p.NormalizeMovieID("4925979"))
	assert.Equal(t, "4925979", p.NormalizeMovieID("FC2-4925979"))
	assert.Equal(t, "4925979", p.NormalizeMovieID("FC2PPV-4925979"))
	assert.Empty(t, p.NormalizeMovieID("invalid"))
}

func TestParseMovieIDFromURL(t *testing.T) {
	p := New()
	id, err := p.ParseMovieIDFromURL("https://fc2cmadb.com/articles/4925979")
	require.NoError(t, err)
	assert.Equal(t, "4925979", id)

	// Legacy domain homepage (cached by older versions).
	id, err = p.ParseMovieIDFromURL("https://fc2ppvdb.com/articles/4925979")
	require.NoError(t, err)
	assert.Equal(t, "4925979", id)
}
