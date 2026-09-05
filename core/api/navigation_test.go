package api

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNavigationUsesTheSameQueryBudgetAsRequests(t *testing.T) {
	valid := "?" + strings.Repeat("x", maxResourceQueryBytes)
	if err := validateNavigation(valid, valid); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest("GET", "https://example.test/"+valid, nil)
	if _, err := readQuery(request); err != nil {
		t.Fatal("link budget differs from request budget", err)
	}
	for _, pair := range [][2]string{{valid + "x", ""}, {"", valid + "x"}} {
		if err := validateNavigation(pair[0], pair[1]); err == nil {
			t.Fatal("generated an unusable over-budget link")
		}
	}
}
