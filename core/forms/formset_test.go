package forms

import (
	"context"
	"net/url"
	"testing"
)

func TestFormSetBoundsBeforeAllocation(t *testing.T) {
	values := url.Values{"form-TOTAL_FORMS": {"999999999"}, "form-INITIAL_FORMS": {"0"}}
	s, err := BindFormSet(context.Background(), nil, values, FormSetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if s.IsValid() || len(s.Forms) != 0 {
		t.Fatal("unbounded forms allocated")
	}
}
func TestFormSetRejectsDuplicateAndForeignIdentity(t *testing.T) {
	for _, id := range []string{"outside", "one"} {
		values := url.Values{"form-TOTAL_FORMS": {"2"}, "form-INITIAL_FORMS": {"2"}, "form-0-id": {"one"}, "form-1-id": {id}}
		s, err := BindFormSet(context.Background(), nil, values, FormSetOptions{Initial: []map[string]any{{}, {}}, ExistingIDs: []string{"one", "two"}})
		if err != nil {
			t.Fatal(err)
		}
		if s.IsValid() {
			t.Fatal("forged identities accepted")
		}
	}
}

func TestReorderedRowsKeepTheirTrustedInitialValues(t *testing.T) {
	field := NewField("role", Char)
	field.Disabled = true
	values := url.Values{"form-TOTAL_FORMS": {"2"}, "form-INITIAL_FORMS": {"2"}, "form-0-id": {"two"}, "form-1-id": {"one"}}
	set, err := BindFormSet(context.Background(), []Field{field}, values, FormSetOptions{Initial: []map[string]any{{"role": "first"}, {"role": "second"}}, ExistingIDs: []string{"one", "two"}})
	if err != nil || !set.IsValid() {
		t.Fatal(set, err)
	}
	if set.Forms[0].CleanedData()["role"] != "second" || set.Forms[1].CleanedData()["role"] != "first" {
		t.Fatal("initial data followed index instead of trusted row identity")
	}
}
func TestEmptyScopedFormsetRejectsInjectedExistingID(t *testing.T) {
	values := url.Values{"form-TOTAL_FORMS": {"1"}, "form-INITIAL_FORMS": {"0"}, "form-0-id": {"foreign"}}
	set, err := BindFormSet(context.Background(), nil, values, FormSetOptions{ExistingIDs: []string{}})
	if err != nil || set.IsValid() {
		t.Fatal(set, err)
	}
}
