package http

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestConditionalGETAndHEADParseCompleteEntityTagLists(t *testing.T) {
	for _, test := range []struct {
		name, tag string
		lines     []string
		match     bool
	}{
		{"absent", `"current"`, nil, false},
		{"strong", `"current"`, []string{`"current"`}, true},
		{"weak_request", `"current"`, []string{`W/"current"`}, true},
		{"weak_response", `W/"current"`, []string{`"current"`}, true},
		{"both_weak", `W/"current"`, []string{`W/"current"`}, true},
		{"second_line", `"current"`, []string{`"other"`, `"current"`}, true},
		{"second_member", `"current"`, []string{`"other", "current"`}, true},
		{"opaque_comma", `"revision,one"`, []string{`"other", W/"revision,one"`}, true},
		{"literal_backslash", `"revision\one"`, []string{`W/"revision\one"`}, true},
		{"no_unescape", `"revision\one"`, []string{`"revision\\one"`}, false},
		{"opaque_obstext", "\"a\xff\"", []string{"W/\"a\xff\""}, true},
		{"empty_tag", `""`, []string{`W/""`}, true},
		{"empty_members", `"current"`, []string{`,, "other",`, `,W/"current",,`}, true},
		{"star", `"current"`, []string{" \t*\t "}, true},
		{"star_without_tag", "", []string{"*"}, true},
		{"empty_list", `"current"`, []string{""}, false},
		{"only_empty_members", `"current"`, []string{",, \t,"}, false},
		{"case_sensitive", `"Current"`, []string{`"current"`}, false},
		{"no_server_tag", "", []string{`"current"`}, false},
		{"invalid_prefix", `"current"`, []string{`bad, "current"`}, false},
		{"invalid_suffix", `"current"`, []string{`"current", bad`}, false},
		{"malformed_second_line", `"current"`, []string{`"current"`, "bad"}, false},
		{"star_with_tag", `"current"`, []string{`*, "current"`}, false},
		{"tag_with_star", `"current"`, []string{`"current", *`}, false},
		{"repeated_star", `"current"`, []string{"*", "*"}, false},
		{"star_and_empty", `"current"`, []string{"*,"}, false},
		{"lowercase_weak", `"current"`, []string{`w/"current"`}, false},
		{"weak_with_space", `"current"`, []string{`W/ "current"`}, false},
		{"space_inside", `"current"`, []string{`"current", "not allowed"`}, false},
		{"tab_inside", `"current"`, []string{"\"current\", \"not\tallowed\""}, false},
		{"nul_inside", `"current"`, []string{"\"current\", \"not\x00allowed\""}, false},
		{"del_inside", `"current"`, []string{"\"current\", \"not\x7fallowed\""}, false},
		{"unterminated", `"current"`, []string{`"current", "unterminated`}, false},
		{"quoted_pair_is_not_escape", `"current"`, []string{`"current", "not\"a-tag"`}, false},
		{"missing_separator", `"current"`, []string{`"current" "other"`}, false},
		{"unquoted_current_is_invalid", "current", []string{"current"}, false},
		{"current_suffix_is_invalid", `"current"suffix`, []string{`"current"`}, false},
		{"current_space_is_invalid", `"not allowed"`, []string{`"not allowed"`}, false},
		{"current_lowercase_weak_is_invalid", `w/"current"`, []string{`"current"`}, false},
	} {
		for _, method := range []string{"GET", "HEAD"} {
			t.Run(test.name+"/"+method, func(t *testing.T) {
				response := Text(200, "authorized representation")
				response.ETag = test.tag
				response.Headers.Set("Cache-Control", "private, no-store")
				response.Headers.Set("Vary", "Authorization, Cookie")
				response.Headers.Set("X-Content-Type-Options", "nosniff")
				r := httptest.NewRequest(method, "/", nil)
				if test.lines != nil {
					r.Header["If-None-Match"] = test.lines
				}
				w := httptest.NewRecorder()
				if err := response.Write(w, r); err != nil {
					t.Fatal(err)
				}
				want := 200
				if test.match {
					want = 304
				}
				if w.Code != want {
					t.Fatalf("status=%d want=%d", w.Code, want)
				}
				if method == "HEAD" || test.match {
					if w.Body.Len() != 0 {
						t.Fatal("conditional/HEAD response contains a body")
					}
				} else if w.Body.String() != "authorized representation" {
					t.Fatal("full representation was not sent")
				}
				for _, name := range []string{"Cache-Control", "Vary", "X-Content-Type-Options"} {
					if w.Header().Get(name) != response.Headers.Get(name) {
						t.Fatal("security/cache metadata lost", name)
					}
				}
			})
		}
	}
}

func TestConditionalGETBoundedParsingNeverUsesAPartialMatch(t *testing.T) {
	for _, test := range []struct {
		name, tag string
		lines     []string
		match     bool
	}{
		{"exact_bytes", `"` + strings.Repeat("a", 4094) + `"`, []string{`"` + strings.Repeat("a", 4094) + `"`}, true},
		{"over_bytes", `"current"`, []string{`"current", "` + strings.Repeat("a", 4096) + `"`}, false},
		{"combined_exact_bytes", `"current"`, []string{`"current"`, `"` + strings.Repeat("a", 4084) + `"`}, true},
		{"combined_over_bytes", `"current"`, []string{`"current"`, `"` + strings.Repeat("a", 4085) + `"`}, false},
		{"exact_lines", `"current"`, append(strings.Split(strings.Repeat("\"other\"\n", 31), "\n")[:31], `"current"`), true},
		{"over_lines", `"current"`, append(strings.Split(strings.Repeat("\"other\"\n", 32), "\n")[:32], `"current"`), false},
		{"exact_members", `"current"`, []string{`"current"` + strings.Repeat(`,"other"`, 63)}, true},
		{"over_members", `"current"`, []string{`"current"` + strings.Repeat(`,"other"`, 64)}, false},
		{"empty_member_budget", `"current"`, []string{`"current"` + strings.Repeat(",", 65)}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := Text(200, "full")
			response.ETag = test.tag
			r := httptest.NewRequest("GET", "/", nil)
			r.Header["If-None-Match"] = test.lines
			w := httptest.NewRecorder()
			if err := response.Write(w, r); err != nil {
				t.Fatal(err)
			}
			if (w.Code == 304) != test.match {
				t.Fatal("partial or over-budget condition used", w.Code)
			}
		})
	}
}

func FuzzConditionalTagListBoundaries(f *testing.F) {
	for _, seed := range []string{`"current"`, `W/"current"`, `"other", "current"`, `"comma,tag"`, `"back\slash"`, "\"a\xff\"", ""} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, value string) {
		// Every generated list, including one containing an early match, must
		// be rejected as a cache hint once any subsequent member is invalid.
		if matchesIfNoneMatch([]string{value, "invalid"}, `"current"`) {
			t.Fatal("accepted a partially valid list")
		}
		if matchesIfNoneMatch([]string{`"current"`, value, strings.Repeat("a", 4097)}, `"current"`) {
			t.Fatal("accepted an over-budget list")
		}
		// A valid single tag's weak prefix does not change byte comparison.
		tag, rest, valid := takeEntityTag(value)
		if valid && rest == "" && len(tag)+2 <= 4096 {
			if !matchesIfNoneMatch([]string{"W/" + tag}, tag) || !matchesIfNoneMatch([]string{tag}, "W/"+tag) {
				t.Fatal("weak comparison changed the opaque tag")
			}
		}
	})
}

func TestConditionalGETTagPresenceSuppressesDateFallback(t *testing.T) {
	modified := time.Date(2026, 1, 1, 0, 0, 0, 321, time.UTC)
	for _, test := range []struct {
		name        string
		tags, dates []string
		match       bool
	}{
		{"date_only", nil, []string{modified.Format(http.TimeFormat)}, true},
		{"date_newer", nil, []string{modified.Add(time.Hour).Format(http.TimeFormat)}, true},
		{"date_older", nil, []string{modified.Add(-time.Hour).Format(http.TimeFormat)}, false},
		{"empty_tag", []string{""}, []string{modified.Format(http.TimeFormat)}, false},
		{"invalid_tag", []string{"bad"}, []string{modified.Format(http.TimeFormat)}, false},
		{"different_tag", []string{`"other"`}, []string{modified.Format(http.TimeFormat)}, false},
		{"matching_tag_older_date", []string{`"current"`}, []string{modified.Add(-time.Hour).Format(http.TimeFormat)}, true},
		{"duplicate_date", nil, []string{modified.Format(http.TimeFormat), modified.Format(http.TimeFormat)}, false},
		{"invalid_date", nil, []string{"bad"}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := Text(200, "full")
			response.ETag, response.LastModified = `"current"`, modified
			r := httptest.NewRequest("GET", "/", nil)
			r.Header["If-None-Match"], r.Header["If-Modified-Since"] = test.tags, test.dates
			w := httptest.NewRecorder()
			if err := response.Write(w, r); err != nil {
				t.Fatal(err)
			}
			if (w.Code == 304) != test.match {
				t.Fatal("date/etag precedence violated", w.Code)
			}
		})
	}
}

func TestConditionalResponseNeverReplacesDenialFailureOrUnsafeMethods(t *testing.T) {
	for _, status := range []int{201, 204, 301, 400, 403, 404, 500} {
		response := Text(status, "result")
		response.ETag = `"current"`
		r := httptest.NewRequest("GET", "/", nil)
		r.Header.Set("If-None-Match", "*")
		w := httptest.NewRecorder()
		if err := response.Write(w, r); err != nil || w.Code != status {
			t.Fatal(status, w.Code, err)
		}
	}
	for _, method := range []string{"POST", "PUT", "PATCH", "DELETE", "OPTIONS"} {
		response := Text(200, "result")
		response.ETag = `"current"`
		r := httptest.NewRequest(method, "/", nil)
		r.Header.Set("If-None-Match", "*")
		w := httptest.NewRecorder()
		if err := response.Write(w, r); err != nil || w.Code != 200 || w.Body.String() != "result" {
			t.Fatal(method, w.Code, err)
		}
	}
	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Set("If-None-Match", "*")
	w := httptest.NewRecorder()
	called := false
	response := Response{ETag: `"current"`, Render: func(context.Context) ([]byte, error) { called = true; return nil, errors.New("render failed") }}
	if response.Write(w, r) == nil || !called || w.Body.Len() != 0 || w.Header().Get("ETag") != "" {
		t.Fatal("conditional hint bypassed rendering failure")
	}
}
