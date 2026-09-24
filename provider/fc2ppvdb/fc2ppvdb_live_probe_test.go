package fc2ppvdb

// NOTE: temporary diagnostic test, enabled only in GitHub Actions. It
// verifies the provider against the live site from a CI runner and logs the
// raw Inertia page state so the anonymous data contract (image_url,
// actresses, video_id type, ...) can be inspected in the CI logs. This file
// is meant to be removed after the verification is done.

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestFC2PPVDB_LiveProbe(t *testing.T) {
	if os.Getenv("GITHUB_ACTIONS") != "true" {
		t.Skip("live probe runs only in GitHub Actions")
	}

	client := &http.Client{Timeout: 30 * time.Second}
	req, err := http.NewRequest(http.MethodGet, "https://fc2cmadb.com/articles/4925979", nil)
	require.NoError(t, err)
	req.Header.Set("User-Agent",
		"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36")
	resp, err := client.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	t.Logf("http status=%d content-type=%s size=%d server=%s cf-ray=%s",
		resp.StatusCode, resp.Header.Get("Content-Type"), len(body),
		resp.Header.Get("Server"), resp.Header.Get("CF-Ray"))
	require.Equal(t, http.StatusOK, resp.StatusCode)

	// Extract the data-page attribute (HTML-escaped JSON). Since the JSON is
	// HTML-escaped (&quot;), there are no raw double quotes inside the value,
	// so a plain [^"]* match is safe.
	match := regexp.MustCompile(`data-page="([^"]*)"`).FindSubmatch(body)
	require.NotNil(t, match, "data-page attribute not found in live page")

	dataPage := strings.ReplaceAll(string(match[1]), "&quot;", `"`)
	t.Logf("data-page size=%d", len(dataPage))

	var raw struct {
		Component string `json:"component"`
		Props     struct {
			Article   json.RawMessage `json:"article"`
			Actresses json.RawMessage `json:"actresses"`
		} `json:"props"`
		URL     string `json:"url"`
		Version string `json:"version"`
	}
	require.NoError(t, json.Unmarshal([]byte(dataPage), &raw))
	t.Logf("component=%s url=%s version=%s", raw.Component, raw.URL, raw.Version)
	t.Logf("article prop: %s", string(raw.Props.Article))
	t.Logf("actresses prop: %s", string(raw.Props.Actresses))

	// Exercise the actual provider code path.
	info, err := New().GetMovieInfoByID("4925979")
	if err != nil {
		t.Fatalf("provider failed: %v", err)
	}
	data, err := json.MarshalIndent(info, "", "  ")
	require.NoError(t, err)
	t.Logf("provider result:\n%s", data)
}
