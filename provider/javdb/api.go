package javdb

import (
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

// JavDB publishes a JSON API for its mobile app which, unlike the website, is
// public: the endpoints used here are authorised by a computed request
// signature rather than by an account, so the provider needs no configuration
// and no cookie. (The javdb.com *website* detail pages do require a login,
// which is why they are not scraped.)
const (
	// apiMoviePath takes the internal movie id, e.g. "82BkzE".
	apiMoviePath = "/api/v4/movies/%s"
	// apiSearchPath is a keyword search and is used to turn a printed number
	// into the internal movie id that apiMoviePath expects.
	apiSearchPath = "/api/v2/search"
	// apiStartupPath returns data.web_image_prefix, the plain JPEG base URL.
	apiStartupPath = "/api/v1/startup"
)

// fallbackWebImagePrefix is the plain image CDN used when startup cannot be
// read. It serves the same files the website uses, without client-side
// decryption.
const fallbackWebImagePrefix = "https://c0.jdbstatic.com"

const (
	// webImagePrefixTTL is how long a prefix learned from startup is reused.
	// The CDN base changes rarely; six hours avoids a startup request on every
	// movie while still following a rotation within a day.
	webImagePrefixTTL = 6 * time.Hour
	// webImagePrefixRetry is how long the fallback is reused after startup
	// fails, so one outage does not turn every image into another call.
	webImagePrefixRetry = 15 * time.Minute
	// encryptedImageToken is the opaque path segment the app inserts in front
	// of the real file (…/rhe951l4q/covers/…). It is not part of the JPEG path.
	encryptedImageToken = "rhe951l4q"
)

// apiHost is the API host the app talks to. javdb.com serves the same routes,
// but this is the host the app advertises.
//
// It is a variable rather than a constant so tests can point the provider at a
// local httptest server and exercise the whole request path - URL building,
// signing, envelope handling and mapping - without touching the network.
var apiHost = "https://jdforrepam.com"

// Official Android app identification attached to every request. The app
// advertises these as query parameters alongside the signed jdsignature
// header; sending only the signature (and a randomised scraper UA) has never
// been shown to be accepted by the API. Values match a recent official
// Android build (1.9.28 / build 10928) running on a Pixel 6.
const (
	appChannel       = "official"
	appVersion       = "1.9.28"
	appVersionNumber = "10928"
	appPlatform      = "android"
	appSystemVersion = "13"
	appDeviceModel   = "Pixel 6"
	appDeviceName    = "Pixel"
	// appUserAgent is the Flutter/Dart HTTP client UA the official app sends.
	appUserAgent = "Dart/3.4 (dart:io)"
	// appAcceptLanguage is the locale the Japanese-language app requests.
	appAcceptLanguage = "ja"
)

// deviceUUID is a random v4 UUID minted once per process so successive
// requests look like they come from the same installed app. It is not a
// secret and is not persisted across restarts.
var deviceUUID = uuid.NewString()

// The jdsignature header is "{timestamp}.{tag}.{md5(timestamp + key)}". The key
// and tag are fixed key material belonging to the official Android app; only
// the timestamp changes per request. The algorithm is:
//
//	sum := md5.Sum([]byte(strconv.FormatInt(ts, 10) + jdSignatureKey))
//	header := fmt.Sprintf("%d.%s.%s", ts, jdSignatureTag, hex.EncodeToString(sum[:]))
const (
	// jdSignatureKey is the md5 key material.
	jdSignatureKey = "71cf27bb3c0bcdf207b64abecddc970098c7421ee7203b9cdae54478478a199e7d5a6e1a57691123c1a931c057842fb73ba3b3c83bcd69c17ccf174081e3d8aa"
	// jdSignatureTag is the middle segment of the header value.
	jdSignatureTag = "lpw6vgqzsp"
)

// appIdentity returns the query parameters the official Android app attaches
// to every request. Callers must merge these onto any endpoint-specific
// parameters before the request is issued.
func appIdentity() url.Values {
	v := url.Values{}
	v.Set("app_channel", appChannel)
	v.Set("app_version", appVersion)
	v.Set("app_version_number", appVersionNumber)
	v.Set("platform", appPlatform)
	v.Set("system_version", appSystemVersion)
	v.Set("device_model", appDeviceModel)
	v.Set("device_name", appDeviceName)
	v.Set("device_uuid", deviceUUID)
	return v
}

// jdSignature returns the value of the jdsignature header for a unix second.
//
// The signature is deliberately time-based and is not a secret: it is baked
// into every copy of the app, so reproducing it here does not weaken anyone's
// account. Only the API host validates it, and it grants access to public
// metadata only.
func jdSignature(ts int64) string {
	sum := md5.Sum([]byte(strconv.FormatInt(ts, 10) + jdSignatureKey))
	return fmt.Sprintf("%d.%s.%s", ts, jdSignatureTag, hex.EncodeToString(sum[:]))
}

// apiEnvelope wraps every JavDB app API response. Failures are normally
// reported as HTTP 200 with success 0, so the envelope - not the HTTP status
// code - decides whether a call succeeded.
type apiEnvelope struct {
	Success apiSuccess `json:"success"`
	Action  string     `json:"action"`
	Message string     `json:"message"`
}

// apiError converts a failed envelope into an error, or nil when the response
// carries an action-less success.
func (e *apiEnvelope) apiError() error {
	if e.Action == "" {
		return nil
	}
	if msg := strings.TrimSpace(e.Message); msg != "" {
		return fmt.Errorf("javdb: api error: %s (%s)", msg, e.Action)
	}
	return fmt.Errorf("javdb: api error: %s", e.Action)
}

// apiSuccess decodes the envelope's "success" field. The API sends the number
// 1 or 0 and, on some routes, a boolean.
type apiSuccess bool

// UnmarshalJSON implements json.Unmarshaler.
func (s *apiSuccess) UnmarshalJSON(b []byte) error {
	switch strings.Trim(strings.TrimSpace(string(b)), `"`) {
	case "1", "true":
		*s = true
	case "0", "false", "null", "":
		*s = false
	default:
		return fmt.Errorf("javdb: unexpected success value %q", string(b))
	}
	return nil
}

// apiMovie is a movie object as returned by the search and detail endpoints.
// The search endpoint sends a subset of the same fields under the same names,
// so one type covers both.
type apiMovie struct {
	ID              string    `json:"id"`
	Number          string    `json:"number"`
	Title           string    `json:"title"`
	OriginTitle     string    `json:"origin_title"`
	Summary         string    `json:"summary"`
	ThumbURL        string    `json:"thumb_url"`
	CoverURL        string    `json:"cover_url"`
	Duration        flexInt   `json:"duration"`
	Score           flexFloat `json:"score"`
	ReleaseDate     string    `json:"release_date"`
	MakerName       string    `json:"maker_name"`
	DirectorName    string    `json:"director_name"`
	SeriesName      string    `json:"series_name"`
	PreviewVideoURL string    `json:"preview_video_url"`
	PreviewImages   flexNames `json:"preview_images"`
	Actors          flexNames `json:"actors"`
	Tags            flexNames `json:"tags"`
}

// flexInt decodes a JSON number or a numeric string as an int.
type flexInt int

// UnmarshalJSON implements json.Unmarshaler.
func (i *flexInt) UnmarshalJSON(b []byte) error {
	s := strings.Trim(strings.TrimSpace(string(b)), `"`)
	if s == "" || s == "null" {
		*i = 0
		return nil
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return fmt.Errorf("javdb: unexpected numeric value %q", string(b))
	}
	*i = flexInt(int(v))
	return nil
}

// flexFloat decodes a JSON number or a numeric string as a float64. The API
// sends the score as a number on the detail route and as a quoted string such
// as "4.3" on some list routes.
type flexFloat float64

// UnmarshalJSON implements json.Unmarshaler.
func (f *flexFloat) UnmarshalJSON(b []byte) error {
	s := strings.Trim(strings.TrimSpace(string(b)), `"`)
	if s == "" || s == "null" {
		*f = 0
		return nil
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return fmt.Errorf("javdb: unexpected numeric value %q", string(b))
	}
	*f = flexFloat(v)
	return nil
}

// flexNames decodes an array whose items are either plain strings or objects.
// Actors and tags carry "name". Preview images are objects with large_url,
// thumb_url and/or url; large_url is the full-size sample and wins.
type flexNames []string

// UnmarshalJSON implements json.Unmarshaler.
func (n *flexNames) UnmarshalJSON(b []byte) error {
	if string(b) == "null" {
		return nil
	}
	var names []string
	if err := json.Unmarshal(b, &names); err == nil {
		*n = names
		return nil
	}
	var objects []struct {
		Name     string `json:"name"`
		LargeURL string `json:"large_url"`
		ThumbURL string `json:"thumb_url"`
		URL      string `json:"url"`
	}
	if err := json.Unmarshal(b, &objects); err != nil {
		return err
	}
	names = make([]string, 0, len(objects))
	for _, o := range objects {
		names = append(names, firstNonEmpty(o.LargeURL, o.ThumbURL, o.URL, o.Name))
	}
	*n = names
	return nil
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if s := strings.TrimSpace(v); s != "" {
			return s
		}
	}
	return ""
}

// startupResponse is the payload of GET /api/v1/startup. web_image_prefix is
// the plain JPEG base the website uses; the app's own CDN URLs are encrypted.
type startupResponse struct {
	apiEnvelope
	Data struct {
		WebImagePrefix string `json:"web_image_prefix"`
	} `json:"data"`
}

// movieResponse is the payload of GET /api/v4/movies/{id}.
type movieResponse struct {
	apiEnvelope
	Data struct {
		Movie *apiMovie `json:"movie"`
	} `json:"data"`
}

// searchResponse is the payload of GET /api/v2/search.
type searchResponse struct {
	apiEnvelope
	Data struct {
		Movies []*apiMovie `json:"movies"`
	} `json:"data"`
}

// mediaPathPattern extracts the stable file path from an app CDN URL.
// Encrypted URLs insert one opaque segment before it
// (/rhe951l4q/covers/zb/ZbX7.jpg); plain URLs do not (/covers/zb/ZbX7.jpg).
var mediaPathPattern = regexp.MustCompile(`(?i)^(?:/[^/]+)?/((?:small_)?covers|thumbs|samples|avatars)/(.+)$`)

// webImageLoader fetches data.web_image_prefix. New wires it once; tests may
// replace it. A nil loader means "use the fallback".
var (
	webImageMu         sync.Mutex
	webImagePrefix     string
	webImageExpires    time.Time
	webImageLoader     func() (string, error)
	webImageLoaderOnce sync.Once
)

// ensureWebImageLoader installs the process-wide startup loader. Later calls
// are ignored so every JavDB instance shares one prefix cache.
func ensureWebImageLoader(load func() (string, error)) {
	if load == nil {
		return
	}
	webImageLoaderOnce.Do(func() {
		webImageLoader = load
	})
}

// setWebImagePrefix caches prefix for webImagePrefixTTL. An empty prefix
// clears the cache. Tests set this so a rewrite does not call startup.
func setWebImagePrefix(prefix string) {
	prefix = normalizeWebImagePrefix(prefix)
	webImageMu.Lock()
	defer webImageMu.Unlock()
	webImagePrefix = prefix
	if prefix == "" {
		webImageExpires = time.Time{}
		return
	}
	webImageExpires = time.Now().Add(webImagePrefixTTL)
}

func normalizeWebImagePrefix(prefix string) string {
	prefix = strings.TrimSpace(prefix)
	if prefix == "" {
		return ""
	}
	if !strings.Contains(prefix, "://") {
		prefix = "https://" + strings.TrimLeft(prefix, "/")
	}
	u, err := url.Parse(prefix)
	if err != nil || u.Hostname() == "" {
		return ""
	}
	u.RawQuery = ""
	u.Fragment = ""
	return strings.TrimRight(u.String(), "/")
}

// currentWebImagePrefix returns the cached plain base, loading it through
// webImageLoader on a miss. The lock is held across the load so a movie's
// cover, thumb and samples share one startup request. The loader must not
// call back into this function or setWebImagePrefix.
func currentWebImagePrefix() string {
	webImageMu.Lock()
	defer webImageMu.Unlock()
	if webImagePrefix != "" && time.Now().Before(webImageExpires) {
		return webImagePrefix
	}
	if webImageLoader != nil {
		if p, err := webImageLoader(); err == nil {
			if p = normalizeWebImagePrefix(p); p != "" {
				webImagePrefix = p
				webImageExpires = time.Now().Add(webImagePrefixTTL)
				return p
			}
		}
	}
	webImagePrefix = fallbackWebImagePrefix
	webImageExpires = time.Now().Add(webImagePrefixRetry)
	return webImagePrefix
}

// rewriteMovieImages rewrites every image URL on m in place. Nil is a no-op.
func rewriteMovieImages(m *apiMovie) {
	if m == nil {
		return
	}
	m.CoverURL = rewriteImageURL(m.CoverURL)
	m.ThumbURL = rewriteImageURL(m.ThumbURL)
	for i, raw := range m.PreviewImages {
		m.PreviewImages[i] = rewriteImageURL(raw)
	}
}

// rewriteImageURL turns an encrypted app CDN URL into a plain JPEG URL:
//
//	https://tp-iu.cmastd.com/rhe951l4q/covers/zb/ZbX7.jpg
//	-> {web_image_prefix}/covers/zb/ZbX7.jpg
//
// Already-plain URLs (https://c0.jdbstatic.com/covers/...) are left unchanged,
// as are empty values and strings that are not URLs.
func rewriteImageURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" {
		return raw
	}
	if media, ok := encryptedMediaPath(u); ok {
		return joinImagePrefix(currentWebImagePrefix(), media)
	}
	return raw
}

func encryptedMediaPath(u *url.URL) (string, bool) {
	path := u.Path
	if media := extractMediaPath(path); media != "" && shouldRewriteMedia(u.Hostname(), path, media) {
		return media, true
	}
	// The same token also fronts files outside the directories above, for
	// example /rhe951l4q/images/c.jpg on whichever host currently serves it.
	token := "/" + encryptedImageToken + "/"
	if i := strings.Index(path, token); i >= 0 {
		rest := strings.TrimLeft(path[i+len(token):], "/")
		if rest != "" {
			return rest, true
		}
	}
	return "", false
}

func shouldRewriteMedia(host, path, media string) bool {
	if isEncryptedImageHost(host) {
		return true
	}
	if strings.Contains(path, "/"+encryptedImageToken+"/") {
		return true
	}
	return hasCDNTokenPrefix(path, media)
}

// isEncryptedImageHost reports whether host serves the app's client-encrypted
// images. tp-iu.cmastd.com is the host from the original capture; tp.spfcas.com
// is a later front for the same paths. Plain hosts such as c0.jdbstatic.com
// are not matched.
func isEncryptedImageHost(host string) bool {
	host = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(host), "."))
	for _, root := range []string{"cmastd.com", "spfcas.com"} {
		if host == root || strings.HasSuffix(host, "."+root) {
			return true
		}
	}
	return false
}

func hasCDNTokenPrefix(urlPath, media string) bool {
	if strings.EqualFold(urlPath, "/"+media) {
		return false
	}
	token, suffix, ok := strings.Cut(strings.TrimPrefix(urlPath, "/"), "/")
	if !ok || !strings.EqualFold(suffix, media) {
		return false
	}
	return looksLikeCDNToken(token)
}

// looksLikeCDNToken reports whether token is an opaque CDN segment such as
// "rhe951l4q", rather than a dictionary word that happens to precede /thumbs/.
func looksLikeCDNToken(token string) bool {
	if len(token) < 6 || len(token) > 32 {
		return false
	}
	digit := false
	for _, r := range token {
		switch {
		case r >= '0' && r <= '9':
			digit = true
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z':
			continue
		default:
			return false
		}
	}
	return digit
}

func extractMediaPath(urlPath string) string {
	m := mediaPathPattern.FindStringSubmatch(urlPath)
	if m == nil {
		return ""
	}
	return m[1] + "/" + m[2]
}

func joinImagePrefix(prefix, media string) string {
	return strings.TrimRight(prefix, "/") + "/" + strings.TrimLeft(media, "/")
}
