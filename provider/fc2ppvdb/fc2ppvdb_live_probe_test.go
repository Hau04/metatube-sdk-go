package fc2ppvdb

// Opt-in live diagnostic test. It is NOT part of the regular test suite:
// it only runs when FC2PPVDB_PROBE=1 is set, so CI and normal local runs
// stay hermetic. Use it to inspect the live anonymous data contract of
// fc2cmadb.com (raw Inertia page state + provider result):
//
//	FC2PPVDB_PROBE=1 go test ./provider/fc2ppvdb/ -run TestFC2PPVDB_LiveProbe -v

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
	if os.Getenv("FC2PPVDB_PROBE") != "1" {
		t.Skip("set FC2PPVDB_PROBE=1 to run the live probe")
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

	// Exercise the actual provider code path (colly + parsing).
	info, err := New().GetMovieInfoByID("4925979")
	if err != nil {
		t.Fatalf("provider failed: %v", err)
	}
	data, err := json.MarshalIndent(info, "", "  ")
	require.NoError(t, err)
	t.Logf("provider result:\n%s", data)
}
