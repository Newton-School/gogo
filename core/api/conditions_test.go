package api

import (
	"net/http"
	"strings"
	"testing"
)

func TestIfMatchUsesBoundedStrongEntityTagLists(t *testing.T) {
	for _, test := range []struct {
		headers []string
		tag     string
		matches bool
	}{
		{nil, `"x"`, true}, {[]string{"*"}, `"x"`, true}, {[]string{`"x"`}, `"x"`, true},
		{[]string{`W/"x"`}, `"x"`, false}, {[]string{`W/"x", "y"`}, `"y"`, true},
		{[]string{`"a,b", "x"`}, `"a,b"`, true}, {[]string{`"z"`, ` "x" `}, `"x"`, true},
		{[]string{`, , "x",`}, `"x"`, true}, {[]string{``}, `"x"`, false},
		{[]string{`"a\b"`}, `"a\b"`, true}, {[]string{`""`}, `""`, true},
	} {
		h := http.Header{}
		if test.headers != nil {
			h["If-Match"] = test.headers
		}
		condition, err := parseIfMatch(h)
		if err != nil || condition.matches(test.tag) != test.matches || condition.present != (test.headers != nil) {
			t.Fatal(test, condition, err)
		}
	}
	for _, invalid := range []string{`*, "x"`, `"x", *`, `w/"x"`, `"x"junk`, `"x`, `x`, "\"x\x7f\"", "\"x\t\"", "\"x\n\"", strings.Repeat(",", 65), strings.Repeat("x", 4097)} {
		if _, err := parseIfMatch(http.Header{"If-Match": {invalid}}); err == nil {
			t.Fatal("invalid entity condition accepted", invalid)
		}
	}
}

func TestRepresentationTagsUseExactPublicJSONBytes(t *testing.T) {
	first, err := representationTag(Values{"id": 1, "name": "public"})
	if err != nil || !validReceiptETag(first) {
		t.Fatal(first, err)
	}
	same, _ := representationTag(Values{"name": "public", "id": 1})
	changed, _ := representationTag(Values{"id": 1})
	if first != same || first == changed {
		t.Fatal("tag does not follow representation bytes")
	}
}
