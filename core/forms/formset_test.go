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
