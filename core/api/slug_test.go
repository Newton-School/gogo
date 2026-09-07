package api_test

import (
	"context"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Newton-School/gogo/core/api"
	"github.com/Newton-School/gogo/core/forms"
	"github.com/Newton-School/gogo/core/migrations"
	"github.com/Newton-School/gogo/core/models"
)

func TestSlugAlphabetAgreesAcrossModelsFormsAndSerializers(t *testing.T) {
	for _, allow := range []bool{false, true} {
		for _, test := range []struct {
			name, value    string
			ascii, unicode bool
		}{
			{"ascii", "Article_42-one", true, true},
			{"latin", "café", false, true},
			{"letters_numbers", "中文１２", false, true},
			{"numeric_categories", "Ⅻ²", false, true},
			{"astral_letter", "𐐀", false, true},
			{"combining_mark", "e\u0301", false, false},
			{"joiner", "a\u200db", false, false},
			{"emoji", "a😀", false, false},
			{"internal_space", "two words", false, false},
			{"path", "a/b", false, false},
			{"dot", "a.b", false, false},
			{"nul", "a\x00b", false, false},
			{"invalid_utf8", "a\xffb", false, false},
			{"empty", "", false, false},
		} {
			mode := "ascii/"
			if allow {
				mode = "unicode/"
			}
			t.Run(mode+test.name, func(t *testing.T) {
				want := test.ascii || allow && test.unicode
				metadata := models.SlugField("slug", models.WithAllowUnicode(allow))
				cleaned, err := metadata.Clean(context.Background(), test.value)
				if (err == nil) != want || want && cleaned != test.value {
					t.Fatal("model changed alphabet or value", cleaned, err)
				}
				field := forms.NewField("slug", forms.Slug)
				field.AllowUnicode = allow
				form, err := forms.New([]forms.Field{field}, forms.WithData(url.Values{"slug": {test.value}}))
				if err != nil || form.IsValid() != want || want && form.CleanedData()["slug"] != test.value {
					t.Fatal("plain form disagrees", err, form.Errors())
				}
				serializer, err := api.New(api.Definition{Fields: []api.Field{api.SlugField("slug", models.WithAllowUnicode(allow))}})
				if err != nil {
					t.Fatal(err)
				}
				values, err := serializer.Validate(context.Background(), api.Values{"slug": test.value}, api.BindOptions{})
				if (err == nil) != want || want && values["slug"] != test.value {
					t.Fatal("plain API disagrees", values, err)
				}
				schema := models.Schema{AppLabel: "shop", Name: "Article", Fields: []models.Field{models.BigAutoField("id"), metadata}}
				record, err := models.NewRecord(schema)
				if err != nil {
					t.Fatal(err)
				}
				modelForm, err := forms.NewModelForm(context.Background(), record, forms.ModelFormOptions{Fields: []string{"slug"}}, forms.WithData(url.Values{"slug": {test.value}}))
				if err != nil || modelForm.IsValid() != want {
					t.Fatal("model form disagrees", err, modelForm.Errors())
				}
				derived, err := api.FromModel(schema, api.ModelOptions{Fields: []string{"slug"}})
				if err != nil {
					t.Fatal(err)
				}
				values, err = derived.Validate(context.Background(), api.Values{"slug": test.value}, api.BindOptions{})
				if (err == nil) != want || want && values["slug"] != test.value {
					t.Fatal("model API disagrees", values, err)
				}
			})
		}
	}
}

func TestSlugFormStripPresenceAndUnicodeLength(t *testing.T) {
	for _, strip := range []*bool{nil, new(true), new(false)} {
		field := forms.NewField("slug", forms.Slug)
		field.AllowUnicode, field.Strip = true, strip
		field.MaxLength = 4
		form, err := forms.New([]forms.Field{field}, forms.WithData(url.Values{"slug": {" \tcafé\n "}}))
		if err != nil {
			t.Fatal(err)
		}
		want := strip == nil || *strip
		if form.IsValid() != want || want && form.CleanedData()["slug"] != "café" {
			t.Fatal("form stripping changed", form.Errors(), form.CleanedData())
		}
	}
	for _, required := range []bool{false, true} {
		field := forms.NewField("slug", forms.Slug)
		field.Required = required
		form, err := forms.New([]forms.Field{field}, forms.WithData(url.Values{"slug": {" \t\n"}}))
		if err != nil || form.IsValid() == required || required && !form.HasError("slug", "required") {
			t.Fatal("whitespace-only slug lost required semantics", err, form.Errors())
		}
	}
	metadata := models.SlugField("slug", models.WithAllowUnicode(true), models.WithMaxLength(4))
	if err := metadata.Validate(context.Background(), "𐐀中文é"); err != nil {
		t.Fatal(err)
	}
	if err := metadata.Validate(context.Background(), "𐐀中文é5"); err == nil {
		t.Fatal("rune length not enforced")
	}
	if err := metadata.Validate(context.Background(), " café "); err == nil {
		t.Fatal("model silently trimmed value")
	}
	metadata.Blank = true
	if err := metadata.Validate(context.Background(), ""); err != nil {
		t.Fatal("optional slug rejected empty", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := metadata.Clean(ctx, "café"); err == nil {
		t.Fatal("cancellation ignored")
	}
}

func TestSlugUnicodeOptionSurvivesHistoricalGenerationWithoutChangingOldChecksums(t *testing.T) {
	before := models.Schema{AppLabel: "shop", Name: "Article", Fields: []models.Field{models.BigAutoField("id"), models.SlugField("slug")}}
	encoded, err := json.Marshal(before)
	if err != nil || strings.Contains(string(encoded), "AllowUnicode") {
		t.Fatal("new false option changed old schema encoding", err)
	}
	after := before.Clone()
	after.Fields[1].AllowUnicode = true
	if before.Fields[1].AllowUnicode {
		t.Fatal("clone modified original field")
	}
	operations, err := migrations.Detect([]models.Schema{before}, []models.Schema{after}, migrations.DetectOptions{})
	if err != nil || len(operations) != 1 || operations[0].Kind != "alter_field" || !operations[0].Field.AllowUnicode || operations[0].OldField.AllowUnicode {
		t.Fatal("migration failed to retain validator state", operations, err)
	}
	initial := migrations.Migration{App: "shop", Name: "0001_initial", Operations: []migrations.Operation{migrations.CreateModel(before)}}
	changed := migrations.Migration{App: "shop", Name: "0002_unicode", Dependencies: []string{initial.Key()}, Operations: operations}
	encoded, err = json.Marshal(changed)
	if err != nil {
		t.Fatal(err)
	}
	decoded := migrations.MustDecode(string(encoded))
	first, err := changed.Checksum()
	if err != nil {
		t.Fatal(err)
	}
	second, err := decoded.Checksum()
	if err != nil || first != second {
		t.Fatal("generated metadata checksum changed", err)
	}
	executor := migrations.Executor{Migrations: []migrations.Migration{initial, decoded}}
	state, err := executor.State("")
	if err != nil || len(state) != 1 || !state[0].Fields[1].AllowUnicode {
		t.Fatal("historical state lost option", state, err)
	}
	directory, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	path, err := migrations.Generate(directory, changed)
	if err != nil {
		t.Fatal(err)
	}
	source, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(source), `\"AllowUnicode\":true`) {
		t.Fatal("generated source lost option", err)
	}
	// Historical descriptors without the new option remain ASCII and preserve
	// their original serialized checksum shape; generation never rewrites them.
	oldEncoded, err := json.Marshal(initial)
	if err != nil {
		t.Fatal(err)
	}
	old := migrations.MustDecode(string(oldEncoded))
	a, _ := initial.Checksum()
	b, _ := old.Checksum()
	if a != b || old.Operations[0].Schema.Fields[1].AllowUnicode {
		t.Fatal("old migration encoding changed")
	}
}
