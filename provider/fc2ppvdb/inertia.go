package fc2ppvdb

import (
	"encoding/json"
	"fmt"
	"strings"
)

// The site (fc2cmadb.com) is a Laravel + Inertia.js (React) application.
// Every HTML page response embeds the complete page state as a JSON document
// in the "data-page" HTML attribute of the #app root element, for example:
//
//	<div id="app" data-page="{&quot;component&quot;:&quot;Articles/Show&quot;,
//	     &quot;props&quot;:{&quot;article&quot;:{...}},&quot;url&quot;:&quot;/articles/123&quot;}">
//
// The article metadata is read from that JSON instead of the rendered DOM,
// which is more stable against front-end redesigns. The field names below
// were verified against the production frontend bundle (build/assets/
// app-BkK2ozc3.js, deploy of 2026-08-13).
type inertiaPage struct {
	Component string `json:"component"`
	Props     struct {
		Article   *article   `json:"article"`
		Actresses []*actress `json:"actresses"`
	} `json:"props"`
	URL     string `json:"url"`
	Version string `json:"version"`
}

// article is the "article" prop of the Articles/Show page.
type article struct {
	ID          flexString `json:"id"`
	VideoID     flexString `json:"video_id"`
	Title       string     `json:"title"`
	ImageURL    flexString `json:"image_url"`
	NotFound    bool       `json:"not_found"`
	Censored    flexString `json:"censored"`
	ReleaseDate flexString `json:"release_date"`
	Duration    flexString `json:"duration"`
	Writer      *struct {
		Name flexString `json:"name"`
		Slug flexString `json:"slug"`
	} `json:"writer"`
	Tags []struct {
		Name flexString `json:"name"`
	} `json:"tags"`
	AffiliateLinks []struct {
		Name     flexString `json:"name"`
		URL      flexString `json:"url"`
		SiteName flexString `json:"site_name"`
	} `json:"affiliate_links"`
}

// actress is an item of the "actresses" prop.
type actress struct {
	ID   flexString `json:"id"`
	Name flexString `json:"name"`
}

// flexString is a JSON string field that also tolerates numeric and null
// values (the server is not strict about the type of id-like fields).
type flexString string

func (s *flexString) UnmarshalJSON(b []byte) error {
	if string(b) == "null" {
		*s = ""
		return nil
	}
	switch b[0] {
	case '"':
		var v string
		if err := json.Unmarshal(b, &v); err != nil {
			return err
		}
		*s = flexString(v)
		return nil
	case '-', '0', '1', '2', '3', '4', '5', '6', '7', '8', '9':
		*s = flexString(strings.TrimSpace(string(b)))
		return nil
	default:
		return fmt.Errorf("fc2ppvdb: unexpected JSON value %q", string(b))
	}
}
