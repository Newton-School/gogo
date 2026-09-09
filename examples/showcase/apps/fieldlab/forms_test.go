package fieldlab_test

import (
	"context"
	"mime/multipart"
	"net/url"
	"strings"
	"testing"

	"example.com/gogo-showcase/apps/fieldlab"
	"github.com/Newton-School/gogo/core/forms"
)

func TestEveryFormKindAcceptsValidAndRejectsInvalidInput(t *testing.T) {
	cases := fieldlab.FormCases()
	if len(cases) != 29 {
		t.Fatalf("form inventory changed: %d", len(cases))
	}
	files, err := fieldlab.ValidFiles()
	if err != nil {
		t.Fatal(err)
	}
	seen := map[forms.Kind]bool{}
	for _, example := range cases {
		t.Run(string(example.Field.Kind), func(t *testing.T) {
			if seen[example.Field.Kind] {
				t.Fatal("duplicate form kind")
			}
			seen[example.Field.Kind] = true
			good, err := forms.New([]forms.Field{example.Field}, forms.WithContext(t.Context()), forms.WithData(example.Valid), forms.WithFiles(files))
			if err != nil {
				t.Fatal(err)
			}
			if !good.IsValid() {
				t.Fatalf("valid example: %v", good.Errors())
			}
			if _, ok := good.CleanedData()[example.Field.Name]; !ok {
				t.Fatal("cleaned result missing")
			}
			bad, err := forms.New([]forms.Field{example.Field}, forms.WithData(example.Invalid))
			if err != nil {
				t.Fatal(err)
			}
			if bad.IsValid() || len(bad.Errors()[example.Field.Name]) == 0 {
				t.Fatal("invalid input needs a field error")
			}
			if _, ok := bad.CleanedData()[example.Field.Name]; ok {
				t.Fatal("invalid field leaked into cleaned data")
			}
			for _, layout := range []string{"div", "p", "ul", "table"} {
				if rendered, err := bad.Render(layout); err != nil || rendered == "" {
					t.Fatalf("%s: %q %v", layout, rendered, err)
				}
			}
		})
	}
}

func TestCombinedFormUnboundBoundAndMultipartValidation(t *testing.T) {
	unbound, err := fieldlab.Form()
	if err != nil {
		t.Fatal(err)
	}
	if unbound.IsBound() || unbound.IsValid() || len(unbound.Errors()) != 0 {
		t.Fatal("initial values must not imply submitted or validated data")
	}
	files, err := fieldlab.ValidFiles()
	if err != nil {
		t.Fatal(err)
	}
	bound, err := fieldlab.Form(forms.WithData(fieldlab.ValidValues()), forms.WithFiles(files))
	if err != nil || !bound.IsValid() {
		t.Fatalf("full field form: %v %v", err, bound.Errors())
	}
	if len(bound.CleanedData()) != 29 {
		t.Fatal("not every field produced cleaned data")
	}
	imageField := forms.NewField("image", forms.Image)
	imageField.MaxBytes = 4096
	invalid := []*multipart.FileHeader{
		{Filename: "empty.png", Size: 0},
		{Filename: "oversized.png", Size: 4097},
		{Filename: "../escape.png", Size: 10},
		files["file"][0], // Valid text is not a valid image.
	}
	for _, file := range invalid {
		form, err := forms.New([]forms.Field{imageField}, forms.WithFiles(map[string][]*multipart.FileHeader{"image": {file}}))
		if err != nil || form.IsValid() {
			t.Fatalf("bad image accepted: %v", err)
		}
	}
}

func TestFormOptionsDisabledPrefixCleanAndPasswordRedisplay(t *testing.T) {
	password := forms.NewField("password", forms.Char)
	preserve := false
	password.Strip, password.Widget = &preserve, forms.InputWidget{Type: "password"}
	locked := forms.NewField("locked", forms.Char)
	locked.Disabled, locked.Initial = true, "server-owned"
	form, err := forms.New([]forms.Field{password, locked}, forms.WithPrefix("demo"), forms.WithData(url.Values{
		"demo-password": {"  fictional input  "}, "demo-locked": {"forged"}, "undeclared": {"ignored"},
	}), forms.WithClean(func(f *forms.Form) error {
		return nil
	}))
	if err != nil || !form.IsValid() {
		t.Fatalf("options: %v", err)
	}
	data := form.CleanedData()
	if data["password"] != "  fictional input  " || data["locked"] != "server-owned" || len(data) != 2 {
		t.Fatal("trusted initial, whitespace or allowlist behavior differs")
	}
	if !form.HasChanged() {
		t.Fatal("changed data not recorded")
	}
	html, err := form.Render("div")
	if err != nil || strings.Contains(string(html), "fictional input") || strings.Contains(string(html), "forged") {
		t.Fatal("password or forged disabled value was redisplayed")
	}
	bad, err := forms.New([]forms.Field{forms.NewField("value", forms.Integer)}, forms.WithData(url.Values{"value": {"1"}}),
		forms.WithClean(func(_ *forms.Form) error { return forms.Error{Code: "policy", Message: "Demo cross-field rejection."} }))
	if err != nil || bad.IsValid() || !bad.HasError(forms.NonFieldErrors, "policy") {
		t.Fatal("form-wide clean did not reject")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	canceled, err := forms.New([]forms.Field{forms.NewField("value", forms.Integer)}, forms.WithContext(ctx), forms.WithData(url.Values{"value": {"1"}}))
	if err != nil || canceled.IsValid() {
		t.Fatal("canceled validation must not succeed")
	}
}

func TestBoundedFormsetRejectsForgedManagementCounts(t *testing.T) {
	fields := []forms.Field{forms.NewField("name", forms.Char)}
	options := forms.FormSetOptions{Prefix: "items", Minimum: 1, Maximum: 3, AbsoluteMaximum: 3, CanOrder: true, CanDelete: true}
	values := url.Values{"items-TOTAL_FORMS": {"2"}, "items-INITIAL_FORMS": {"0"}, "items-0-name": {"First"}, "items-1-name": {"Second"}, "items-0-ORDER": {"2"}, "items-1-ORDER": {"1"}}
	set, err := forms.BindFormSet(t.Context(), fields, values, options)
	if err != nil || !set.IsValid() || len(set.Ordered) != 2 || set.Ordered[0] != 1 {
		t.Fatalf("bounded ordered formset: %v", err)
	}
	values.Set("items-TOTAL_FORMS", "1000000")
	set, err = forms.BindFormSet(t.Context(), fields, values, options)
	if err != nil || set.IsValid() || len(set.Forms) != 0 {
		t.Fatal("forged management count must fail before row allocation")
	}
}
