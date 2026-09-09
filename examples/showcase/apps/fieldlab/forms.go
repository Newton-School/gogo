package fieldlab

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/png"
	"mime/multipart"
	"net/url"
	"regexp"
	"strconv"

	"github.com/Newton-School/gogo/core/forms"
)

// FormCase binds the exact submitted values used by the corresponding test.
// File and Image additionally use ValidFiles; their invalid case omits uploads.
type FormCase struct {
	Field          forms.Field
	Valid, Invalid url.Values
	Limitation     string
}

// FormCases covers every currently declared form Kind using public NewField.
// Relation choices below are deliberately a fixed, public educational catalog,
// not customer records. A real application must resolve IDs within actor scope.
func FormCases() []FormCase {
	type scalar struct {
		kind       forms.Kind
		good, bad  string
		limitation string
	}
	declarations := []scalar{
		{forms.Char, "  Example  ", "", "Char trims whitespace by default; password preservation is tested separately."},
		{forms.Boolean, "true", "false", "Required Boolean means an affirmative checkbox; it is not model Boolean semantics."},
		{forms.NullBoolean, "false", "unknown", "Required in this demonstration; an optional NullBoolean also accepts unknown as nil."},
		{forms.ChoiceKind, "1", "999", ""},
		{forms.TypedChoice, "1", "999", "Choice coercion returns an int64."},
		{forms.MultipleChoice, "1", "999", ""},
		{forms.TypedMultipleChoice, "1", "999", "Choice coercion returns integer values."},
		{forms.Integer, "42", "1.5", ""},
		{forms.Float, "1.25", "NaN", ""},
		{forms.Decimal, "12.50", "12.501", "Precision is declared explicitly; cleaned decimals remain strings."},
		{forms.Date, "2026-01-02", "2026-02-30", ""},
		{forms.DateTime, "2026-01-02T03:04:05Z", "not-a-date", "UTC input; framework locale context also supports configured timezone conversion."},
		{forms.Time, "03:04:05", "25:00:00", ""},
		{forms.Duration, "1h2m", "forever", ""},
		{forms.Email, "developer@example.com", "Developer <developer@example.com>", ""},
		{forms.URL, "https://example.com", "javascript:alert(1)", ""},
		{forms.UUID, "12345678-1234-1234-1234-123456789abc", "not-a-uuid", ""},
		{forms.Slug, "example-slug", "has spaces", ""},
		{forms.IP, "2001:db8::1", "999.1.1.1", ""},
		{forms.Regex, "DEMO-42", "wrong", "Application-owned regular expression is anchored."},
		{forms.JSON, `{"count":9007199254740993}`, `{} trailing`, "Uses json.Number for lossless numeric values."},
		{forms.File, "", "", "Real bounded multipart file validation; validation does not save a storage object."},
		{forms.Image, "", "", "Image bytes are decoded before accepting an upload; validation does not save an object."},
		{forms.FilePath, "documents/example.txt", "", "Only string cleaning is implemented; no filesystem enumeration or path authorization."},
		{forms.ModelChoice, "1", "999", "Fixed public demo choices are resolved again during validation; this is not an ORM lookup."},
		{forms.ModelMultipleChoice, "1", "999", "Fixed public choices; unknown IDs fail closed and duplicate IDs are deduplicated."},
		{forms.MultiValue, "", "", "Two independently cleaned integers are compressed into a pair."},
		{forms.Combo, "DEMO-42", "wrong", "One value runs through Char then Regex cleaning."},
		{forms.SplitDateTime, "", "", "Date/time components are bound independently and compressed into one instant."},
	}
	result := make([]FormCase, 0, len(declarations))
	for _, declaration := range declarations {
		name := string(declaration.kind)
		field := forms.NewField(name, declaration.kind)
		field.Label = name
		field.HelpText = declaration.limitation
		good, bad := url.Values{name: {declaration.good}}, url.Values{name: {declaration.bad}}
		switch declaration.kind {
		case forms.ChoiceKind, forms.TypedChoice, forms.MultipleChoice, forms.TypedMultipleChoice, forms.ModelChoice, forms.ModelMultipleChoice:
			field.Choices = []forms.Choice{{Value: "1", Label: "One"}, {Value: "2", Label: "Two"}}
			field.Coerce = func(value string) (any, error) { return strconv.ParseInt(value, 10, 64) }
			field.Resolve = resolvePublicChoices
			if declaration.kind == forms.MultipleChoice || declaration.kind == forms.TypedMultipleChoice || declaration.kind == forms.ModelMultipleChoice {
				good[name] = []string{"1", "2"}
			}
		case forms.Decimal:
			field.MaxDigits, field.DecimalPlaces = 8, 2
		case forms.Regex:
			field.Pattern = regexp.MustCompile(`^DEMO-[0-9]+$`)
		case forms.File, forms.Image:
			field.MaxBytes = 4096
		case forms.MultiValue:
			field.Fields = []forms.Field{forms.NewField("first", forms.Integer), forms.NewField("second", forms.Integer)}
			field.Compress = func(parts []any) (any, error) {
				if len(parts) != 2 {
					return nil, errors.New("fieldlab: expected a pair")
				}
				return parts, nil
			}
			good = url.Values{name + "_0": {"1"}, name + "_1": {"2"}}
			bad = url.Values{name + "_0": {"1"}, name + "_1": {"bad"}}
		case forms.Combo:
			pattern := forms.NewField("pattern", forms.Regex)
			pattern.Pattern = regexp.MustCompile(`^DEMO-[0-9]+$`)
			field.Fields = []forms.Field{forms.NewField("text", forms.Char), pattern}
		case forms.SplitDateTime:
			good = url.Values{name + "_0": {"2026-01-02"}, name + "_1": {"03:04:05"}}
			bad = url.Values{name + "_0": {"2026-02-30"}, name + "_1": {"03:04:05"}}
		}
		result = append(result, FormCase{Field: field, Valid: good, Invalid: bad, Limitation: declaration.limitation})
	}
	return result
}

func resolvePublicChoices(ctx context.Context, ids []string) ([]any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	result := make([]any, 0, len(ids))
	for _, id := range ids {
		if id != "1" && id != "2" {
			return nil, forms.Error{Code: "invalid_choice", Message: "Select a public demo choice."}
		}
		result = append(result, id)
	}
	return result, nil
}

// Form renders and binds every form kind. Each request must create its own Form.
// Passing no options produces an unbound form; WithData binds even empty input.
func Form(options ...forms.Option) (*forms.Form, error) {
	fields := make([]forms.Field, 0, len(FormCases()))
	for _, example := range FormCases() {
		field := example.Field
		// Initial data populates the browser without pretending it was submitted.
		if values := example.Valid[field.Name]; len(values) == 1 {
			field.Initial = values[0]
		} else if len(values) > 1 {
			field.Initial = values
		} else if field.Kind == forms.MultiValue || field.Kind == forms.SplitDateTime {
			field.Initial = []any{example.Valid.Get(field.Name + "_0"), example.Valid.Get(field.Name + "_1")}
		}
		fields = append(fields, field)
	}
	return forms.New(fields, options...)
}

// ValidValues is complete text input for Form. Include ValidFiles with WithFiles
// to exercise successful validation of the required file and image fields.
func ValidValues() url.Values {
	result := url.Values{}
	for _, example := range FormCases() {
		for name, values := range example.Valid {
			result[name] = append([]string(nil), values...)
		}
	}
	return result
}

// ValidFiles creates two tiny in-memory multipart fixtures, without touching a
// server filesystem. This is test/demo input, not a production upload parser.
func ValidFiles() (map[string][]*multipart.FileHeader, error) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	file, err := writer.CreateFormFile("file", "example.txt")
	if err != nil {
		return nil, err
	}
	if _, err := file.Write([]byte("Gogo field demonstration.\n")); err != nil {
		return nil, err
	}
	picture, err := writer.CreateFormFile("image", "pixel.png")
	if err != nil {
		return nil, err
	}
	pixel := image.NewRGBA(image.Rect(0, 0, 1, 1))
	pixel.Set(0, 0, color.RGBA{R: 42, G: 120, B: 180, A: 255})
	if err := png.Encode(picture, pixel); err != nil {
		return nil, err
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}
	// Both bounded, source-defined fixtures fit entirely within this memory cap.
	parsed, err := multipart.NewReader(bytes.NewReader(body.Bytes()), writer.Boundary()).ReadForm(1 << 20)
	if err != nil {
		return nil, err
	}
	return parsed.File, nil
}
