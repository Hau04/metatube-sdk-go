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

// makePageHTML wraps an Inertia page JSON document in the HTML document layout
// observed on fc2cmadb.com production (Inertia.js v2): the complete page state
// is the *text content* of <script data-page="app" type="application/json">,
// and <body> carries its own non-JSON data-page attribute holding the Laravel
// route name.
//
// The script body is raw JSON, not HTML-escaped, because an HTML <script>
// element is parsed in the raw-text state and does not decode entities.
func makePageHTML(t *testing.T, pageJSON string) []byte {
	t.Helper()
	return []byte(fmt.Sprintf(
		`<!DOCTYPE html><html lang="ja"><head><meta charset="utf-8">`+
			`<title>4925979 Sample - FC2CMADB</title></head>`+
			`<body data-page="articles.show">`+
			`<script data-page="app" type="application/json">%s</script>`+
			`<div id="app"></div>`+
			`<script type="module" src="/build/assets/app-BkK2ozc3.js"></script>`+
			`</body></html>`,
		pageJSON,
	))
}

// makePageHTMLV1 builds the legacy Inertia.js v1 layout, where the page state
// is the HTML-escaped data-page *attribute* of the #app root element. Pages
// rendered before the upgrade, and any cached response, still use it.
func makePageHTMLV1(t *testing.T, pageJSON string) []byte {
	t.Helper()
	return []byte(fmt.Sprintf(
		`<!DOCTYPE html><html lang="ja"><head><meta charset="utf-8">`+
			`<title>4925979 Sample - FC2CMADB</title></head>`+
			`<body data-page="articles.show">`+
			`<div id="app" data-page="%s"></div>`+
			`</body></html>`,
		html.EscapeString(pageJSON),
	))
}

// baseArticleJSON is a realistic Articles/Show page state. Field names, JSON
// types and the surrounding shared props were captured from a live
// fc2cmadb.com response (article 4925979, fc2cmadb.com deploy of 2026-09-24).
//
// Note the details that differ from the pre-v2 site and that the parser has to
// tolerate: video_id is a JSON number, not_found is null rather than false,
// image_url is an absolute contents-thumbnail2.fc2.com URL rather than a
// relative /storage/images path, tags carry a pivot object, and the actresses
// prop is absent because it is deferred to a follow-up request.
const baseArticleJSON = `{
  "component": "Articles/Show",
  "props": {
    "appName": "FC2CMADB",
    "appEnv": "production",
    "errors": {},
    "auth": {"user": null, "notifications": []},
    "locale": "ja",
    "language": {"Comment": "コメント"},
    "article": {
      "id": 844496,
      "title": "50％OFF【W杯決勝T進出記念】何の繋がりもない女を３人集めて乱パ！！！",
      "video_id": 4925979,
      "censored": "無",
      "uncensored_image": null,
      "not_found": null,
      "status": null,
      "release_date": "2026-06-26",
      "duration": "01:25:07",
      "writer_id": 2963,
      "image_url": "https://contents-thumbnail2.fc2.com/w276/storage201000.contents.fc2.com/file/385/38454154/1782198158.36.png",
      "sale_percentage": 50,
      "sale_limite_date": "2026-06-29 23:59:59",
      "love_reactant_id": 368253,
      "bookmark_count": 15,
      "like_count": 9,
      "dislike_count": 1,
      "model_name": "article",
      "writer": {"id": 2963, "slug": "ptpt", "name": "推しの素人", "uncensored_image": 2},
      "tags": [
        {"name": "ハメ撮り", "pivot": {"article_id": 844496, "tag_id": 10}},
        {"name": "素人", "pivot": {"article_id": 844496, "tag_id": 2}},
        {"name": "美乳", "pivot": {"article_id": 844496, "tag_id": 125}}
      ],
      "affiliate_links": []
    },
    "comments": {"data": [], "count": 10},
    "reacter": {},
    "honeypot": {"enabled": true, "nameFieldName": "my_name_t7oMen3yf6TEhinV"}
  },
  "url": "/articles/4925979",
  "version": "fcb3b524d4c7f8f3d2c38e437b35b7a9",
  "sharedProps": ["appName", "appEnv", "errors", "auth", "locale", "language"],
  "deferredProps": {"default": ["actresses"]}
}`

func parsePage(t *testing.T, body []byte) *inertiaPage {
	t.Helper()
	dataPage, err := extractDataPage(body)
	require.NoError(t, err)
	var page inertiaPage
	require.NoError(t, json.Unmarshal([]byte(dataPage), &page))
	return &page
}

// TestExtractDataPage covers the production (Inertia.js v2) layout, where the
// page state is the text content of <script data-page="app">.
func TestExtractDataPage(t *testing.T) {
	body := makePageHTML(t, baseArticleJSON)

	dataPage, err := extractDataPage(body)
	require.NoError(t, err)
	// Regression guards: neither the script's data-page id ("app") nor the
	// <body> route name may be mistaken for the page state.
	assert.NotEqual(t, "app", dataPage)
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

// TestExtractDataPage_InertiaV1 keeps the legacy attribute layout working.
func TestExtractDataPage_InertiaV1(t *testing.T) {
	body := makePageHTMLV1(t, baseArticleJSON)

	dataPage, err := extractDataPage(body)
	require.NoError(t, err)
	// The state sits in an attribute here, so it arrives HTML-escaped: entity
	// decoding is what turns it into a JSON document, and nothing may be left
	// escaped once it has been extracted.
	assert.NotContains(t, dataPage, "&quot;")
	var v map[string]any
	require.NoError(t, json.Unmarshal([]byte(dataPage), &v))
	assert.Equal(t, "Articles/Show", v["component"])
}

// TestExtractDataPage_InertiaV1OnNonAppElement covers a v1 page whose app root
// was renamed but which still embeds the state in a data-page attribute.
func TestExtractDataPage_InertiaV1OnNonAppElement(t *testing.T) {
	const escaped = `{&quot;component&quot;:&quot;Articles/Show&quot;,&quot;props&quot;:{},&quot;url&quot;:&quot;/articles/4925979&quot;}`
	body := []byte(`<html><body data-page="articles.show"><div id="app"></div>` +
		`<div id="inertia-root" data-page="` + escaped + `"></div></body></html>`)

	got, err := extractDataPage(body)
	require.NoError(t, err)
	assert.Equal(t, `{"component":"Articles/Show","props":{},"url":"/articles/4925979"}`, got)
}

// TestExtractDataPage_SkipsNonJSONDataPage covers the candidates that must be
// rejected rather than handed to json.Unmarshal.
func TestExtractDataPage_SkipsNonJSONDataPage(t *testing.T) {
	// The JSON below is deliberately given as a raw <script> body (v2 style),
	// which is what production serves.
	const jsonBody = `{"component":"Articles/Show","props":{},"url":"/articles/4925979"}`
	// The same document in v1 style, HTML-escaped inside an attribute.
	escaped := strings.ReplaceAll(jsonBody, `"`, "&quot;")

	for name, tt := range map[string]struct {
		html string
		want string // expected JSON, empty means "expect an error"
	}{
		// Production layout: the <body> route name comes first in document
		// order, so a naive first-match search returns a non-JSON string.
		"body route name first, v2 script": {
			html: `<html><body data-page="articles.show"><div id="app"></div>` +
				`<script data-page="app" type="application/json">` + jsonBody + `</script></body></html>`,
			want: jsonBody,
		},
		// Only the route name is present (e.g. an error/bot page).
		"only route name": {
			html: `<html><body data-page="articles.show"><div id="app"></div></body></html>`,
		},
		// A script that carries data-page but whose text is not JSON.
		"script with non-JSON text": {
			html: `<html><body data-page="articles.show">` +
				`<script data-page="app" type="application/json">app</script></body></html>`,
		},
		// The app root carries the route name instead; the script still has it.
		"app root holds route name, v2 script": {
			html: `<html><body><div id="app" data-page="articles.show"></div>` +
				`<script data-page="app" type="application/json">` + jsonBody + `</script></body></html>`,
			want: jsonBody,
		},
		// v1 attribute state is found even when a v2-style script is absent.
		"v1 attribute wins when only it holds JSON": {
			html: `<html><body data-page="articles.show"><div id="app" data-page="` + escaped + `"></div></body></html>`,
			want: jsonBody,
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
	assert.Equal(t, "50％OFF【W杯決勝T進出記念】何の繋がりもない女を３人集めて乱パ！！！", info.Title)
	assert.Equal(t, "推しの素人", info.Maker)
	// image_url is an absolute CDN URL on the current site.
	assert.Equal(t,
		"https://contents-thumbnail2.fc2.com/w276/storage201000.contents.fc2.com/file/385/38454154/1782198158.36.png",
		info.CoverURL)
	assert.Equal(t, 85, info.Runtime) // 01:25:07
	assert.Equal(t, 2026, time.Time(info.ReleaseDate).Year())
	assert.Equal(t, time.Month(6), time.Time(info.ReleaseDate).Month())
	assert.Equal(t, 26, time.Time(info.ReleaseDate).Day())
	assert.ElementsMatch(t, []string{"ハメ撮り", "素人", "美乳"}, info.Genres)
	// actresses is a deferred Inertia prop, so it is absent for anonymous
	// sessions and no actors can be reported.
	assert.Empty(t, info.Actors)
	assert.True(t, info.IsValid())
}

// TestBuildMovieInfo_ProductionVideoID pins the exact production detail that
// the live page sends video_id as a JSON number (4925979, not "4925979").
func TestBuildMovieInfo_ProductionVideoID(t *testing.T) {
	require.Contains(t, baseArticleJSON, `"video_id": 4925979`)
	page := parsePage(t, makePageHTML(t, baseArticleJSON))

	info, err := buildMovieInfo(page, "https://fc2cmadb.com/articles/4925979")
	require.NoError(t, err)
	assert.Equal(t, "4925979", info.ID)
	assert.Equal(t, "FC2-4925979", info.Number)
	// The numeric row id is only a fallback and must not shadow video_id.
	assert.NotEqual(t, "844496", info.ID)
}

func TestBuildMovieInfo_StringVideoID(t *testing.T) {
	// The server has sent video_id as a JSON string in other deploys.
	pageJSON := strings.Replace(baseArticleJSON, `"video_id": 4925979`, `"video_id": "4925979"`, 1)
	page := parsePage(t, makePageHTML(t, pageJSON))

	info, err := buildMovieInfo(page, "https://fc2cmadb.com/articles/4925979")
	require.NoError(t, err)
	assert.Equal(t, "4925979", info.ID)
	assert.Equal(t, "FC2-4925979", info.Number)
}

// TestBuildMovieInfo_NotFound covers both the null flag the live site sends and
// the literal true used by deleted entries: deleted FC2 videos are still listed
// by the database, and their metadata should still be returned.
func TestBuildMovieInfo_NotFound(t *testing.T) {
	assert.Contains(t, baseArticleJSON, `"not_found": null`)
	page := parsePage(t, makePageHTML(t, baseArticleJSON))
	info, err := buildMovieInfo(page, "https://fc2cmadb.com/articles/4925979")
	require.NoError(t, err)
	assert.True(t, info.IsValid())

	pageJSON := strings.Replace(baseArticleJSON, `"not_found": null`, `"not_found": true`, 1)
	page = parsePage(t, makePageHTML(t, pageJSON))
	info, err = buildMovieInfo(page, "https://fc2cmadb.com/articles/4925979")
	require.NoError(t, err)
	assert.Equal(t, "50％OFF【W杯決勝T進出記念】何の繋がりもない女を３人集めて乱パ！！！", info.Title)
	assert.True(t, info.IsValid())
}

func TestBuildMovieInfo_PlaceholderImage(t *testing.T) {
	// Videos without a custom cover may still be served the site's relative
	// "no image" placeholder; it is reported as the cover because MetaTube
	// discards metadata whose CoverURL is empty (model.MovieInfo.IsValid).
	pageJSON := strings.Replace(baseArticleJSON,
		`"image_url": "https://contents-thumbnail2.fc2.com/w276/storage201000.contents.fc2.com/file/385/38454154/1782198158.36.png"`,
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
		`"image_url": "https://contents-thumbnail2.fc2.com/w276/storage201000.contents.fc2.com/file/385/38454154/1782198158.36.png"`,
		`"image_url": null`, 1)
	page := parsePage(t, makePageHTML(t, pageJSON))

	info, err := buildMovieInfo(page, "https://fc2cmadb.com/articles/4925979")
	require.NoError(t, err)
	assert.Equal(t, defaultCoverURL, info.CoverURL)
	assert.True(t, info.IsValid())
}

func TestBuildMovieInfo_WithActresses(t *testing.T) {
	// actresses is normally deferred, but an eager response should still be
	// mapped.
	pageJSON := strings.Replace(baseArticleJSON, `"comments": {"data": [], "count": 10},`,
		`"actresses": [{"id": 1, "name": "Sample Actress A"}, {"id": 2, "name": "Sample Actress B"}],`+
			`"comments": {"data": [], "count": 10},`, 1)
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
