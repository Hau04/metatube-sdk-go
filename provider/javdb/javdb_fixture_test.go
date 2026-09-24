package javdb

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/metatube-community/metatube-sdk-go/provider"
)

func TestMain(m *testing.M) {
	// Keep rewrites off the network for the whole package. Tests that need a
	// cold cache clear it themselves.
	setWebImagePrefix(fallbackWebImagePrefix)
	os.Exit(m.Run())
}

// detailFixture mirrors a GET /api/v4/movies/82BkzE response: actors and tags
// are objects carrying a "name", duration and score are numbers, and unset
// fields arrive as null.
const detailFixture = `{
  "success": 1,
  "action": null,
  "message": null,
  "data": {
    "movie": {
      "id": "82BkzE",
      "type": 3,
      "number": "FC2-4925979",
      "number_letter": "FC2-4925979",
      "title": "サンプル作品タイトル",
      "origin_title": "サンプル作品タイトル (origin)",
      "summary": "サンプル説明文。",
      "thumb_url": "https://c0.jdbstatic.com/thumbs/82/82BkzE.jpg",
      "cover_url": "https://c0.jdbstatic.com/covers/82/82BkzE.jpg",
      "has_cnsub": false,
      "has_preview_images": true,
      "duration": 85,
      "score": 3.63,
      "reviews_count": 17,
      "magnets_count": 5,
      "has_preview_video": true,
      "can_play": true,
      "release_date": "2026-06-26",
      "maker_id": 1,
      "maker_name": "FC2",
      "director_id": null,
      "director_name": "",
      "series_id": null,
      "series_name": "",
      "tags": [{"id": 1, "name": "素人"}, {"id": 2, "name": "美乳"}],
      "actors": [{"id": "actor-1", "name": "サンプル女優"}],
      "preview_video_url": "https://example.com/preview.mp4",
      "preview_images": ["https://c0.jdbstatic.com/samples/82/82BkzE_1.jpg"],
      "relative_movies": [],
      "actor_movies": []
    }
  }
}`

// searchFixture returns one loosely related entry ("4925979", which is only
// equivalent once separators and the FC2 prefix are ignored) alongside the
// exact match, so ranking has to prefer the latter.
const searchFixture = `{
  "success": 1,
  "action": null,
  "message": null,
  "data": {
    "movies": [
      {"id": "aaaaa1", "number": "4925979", "title": "別の作品",
       "thumb_url": "https://c0.jdbstatic.com/thumbs/aa/aaaaa1.jpg",
       "cover_url": "https://c0.jdbstatic.com/covers/aa/aaaaa1.jpg",
       "duration": 10, "score": "2.00", "release_date": "2020-01-01"},
      {"id": "82BkzE", "number": "FC2-4925979", "title": "サンプル作品タイトル",
       "thumb_url": "https://c0.jdbstatic.com/thumbs/82/82BkzE.jpg",
       "cover_url": "https://c0.jdbstatic.com/covers/82/82BkzE.jpg",
       "duration": 85, "score": "3.63", "release_date": "2026-06-26"}
    ]
  }
}`

// searchFixtureFC2Only carries the FC2 entry alone, so a bare digits keyword
// can only be resolved through the FC2-prefix equivalence.
const searchFixtureFC2Only = `{
  "success": 1,
  "action": null,
  "message": null,
  "data": {
    "movies": [
      {"id": "82BkzE", "number": "FC2-4925979", "title": "サンプル作品タイトル",
       "thumb_url": "https://c0.jdbstatic.com/thumbs/82/82BkzE.jpg",
       "cover_url": "https://c0.jdbstatic.com/covers/82/82BkzE.jpg",
       "duration": 85, "score": 3.63, "release_date": "2026-06-26"}
    ]
  }
}`

// searchFixtureAmbiguous holds two equally strong, differently identified
// matches: resolution must refuse rather than pick the first hit.
const searchFixtureAmbiguous = `{
  "success": 1,
  "action": null,
  "message": null,
  "data": {
    "movies": [
      {"id": "aaaaa1", "number": "SONE-123", "title": "A"},
      {"id": "bbbbb2", "number": "SONE-123", "title": "B"}
    ]
  }
}`

const searchFixtureEmpty = `{"success": 1, "action": null, "message": null, "data": {"movies": []}}`

// errFixture is a failure envelope. The API reports these with HTTP 200.
const errFixture = `{"success": 0, "action": "InvalidSignature", "message": "Invalid signature", "data": null}`

// apiStub is a local stand-in for the JavDB app API. It records every request
// so tests can assert on the paths, query parameters and headers the provider
// actually sent. Recorded state is mutex-guarded because the handler runs on
// the server's goroutine while the test goroutine reads it back.
type apiStub struct {
	search        string
	detail        string
	startup       string
	detailStatus  int
	startupStatus int

	mu      sync.Mutex
	paths   []string
	queries []url.Values
	headers []http.Header
}

// start serves the stub and redirects the provider at it for the duration of
// the test.
func (s *apiStub) start(t *testing.T) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.paths = append(s.paths, r.URL.Path)
		s.queries = append(s.queries, r.URL.Query())
		s.headers = append(s.headers, r.Header.Clone())
		s.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case apiSearchPath:
			_, _ = io.WriteString(w, s.search)
		case fmt.Sprintf(apiMoviePath, "82BkzE"):
			if s.detailStatus != 0 {
				w.WriteHeader(s.detailStatus)
				return
			}
			_, _ = io.WriteString(w, s.detail)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	// t.Cleanup runs in LIFO order, so the host is restored before the
	// server is torn down.
	t.Cleanup(srv.Close)
	original := apiHost
	apiHost = srv.URL
	t.Cleanup(func() { apiHost = original })
}

func (s *apiStub) detailPath() string {
	return fmt.Sprintf(apiMoviePath, "82BkzE")
}

func (s *apiStub) recordedPaths() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string{}, s.paths...)
}

func (s *apiStub) recordedCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.paths)
}

func (s *apiStub) recordedQueryGet(i int, key string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.queries[i].Get(key)
}

func (s *apiStub) recordedHeaderGet(i int, key string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.headers[i].Get(key)
}

// TestJDSignature pins the signature algorithm against a golden vector. The
// expected value was reproduced independently from the published algorithm.
func TestJDSignature(t *testing.T) {
	const want = "1784134914.lpw6vgqzsp.85b53cc0034eff62f361723615a3b8e3"
	assert.Equal(t, want, jdSignature(1784134914))
}

// TestJDSignatureFormat checks the header shape and that the timestamp tracks
// the clock, since the API rejects stale signatures.
func TestJDSignatureFormat(t *testing.T) {
	before := time.Now().Unix()
	sig := jdSignature(time.Now().Unix())
	after := time.Now().Unix()

	m := regexp.MustCompile(`^([0-9]+)\.lpw6vgqzsp\.([0-9a-f]{32})$`).FindStringSubmatch(sig)
	require.Len(t, m, 3, "unexpected signature shape: %q", sig)

	ts, err := strconv.ParseInt(m[1], 10, 64)
	require.NoError(t, err)
	assert.True(t, ts >= before, "timestamp %d is older than the request (%d)", ts, before)
	assert.True(t, ts <= after, "timestamp %d is in the future (%d)", ts, after)
}

func TestLooksLikeMovieID(t *testing.T) {
	for name, tt := range map[string]struct {
		id   string
		want bool
	}{
		"internal id":           {"82BkzE", true},
		"internal id lowercase": {"abc12d", true},
		"FC2 number":            {"FC2-4925979", false},
		"coded number":          {"SONE-123", false},
		"bare digits":           {"4925979", false},
		"short":                 {"82B", false},
		"too long":              {"abcdefghijk", false},
		"punctuation":           {"82Bk.E", false},
	} {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, tt.want, looksLikeMovieID(tt.id))
		})
	}
}

func TestCompactNumber(t *testing.T) {
	for name, tt := range map[string]struct{ in, want string }{
		"fc2 dashed":      {"FC2-4925979", "4925979"},
		"fc2 ppv":         {"FC2PPV-4925979", "4925979"},
		"fc2 spaced":      {"FC2 4925979", "4925979"},
		"bare digits":     {"4925979", "4925979"},
		"coded":           {"SONE-123", "SONE123"},
		"coded spaced":    {"sone 123", "SONE123"},
		"already compact": {"ABC123", "ABC123"},
	} {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, tt.want, compactNumber(tt.in))
		})
	}
}

func TestMatchRank(t *testing.T) {
	for name, tt := range map[string]struct {
		number, want string
		rank         int
	}{
		"exact":               {"FC2-4925979", "FC2-4925979", 3},
		"exact lowercase":     {"FC2-4925979", "fc2-4925979", 3},
		"fc2 prefix omitted":  {"FC2-4925979", "4925979", 2},
		"separator ignored":   {"SONE-123", "SONE123", 2},
		"exact beats compact": {"4925979", "4925979", 3},
		"unrelated":           {"SONE-123", "IPX-999", 0},
		"empty number":        {"", "FC2-4925979", 0},
		"empty keyword":       {"FC2-4925979", "", 0},
	} {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, tt.rank, matchRank(tt.number, tt.want))
		})
	}
}

func TestBuildMovieInfo(t *testing.T) {
	var resp movieResponse
	require.NoError(t, json.Unmarshal([]byte(detailFixture), &resp))
	require.NotNil(t, resp.Data.Movie)
	assert.True(t, bool(resp.Success))

	info, err := buildMovieInfo(resp.Data.Movie)
	require.NoError(t, err)

	assert.Equal(t, "82BkzE", info.ID)
	assert.Equal(t, "FC2-4925979", info.Number)
	assert.Equal(t, Name, info.Provider)
	assert.Equal(t, "https://javdb.com/v/82BkzE", info.Homepage)
	assert.Equal(t, "サンプル作品タイトル", info.Title)
	assert.Equal(t, "サンプル説明文。", info.Summary)
	assert.Equal(t, "https://c0.jdbstatic.com/covers/82/82BkzE.jpg", info.CoverURL)
	assert.Equal(t, "https://c0.jdbstatic.com/thumbs/82/82BkzE.jpg", info.ThumbURL)
	assert.Equal(t, 3.63, info.Score)
	assert.Equal(t, 85, info.Runtime)
	assert.Equal(t, "FC2", info.Maker)
	assert.Equal(t, []string{"サンプル女優"}, []string(info.Actors))
	assert.Equal(t, []string{"素人", "美乳"}, []string(info.Genres))
	assert.Equal(t, []string{"https://c0.jdbstatic.com/samples/82/82BkzE_1.jpg"}, []string(info.PreviewImages))
	assert.Equal(t, "https://example.com/preview.mp4", info.PreviewVideoURL)
	assert.Equal(t, 2026, time.Time(info.ReleaseDate).Year())
	assert.Equal(t, time.Month(6), time.Time(info.ReleaseDate).Month())
	assert.Equal(t, 26, time.Time(info.ReleaseDate).Day())
	assert.True(t, info.IsValid())
}

// TestBuildMovieInfo_Fallbacks covers entries without a printed number or a
// title, which MetaTube would otherwise reject as incomplete.
func TestBuildMovieInfo_Fallbacks(t *testing.T) {
	var resp movieResponse
	require.NoError(t, json.Unmarshal([]byte(detailFixture), &resp))
	resp.Data.Movie.Number = ""
	resp.Data.Movie.Title = ""
	resp.Data.Movie.OriginTitle = "オリジナルタイトル"

	info, err := buildMovieInfo(resp.Data.Movie)
	require.NoError(t, err)
	assert.Equal(t, "82BkzE", info.Number)
	assert.Equal(t, "オリジナルタイトル", info.Title)
	assert.True(t, info.IsValid())
}

func TestBuildMovieInfo_Nil(t *testing.T) {
	_, err := buildMovieInfo(nil)
	assert.ErrorIs(t, err, provider.ErrInfoNotFound)
}

func TestParseMovieIDFromURL(t *testing.T) {
	p := New()
	for name, tt := range map[string]struct {
		url  string
		want string
	}{
		"movie page":     {"https://javdb.com/v/82BkzE", "82BkzE"},
		"trailing slash": {"https://javdb.com/v/82BkzE/", "82BkzE"},
		"site root":      {"https://javdb.com/", ""},
		"empty path":     {"https://javdb.com", ""},
	} {
		t.Run(name, func(t *testing.T) {
			id, err := p.ParseMovieIDFromURL(tt.url)
			require.NoError(t, err)
			assert.Equal(t, tt.want, id)
		})
	}

	_, err := p.ParseMovieIDFromURL("://bad-url")
	assert.Error(t, err)
}

func TestNormalizeMovieID(t *testing.T) {
	p := New()
	assert.Equal(t, "82BkzE", p.NormalizeMovieID("  82BkzE  "))
	// Internal ids are case sensitive and must not be upper-cased.
	assert.Equal(t, "82BkzE", p.NormalizeMovieID("82BkzE"))
	assert.Equal(t, "FC2-4925979", p.NormalizeMovieID("FC2-4925979"))
}

func TestNormalizeMovieKeyword(t *testing.T) {
	p := New()
	// An empty return would tell the engine to skip this provider, so every
	// keyword is accepted; only surrounding whitespace is removed.
	assert.Equal(t, "fc2-4925979", p.NormalizeMovieKeyword(" fc2-4925979 "))
	assert.Equal(t, "SONE-123", p.NormalizeMovieKeyword("SONE-123"))
	assert.NotEmpty(t, p.NormalizeMovieKeyword("heyzo-1234"))
}

func TestFlexScalars(t *testing.T) {
	var v struct {
		Int      flexInt   `json:"int"`
		IntStr   flexInt   `json:"int_str"`
		IntNull  flexInt   `json:"int_null"`
		Float    flexFloat `json:"float"`
		FloatStr flexFloat `json:"float_str"`
		Names    flexNames `json:"names"`
		Objects  flexNames `json:"objects"`
		NullName flexNames `json:"null_names"`
	}
	require.NoError(t, json.Unmarshal([]byte(`{
		"int": 85,
		"int_str": "85",
		"int_null": null,
		"float": 3.63,
		"float_str": "3.63",
		"names": ["a", "b"],
		"objects": [{"id": 1, "name": "c"}],
		"null_names": null
	}`), &v))

	assert.Equal(t, flexInt(85), v.Int)
	assert.Equal(t, flexInt(85), v.IntStr)
	assert.Equal(t, flexInt(0), v.IntNull)
	assert.Equal(t, flexFloat(3.63), v.Float)
	assert.Equal(t, flexFloat(3.63), v.FloatStr)
	assert.Equal(t, flexNames{"a", "b"}, v.Names)
	assert.Equal(t, flexNames{"c"}, v.Objects)
	assert.Nil(t, v.NullName)

	assert.Error(t, json.Unmarshal([]byte(`{"int": "abc"}`), &v))
	assert.Error(t, json.Unmarshal([]byte(`{"float": "abc"}`), &v))
}

func TestAPISuccess(t *testing.T) {
	for name, tt := range map[string]struct {
		json string
		want bool
		err  bool
	}{
		"number one":  {`1`, true, false},
		"number zero": {`0`, false, false},
		"string one":  {`"1"`, true, false},
		"bool true":   {`true`, true, false},
		"bool false":  {`false`, false, false},
		"null":        {`null`, false, false},
		"invalid":     {`"yes"`, false, true},
	} {
		t.Run(name, func(t *testing.T) {
			var s apiSuccess
			err := json.Unmarshal([]byte(tt.json), &s)
			if tt.err {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, bool(s))
		})
	}
}

func TestAPIError(t *testing.T) {
	var resp movieResponse
	require.NoError(t, json.Unmarshal([]byte(errFixture), &resp))
	assert.False(t, bool(resp.Success))

	err := resp.apiError()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Invalid signature")
	assert.Contains(t, err.Error(), "InvalidSignature")

	// A success envelope carries no action and therefore no error.
	var ok movieResponse
	require.NoError(t, json.Unmarshal([]byte(detailFixture), &ok))
	assert.NoError(t, ok.apiError())
}

// TestGetMovieInfoByID_InternalID checks that an internal id goes straight to
// the detail route, without spending a search request.
func TestGetMovieInfoByID_InternalID(t *testing.T) {
	stub := &apiStub{detail: detailFixture}
	stub.start(t)

	info, err := New().GetMovieInfoByID("82BkzE")
	require.NoError(t, err)
	assert.Equal(t, "82BkzE", info.ID)
	assert.True(t, info.IsValid())

	assert.Equal(t, []string{stub.detailPath()}, stub.recordedPaths())
	require.Equal(t, 1, stub.recordedCount())
	assertAppIdentity(t, stub, 0)
	assert.Equal(t, appUserAgent, stub.recordedHeaderGet(0, "User-Agent"))
	assert.Equal(t, appAcceptLanguage, stub.recordedHeaderGet(0, "Accept-Language"))
	assert.Regexp(t, `^[0-9]+\.lpw6vgqzsp\.[0-9a-f]{32}$`,
		stub.recordedHeaderGet(0, "jdsignature"))
}

// TestGetMovieInfoByID_PrintedNumber checks the two-step path: a printed
// number is resolved through the search route and then fetched by id.
func TestGetMovieInfoByID_PrintedNumber(t *testing.T) {
	stub := &apiStub{search: searchFixture, detail: detailFixture}
	stub.start(t)

	info, err := New().GetMovieInfoByID("FC2-4925979")
	require.NoError(t, err)
	assert.Equal(t, "82BkzE", info.ID)
	assert.Equal(t, "FC2-4925979", info.Number)
	assert.True(t, info.IsValid())

	assert.Equal(t, []string{apiSearchPath, stub.detailPath()}, stub.recordedPaths())
	assert.Equal(t, "FC2-4925979", stub.recordedQueryGet(0, "q"))
	assert.Equal(t, "1", stub.recordedQueryGet(0, "page"))

	// Every request must carry a signed header, ask for JSON, look like the
	// official Android app, and advertise the Flutter/Dart UA the app sends.
	require.Equal(t, 2, stub.recordedCount())
	for i := 0; i < 2; i++ {
		assert.Regexp(t, `^[0-9]+\.lpw6vgqzsp\.[0-9a-f]{32}$`,
			stub.recordedHeaderGet(i, "jdsignature"), "request %d is unsigned", i)
		assert.Equal(t, "application/json", stub.recordedHeaderGet(i, "accept"))
		assert.Equal(t, appUserAgent, stub.recordedHeaderGet(i, "User-Agent"),
			"request %d missing app user-agent", i)
		assert.Equal(t, appAcceptLanguage, stub.recordedHeaderGet(i, "Accept-Language"),
			"request %d missing accept-language", i)
		assertAppIdentity(t, stub, i)
	}
}

// assertAppIdentity checks that request i carried the official Android app's
// identification query parameters, including a stable per-process device_uuid.
func assertAppIdentity(t *testing.T, stub *apiStub, i int) {
	t.Helper()
	assert.Equal(t, appChannel, stub.recordedQueryGet(i, "app_channel"))
	assert.Equal(t, appVersion, stub.recordedQueryGet(i, "app_version"))
	assert.Equal(t, appVersionNumber, stub.recordedQueryGet(i, "app_version_number"))
	assert.Equal(t, appPlatform, stub.recordedQueryGet(i, "platform"))
	assert.Equal(t, appSystemVersion, stub.recordedQueryGet(i, "system_version"))
	assert.Equal(t, appDeviceModel, stub.recordedQueryGet(i, "device_model"))
	assert.Equal(t, appDeviceName, stub.recordedQueryGet(i, "device_name"))
	assert.Equal(t, deviceUUID, stub.recordedQueryGet(i, "device_uuid"))
	assert.Regexp(t, `^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`,
		stub.recordedQueryGet(i, "device_uuid"))
}

// TestGetMovieInfoByID_BareDigits covers an FC2 number typed without its
// prefix, which only matches JavDB's "FC2-<digits>" numbering.
func TestGetMovieInfoByID_BareDigits(t *testing.T) {
	stub := &apiStub{search: searchFixtureFC2Only, detail: detailFixture}
	stub.start(t)

	info, err := New().GetMovieInfoByID("4925979")
	require.NoError(t, err)
	assert.Equal(t, "82BkzE", info.ID)
	assert.True(t, info.IsValid())
}

// TestGetMovieInfoByURL covers the javdb.com URL form.
func TestGetMovieInfoByURL(t *testing.T) {
	stub := &apiStub{detail: detailFixture}
	stub.start(t)

	info, err := New().GetMovieInfoByURL("https://javdb.com/v/82BkzE")
	require.NoError(t, err)
	assert.Equal(t, "82BkzE", info.ID)
	assert.Equal(t, []string{stub.detailPath()}, stub.recordedPaths())
}

// TestGetMovieInfoByMovieID_NotFound covers a missing movie reported by status
// code. The detail route is exercised directly, because GetMovieInfoByID would
// additionally fall back to a search.
func TestGetMovieInfoByMovieID_NotFound(t *testing.T) {
	stub := &apiStub{detailStatus: http.StatusNotFound}
	stub.start(t)

	_, err := New().getMovieInfoByMovieID("82BkzE")
	assert.ErrorIs(t, err, provider.ErrInfoNotFound)
}

// TestGetMovieInfoByMovieID_APIError covers a failure envelope delivered with
// HTTP 200, which is how the API reports a rejected signature.
func TestGetMovieInfoByMovieID_APIError(t *testing.T) {
	stub := &apiStub{detail: errFixture}
	stub.start(t)

	_, err := New().getMovieInfoByMovieID("82BkzE")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "InvalidSignature")
}

// TestSearchMovie_APIError covers the same failure envelope on the search route.
func TestSearchMovie_APIError(t *testing.T) {
	stub := &apiStub{search: errFixture}
	stub.start(t)

	_, err := New().SearchMovie("FC2-4925979")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "InvalidSignature")
}

// TestGetMovieInfoByID_Ambiguous ensures a keyword matching several distinct
// movies fails instead of silently returning the first hit.
func TestGetMovieInfoByID_Ambiguous(t *testing.T) {
	stub := &apiStub{search: searchFixtureAmbiguous}
	stub.start(t)

	_, err := New().GetMovieInfoByID("SONE-123")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ambiguous")
}

// TestGetMovieInfoByID_NoMatch covers a keyword that matches nothing.
func TestGetMovieInfoByID_NoMatch(t *testing.T) {
	stub := &apiStub{search: searchFixtureEmpty}
	stub.start(t)

	_, err := New().GetMovieInfoByID("SONE-999")
	assert.ErrorIs(t, err, provider.ErrInfoNotFound)
}

func TestGetMovieInfoByID_InvalidID(t *testing.T) {
	_, err := New().GetMovieInfoByID("   ")
	assert.ErrorIs(t, err, provider.ErrInvalidID)
}

func TestSearchMovie(t *testing.T) {
	stub := &apiStub{search: searchFixture}
	stub.start(t)

	results, err := New().SearchMovie("FC2-4925979")
	require.NoError(t, err)
	require.Len(t, results, 2)

	// The exact match must be present with its own id, not the loose one.
	assert.Equal(t, "82BkzE", results[1].ID)
	assert.Equal(t, "FC2-4925979", results[1].Number)
	assert.Equal(t, Name, results[1].Provider)
	assert.Equal(t, "https://javdb.com/v/82BkzE", results[1].Homepage)
	assert.Equal(t, 3.63, results[1].Score)
	assert.True(t, results[1].IsValid())
}

func TestSearchMovie_Empty(t *testing.T) {
	stub := &apiStub{search: searchFixtureEmpty}
	stub.start(t)

	results, err := New().SearchMovie("nothing")
	require.NoError(t, err)
	assert.Empty(t, results)
}

func TestProviderMetadata(t *testing.T) {
	p := New()
	assert.Equal(t, Name, p.Name())
	assert.Equal(t, float64(Priority), p.Priority())
	assert.Equal(t, "https://javdb.com/", p.URL().String())
	// 994 is the tier JavDB shares with JAV321; changing it reorders both.
	assert.EqualValues(t, 994, Priority)
}

// isolateWebImagePrefix restores the process-wide prefix cache when the test
// ends, including on failure.
func isolateWebImagePrefix(t *testing.T) {
	t.Helper()
	webImageMu.Lock()
	prefix, expires := webImagePrefix, webImageExpires
	webImageMu.Unlock()
	t.Cleanup(func() {
		webImageMu.Lock()
		webImagePrefix, webImageExpires = prefix, expires
		webImageMu.Unlock()
	})
}

func TestRewriteImageURL(t *testing.T) {
	isolateWebImagePrefix(t)
	// Trailing slash must not produce a double slash in the result.
	setWebImagePrefix("https://c0.jdbstatic.com/")

	for name, tt := range map[string]struct{ in, want string }{
		"encrypted cmastd cover": {
			"https://tp-iu.cmastd.com/rhe951l4q/covers/zb/ZbX7.jpg",
			"https://c0.jdbstatic.com/covers/zb/ZbX7.jpg",
		},
		"encrypted small cover": {
			"https://tp-iu.cmastd.com/rhe951l4q/small_covers/5e/5EpYBB.jpg",
			"https://c0.jdbstatic.com/small_covers/5e/5EpYBB.jpg",
		},
		"cmastd host without token": {
			"https://cmastd.com/covers/zb/ZbX7.jpg",
			"https://c0.jdbstatic.com/covers/zb/ZbX7.jpg",
		},
		"cmastd subdomain": {
			"https://CDN.cmastd.com/token99/thumbs/ab/Ab.jpg",
			"https://c0.jdbstatic.com/thumbs/ab/Ab.jpg",
		},
		"later cdn host": {
			"https://tp.spfcas.com/rhe951l4q/avatars/83/83V.jpg",
			"https://c0.jdbstatic.com/avatars/83/83V.jpg",
		},
		"known token outside media dirs": {
			"https://tp.spfcas.com/rhe951l4q/images/c.jpg",
			"https://c0.jdbstatic.com/images/c.jpg",
		},
		"query dropped": {
			"https://tp-iu.cmastd.com/rhe951l4q/samples/zb/ZbX7.jpg?sign=abc",
			"https://c0.jdbstatic.com/samples/zb/ZbX7.jpg",
		},
		"plain jdbstatic": {
			"https://c0.jdbstatic.com/covers/82/82BkzE.jpg",
			"https://c0.jdbstatic.com/covers/82/82BkzE.jpg",
		},
		"plain other jdbstatic host": {
			"https://c1.jdbstatic.com/thumbs/aa/aaaaa1.jpg",
			"https://c1.jdbstatic.com/thumbs/aa/aaaaa1.jpg",
		},
		"empty": {
			"",
			"",
		},
		"blank": {
			"   ",
			"",
		},
		"garbage": {
			"not a url",
			"not a url",
		},
		"unrelated": {
			"https://example.com/preview.mp4",
			"https://example.com/preview.mp4",
		},
		"lookalike path": {
			"https://example.com/albums/thumbs/1.jpg",
			"https://example.com/albums/thumbs/1.jpg",
		},
	} {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, tt.want, rewriteImageURL(tt.in))
		})
	}
}

func TestFlexNamesPreviewImages(t *testing.T) {
	var v struct {
		Images flexNames `json:"preview_images"`
	}
	require.NoError(t, json.Unmarshal([]byte(`{
		"preview_images": [
			{"thumb_url": "https://cdn.example/thumb.jpg", "large_url": "https://cdn.example/large.jpg"},
			{"thumb_url": "https://cdn.example/only-thumb.jpg"},
			{"url": "https://cdn.example/url.jpg"},
			{"large_url": "  ", "name": "fallback"},
			{"name": "actor"}
		]
	}`), &v))
	assert.Equal(t, flexNames{
		"https://cdn.example/large.jpg",
		"https://cdn.example/only-thumb.jpg",
		"https://cdn.example/url.jpg",
		"fallback",
		"actor",
	}, v.Images)
}

// detailFixtureEncrypted is a detail payload whose images are the encrypted
// app CDN form, including preview_images as objects with large_url.
const detailFixtureEncrypted = `{
  "success": 1,
  "action": null,
  "message": null,
  "data": {
    "movie": {
      "id": "82BkzE",
      "number": "FC2-4925979",
      "title": "サンプル作品タイトル",
      "thumb_url": "https://tp-iu.cmastd.com/rhe951l4q/small_covers/82/82BkzE.jpg",
      "cover_url": "https://tp-iu.cmastd.com/rhe951l4q/covers/82/82BkzE.jpg",
      "duration": 85,
      "score": 3.63,
      "release_date": "2026-06-26",
      "maker_name": "FC2",
      "preview_images": [{
        "thumb_url": "https://tp-iu.cmastd.com/rhe951l4q/samples/82/82BkzE_s_0.jpg",
        "large_url": "https://tp-iu.cmastd.com/rhe951l4q/samples/82/82BkzE_l_0.jpg"
      }]
    }
  }
}`

func TestGetMovieInfoByID_EncryptedImages(t *testing.T) {
	isolateWebImagePrefix(t)
	setWebImagePrefix("https://c0.jdbstatic.com")
	require.Contains(t, detailFixtureEncrypted, "tp-iu.cmastd.com")

	stub := &apiStub{detail: detailFixtureEncrypted}
	stub.start(t)

	info, err := New().GetMovieInfoByID("82BkzE")
	require.NoError(t, err)
	assert.Equal(t, "https://c0.jdbstatic.com/covers/82/82BkzE.jpg", info.CoverURL)
	assert.Equal(t, "https://c0.jdbstatic.com/small_covers/82/82BkzE.jpg", info.ThumbURL)
	assert.Equal(t, []string{"https://c0.jdbstatic.com/samples/82/82BkzE_l_0.jpg"}, []string(info.PreviewImages))
	assert.NotContains(t, info.CoverURL, "cmastd.com")
	assert.NotContains(t, info.ThumbURL, "cmastd.com")
	assert.True(t, info.IsValid())
	// A warm prefix cache must not spend a startup request.
	assert.Equal(t, []string{stub.detailPath()}, stub.recordedPaths())
}

const searchFixtureEncrypted = `{
  "success": 1,
  "action": null,
  "message": null,
  "data": {
    "movies": [
      {"id": "82BkzE", "number": "FC2-4925979", "title": "サンプル作品タイトル",
       "thumb_url": "https://tp.spfcas.com/rhe951l4q/small_covers/82/82BkzE.jpg",
       "cover_url": "https://tp.spfcas.com/rhe951l4q/covers/82/82BkzE.jpg",
       "duration": 85, "score": "3.63", "release_date": "2026-06-26"}
    ]
  }
}`

func TestSearchMovie_EncryptedImages(t *testing.T) {
	isolateWebImagePrefix(t)
	setWebImagePrefix("https://c0.jdbstatic.com")

	stub := &apiStub{search: searchFixtureEncrypted}
	stub.start(t)

	results, err := New().SearchMovie("FC2-4925979")
	require.NoError(t, err)
	require.Len(t, results, 1)
	assert.Equal(t, "https://c0.jdbstatic.com/covers/82/82BkzE.jpg", results[0].CoverURL)
	assert.Equal(t, "https://c0.jdbstatic.com/small_covers/82/82BkzE.jpg", results[0].ThumbURL)
	assert.NotContains(t, results[0].CoverURL, "spfcas.com")
	assert.Equal(t, []string{apiSearchPath}, stub.recordedPaths())
}

func TestRewriteImageURL_LoadsPrefixFromStartup(t *testing.T) {
	isolateWebImagePrefix(t)
	stub := &apiStub{startup: `{
		"success": 1,
		"action": null,
		"message": null,
		"data": {"web_image_prefix": "https://img.example.test/"}
	}`}
	stub.start(t)
	setWebImagePrefix("")
	_ = New()

	in := "https://tp-iu.cmastd.com/rhe951l4q/covers/zb/ZbX7.jpg"
	assert.Equal(t, "https://img.example.test/covers/zb/ZbX7.jpg", rewriteImageURL(in))
	assert.Equal(t, "https://img.example.test/covers/zb/ZbX7.jpg", rewriteImageURL(in))

	var startups []int
	for i, p := range stub.recordedPaths() {
		if p == apiStartupPath {
			startups = append(startups, i)
		}
	}
	require.Len(t, startups, 1, "startup should be fetched once and cached")
	assert.Regexp(t, `^[0-9]+\.lpw6vgqzsp\.[0-9a-f]{32}$`,
		stub.recordedHeaderGet(startups[0], "jdsignature"))
	assertAppIdentity(t, stub, startups[0])
}

func TestRewriteImageURL_StartupFailureUsesFallback(t *testing.T) {
	isolateWebImagePrefix(t)
	stub := &apiStub{startupStatus: http.StatusBadGateway}
	stub.start(t)
	setWebImagePrefix("")
	_ = New()

	got := rewriteImageURL("https://tp-iu.cmastd.com/rhe951l4q/thumbs/zb/ZbX7.jpg")
	assert.Equal(t, fallbackWebImagePrefix+"/thumbs/zb/ZbX7.jpg", got)
	_ = rewriteImageURL("https://tp-iu.cmastd.com/rhe951l4q/covers/zb/ZbX7.jpg")

	var n int
	for _, p := range stub.recordedPaths() {
		if p == apiStartupPath {
			n++
		}
	}
	assert.Equal(t, 1, n, "a failed startup should be cached briefly")
}

func TestFetcherSendsReferer(t *testing.T) {
	var referer string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		referer = r.Referer()
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))
	t.Cleanup(srv.Close)

	resp, err := New().Fetch(srv.URL)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, "https://javdb.com/", referer)
}
