package javdb

import (
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRewriteImageURL_SmallCoversToThumbs(t *testing.T) {
	isolateWebImagePrefix(t)
	setWebImagePrefix("https://c0.jdbstatic.com")

	for name, tt := range map[string]struct{ in, want string }{
		"encrypted cmastd small cover": {
			"https://tp-iu.cmastd.com/rhe951l4q/small_covers/o9/O9AvB.jpg",
			"https://c0.jdbstatic.com/thumbs/o9/O9AvB.jpg",
		},
		"encrypted spfcas small cover other token": {
			"https://tp.spfcas.com/abc123xyz/small_covers/o9/O9AvB.jpg",
			"https://c0.jdbstatic.com/thumbs/o9/O9AvB.jpg",
		},
		"plain small cover keeps host": {
			"https://c0.jdbstatic.com/small_covers/o9/O9AvB.jpg",
			"https://c0.jdbstatic.com/thumbs/o9/O9AvB.jpg",
		},
		"plain small cover other host": {
			"https://c1.jdbstatic.com/small_covers/o9/O9AvB.jpg",
			"https://c1.jdbstatic.com/thumbs/o9/O9AvB.jpg",
		},
		"cover untouched": {
			"https://c0.jdbstatic.com/covers/o9/O9AvB.jpg",
			"https://c0.jdbstatic.com/covers/o9/O9AvB.jpg",
		},
	} {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, tt.want, rewriteImageURL(tt.in))
		})
	}
}

func TestPosterURL(t *testing.T) {
	const cover = "https://c0.jdbstatic.com/covers/o9/O9AvB.jpg"
	const thumb = "https://c0.jdbstatic.com/thumbs/o9/O9AvB.jpg"
	assert.Equal(t, thumb, posterURL(thumb, cover))
	assert.Equal(t, thumb, posterURL("", cover))
	assert.Equal(t, thumb, posterURL(cover, cover))
	assert.Equal(t, thumb, posterURL("https://c0.jdbstatic.com/small_covers/o9/O9AvB.jpg", cover))
	assert.Equal(t, "https://cdn.example/x.jpg", posterURL("https://cdn.example/x.jpg", cover))
	assert.Equal(t, "", posterURL("", ""))
}

func TestImageCandidates(t *testing.T) {
	isolateWebImagePrefix(t)
	setWebImagePrefix("https://c0.jdbstatic.com")

	const (
		thumbs = "https://c0.jdbstatic.com/thumbs/o9/O9AvB.jpg"
		covers = "https://c0.jdbstatic.com/covers/o9/O9AvB.jpg"
		small  = "https://c0.jdbstatic.com/small_covers/o9/O9AvB.jpg"
	)
	assert.Equal(t, []string{thumbs, covers, small}, imageCandidates(small))
	assert.Equal(t, []string{thumbs, covers, small},
		imageCandidates("https://tp-iu.cmastd.com/rhe951l4q/small_covers/o9/O9AvB.jpg"))
	assert.Equal(t, []string{thumbs, covers, small}, imageCandidates(thumbs))
	assert.Equal(t, []string{covers, thumbs, small}, imageCandidates(covers))

	sample := "https://c0.jdbstatic.com/samples/o9/O9AvB_l_0.jpg"
	assert.Equal(t, []string{sample}, imageCandidates(sample))
	avatar := "https://c0.jdbstatic.com/avatars/83/83V.jpg"
	assert.Equal(t, []string{avatar}, imageCandidates(avatar))
	other := "https://example.com/preview.mp4"
	assert.Equal(t, []string{other}, imageCandidates(other))
}

func TestFetch_FallsBackFromSmallCovers(t *testing.T) {
	var (
		mu       sync.Mutex
		paths    []string
		referers []string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.URL.Path)
		referers = append(referers, r.Referer())
		mu.Unlock()
		switch r.URL.Path {
		case "/small_covers/o9/O9AvB.jpg":
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"error":"forbidden"}`))
		case "/thumbs/o9/O9AvB.jpg":
			http.NotFound(w, r)
		case "/covers/o9/O9AvB.jpg":
			w.Header().Set("Content-Type", "image/jpeg")
			_, _ = w.Write([]byte("cover-bytes"))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	resp, err := New().Fetch(srv.URL + "/small_covers/o9/O9AvB.jpg")
	require.NoError(t, err)
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.Equal(t, "cover-bytes", string(body))
	assert.Equal(t, "image/jpeg", resp.Header.Get("Content-Type"))

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, []string{"/thumbs/o9/O9AvB.jpg", "/covers/o9/O9AvB.jpg"}, paths,
		"small_covers must not be tried before thumbs and covers")
	for _, ref := range referers {
		assert.Equal(t, "https://javdb.com/", ref)
	}
}

func TestFetch_AllMissingReturnsError(t *testing.T) {
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		w.WriteHeader(http.StatusForbidden)
	}))
	t.Cleanup(srv.Close)

	_, err := New().Fetch(srv.URL + "/small_covers/o9/O9AvB.jpg")
	require.Error(t, err)
	assert.Equal(t, int32(3), n.Load())
}

func TestFetch_SampleFetchedOnce(t *testing.T) {
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)

	_, err := New().Fetch(srv.URL + "/samples/o9/O9AvB_l_0.jpg")
	require.Error(t, err)
	assert.Equal(t, int32(1), n.Load())
}
