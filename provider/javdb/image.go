package javdb

import (
	stderrors "errors"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/metatube-community/metatube-sdk-go/errors"
)

// imagePathPattern splits a plain image path into an optional leading
// segment, the media directory and the file below it.
var imagePathPattern = regexp.MustCompile(`(?i)^((?:/[^/]+)?)/((?:small_)?covers|thumbs|samples|avatars)/(.+)$`)

const (
	dirSmallCovers = "small_covers"
	dirThumbs      = "thumbs"
	dirCovers      = "covers"
)

// splitImageURL parses raw and returns the URL with the media directory
// reported separately. ok is false for anything that is not a media URL.
func splitImageURL(raw string) (u *url.URL, lead, dir, file string, ok bool) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Hostname() == "" {
		return nil, "", "", "", false
	}
	m := imagePathPattern.FindStringSubmatch(u.Path)
	if m == nil {
		return nil, "", "", "", false
	}
	return u, m[1], strings.ToLower(m[2]), m[3], true
}

// withImageDir returns u with its media directory replaced by dir.
func withImageDir(u *url.URL, lead, dir, file string) string {
	c := *u
	c.Path = lead + "/" + dir + "/" + file
	c.RawPath = ""
	return c.String()
}

// smallCoverToThumb maps a /small_covers/ URL to the /thumbs/ URL of the same
// file on the same host. Other values are returned unchanged.
func smallCoverToThumb(raw string) string {
	u, lead, dir, file, ok := splitImageURL(raw)
	if !ok || dir != dirSmallCovers {
		return raw
	}
	return withImageDir(u, lead, dirThumbs, file)
}

// posterURL picks the poster Emby shows in search and identify dialogs. A
// /thumbs/ URL is kept; an empty thumb or one that points at a cover is
// replaced by the /thumbs/ still derived from whichever URL is available.
func posterURL(thumb, cover string) string {
	for _, candidate := range []string{thumb, cover} {
		if strings.TrimSpace(candidate) == "" {
			continue
		}
		u, lead, dir, file, ok := splitImageURL(candidate)
		if !ok {
			return thumb
		}
		switch dir {
		case dirThumbs:
			return candidate
		case dirCovers, dirSmallCovers:
			return withImageDir(u, lead, dirThumbs, file)
		}
	}
	return thumb
}

// imageCandidates lists the URLs Fetch tries for raw, in order. Encrypted app
// CDN URLs are decrypted first. For covers and thumbs the requested
// directory comes first (never small_covers), then /thumbs/, /covers/ and
// finally the original /small_covers/ URL. Anything else is fetched once as
// requested.
func imageCandidates(raw string) []string {
	plain := decryptImageURL(raw)
	u, lead, dir, file, ok := splitImageURL(plain)
	if !ok || (dir != dirSmallCovers && dir != dirThumbs && dir != dirCovers) {
		return []string{raw}
	}
	var out []string
	seen := map[string]bool{}
	add := func(s string) {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	if dir != dirSmallCovers {
		add(withImageDir(u, lead, dir, file))
	}
	add(withImageDir(u, lead, dirThumbs, file))
	add(withImageDir(u, lead, dirCovers, file))
	add(withImageDir(u, lead, dirSmallCovers, file))
	return out
}

// Fetch overrides the embedded fetcher so the image proxy survives the CDN
// rejecting /small_covers/: a 403 or 404 moves on to the next candidate and
// the first 200 is returned. The fetcher closes rejected bodies itself.
func (javdb *JavDB) Fetch(rawURL string) (*http.Response, error) {
	var lastErr error
	for _, candidate := range imageCandidates(rawURL) {
		resp, err := javdb.Fetcher.Fetch(candidate)
		if err == nil {
			return resp, nil
		}
		lastErr = err
		if !isMissingImage(err) {
			return nil, err
		}
	}
	return nil, lastErr
}

func isMissingImage(err error) bool {
	var httpErr *errors.HTTPError
	if !stderrors.As(err, &httpErr) {
		return false
	}
	switch httpErr.StatusCode() {
	case http.StatusForbidden, http.StatusNotFound:
		return true
	}
	return false
}
