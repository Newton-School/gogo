package api

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestOperationCanonicalJSONKeepsExactNumbersAndNormalizesEquivalentForms(t *testing.T) {
	for _, group := range [][]string{{"1", "1.0", "10e-1", "0.1E+1"}, {"0", "-0", "0e999"}, {"1000", "1e3", "100.00e1"}, {"9007199254740993", "9007199254740993.00"}, {"-0.01", "-1e-2"}} {
		var expected string
		for _, number := range group {
			object, err := receiptObject(Values{"nested": []any{Values{"n": json.Number(number)}}})
			if err != nil {
				t.Fatal(err)
			}
			value, err := canonicalOperation(object)
			if err != nil {
				t.Fatal(err)
			}
			digest := operationDigest(value)
			if expected != "" && expected != digest {
				t.Fatal("equivalent number differs", number)
			}
			expected = digest
		}
	}
	first, _ := canonicalOperation(Values{"n": json.Number("9007199254740993")})
	second, _ := canonicalOperation(Values{"n": json.Number("9007199254740992")})
	if operationDigest(first) == operationDigest(second) {
		t.Fatal("integer precision lost")
	}
	for _, number := range []string{"1e999999999999", strings.Repeat("9", 1025)} {
		if _, err := canonicalNumber(json.Number(number)); err == nil {
			t.Fatal("unbounded number accepted")
		}
	}
}

func TestOperationResponseSealingAndCurrentRedactionCannotAddOrChangeData(t *testing.T) {
	response := MutationResponse{Status: 201, Headers: map[string]string{"Location": "/items/1/"}, ObjectKey: Values{"id": 1}, Body: Values{"nested": []any{Values{"id": 1, "private": "not public"}}, "name": "item"}}
	sealed, err := sealMutationResponse(response)
	if err != nil {
		t.Fatal(err)
	}
	response.Headers["Set-Cookie"] = "secret"
	response.Body["name"] = "mutated"
	if sealed.Body["name"] != "item" || sealed.Headers["Set-Cookie"] != "" {
		t.Fatal("response not detached")
	}
	filtered, _ := receiptObject(Values{"nested": []any{Values{"id": 1}}})
	if !isRedaction(sealed.Body, filtered) {
		t.Fatal("nested field removal denied")
	}
	for _, value := range []Values{{"name": "changed"}, {"new": "field"}, {"nested": []any{}}, {"nested": []any{Values{"id": 2}}}} {
		filtered, _ := receiptObject(value)
		if isRedaction(sealed.Body, filtered) {
			t.Fatal("redactor changed response", value)
		}
	}
	for _, headers := range []map[string]string{{"Set-Cookie": "secret"}, {"Location": "//evil.test/"}, {"Location": "https://evil.test/"}, {"ETag": "bad\r\nX-Secret: yes"}, {"ETag": "\"a\x01\""}, {"ETag": "\"a\"b\""}, {"Content-Type": "text/html"}} {
		candidate := sealed
		candidate.Headers = headers
		if _, err := sealMutationResponse(candidate); err == nil {
			t.Fatal("unsafe receipt header accepted")
		}
	}
	for _, value := range []any{Operation{Key: "private-key"}, IdempotencyRecord{KeyDigest: "private-digest", ResponseBody: map[string]any{"private": "body"}}} {
		if encoded, err := json.Marshal(value); err == nil || len(encoded) != 0 {
			t.Fatal("private operation serialized")
		}
		for _, verb := range []string{"%v", "%+v", "%#v", "%s"} {
			if strings.Contains(fmt.Sprintf(verb, value), "private") {
				t.Fatal("private operation formatted")
			}
		}
	}
	for _, value := range []Values{{"name": "bad\xff"}, {"bad\xff": "value"}, {"nested": []any{"bad\xff"}}, {"typed": map[string]string{"name": "bad\xff"}}, {"typed": []string{"bad\xff"}}} {
		if _, err := receiptObject(value); err == nil {
			t.Fatal("malformed UTF-8 replaced silently")
		}
	}
	cycle := Values{}
	cycle["self"] = cycle
	if _, err := receiptObject(cycle); err == nil {
		t.Fatal("cyclic input accepted")
	}
	for _, pair := range [][2]string{{"1e3", "1000"}, {"9007199254740993.0", "9007199254740993"}, {"1.2e-2", "0.012"}, {"-1e-3", "-0.001"}} {
		got, err := canonicalKeyNumber(json.Number(pair[0]))
		if err != nil || string(got) != pair[1] {
			t.Fatal(got, err)
		}
	}
}
