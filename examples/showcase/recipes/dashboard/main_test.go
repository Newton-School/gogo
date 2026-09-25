package main

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPreviewIsClearlyLabeledAndHostBound(t *testing.T) {
	h, err := preview()
	if err != nil {
		t.Fatal(err)
	}
	for _, host := range []string{"127.0.0.1:5555", "attacker.example:5555"} {
		r := httptest.NewRequest("GET", "http://"+host+"/async/", nil)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if host == "127.0.0.1:5555" {
			if w.Code != 200 || !strings.Contains(w.Body.String(), "Demo data") {
				t.Fatal(w.Code, w.Body.String())
			}
		} else if w.Code != 403 {
			t.Fatal("foreign host accepted")
		}
	}
}
