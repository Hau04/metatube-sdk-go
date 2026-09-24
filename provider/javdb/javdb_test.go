package javdb

import (
	"testing"

	"github.com/metatube-community/metatube-sdk-go/provider/internal/testkit"
)

// These tests talk to the live JavDB app API. They are skipped under GitHub
// Actions by the testkit, so run them locally.
//
// The API is public: it is authorised by a computed request signature rather
// than by an account, so no token or cookie is needed.
func TestJavDB_GetMovieInfoByID(t *testing.T) {
	testkit.Test(t, New, []string{
		"82BkzE",      // FC2-4925979 by internal id
		"FC2-4925979", // the same movie by its printed number
		"4925979",     // and by bare digits, which match through the FC2 prefix
	})
}

func TestJavDB_GetMovieInfoByURL(t *testing.T) {
	testkit.Test(t, New, []string{
		"https://javdb.com/v/82BkzE",
	})
}
