package http

import (
	"context"
	"database/sql/driver"
	"encoding/json"
	htmltemplate "html/template"
	"math"
	"net/netip"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Newton-School/gogo/core/models"
	"github.com/Newton-School/gogo/core/templates"
)

type genericModelNamedText string

func (genericModelNamedText) String() string               { panic("must not call String") }
func (genericModelNamedText) MarshalJSON() ([]byte, error) { panic("must not marshal") }
func (genericModelNamedText) Value() (driver.Value, error) { panic("must not call Valuer") }

type genericModelNamedInteger int64

func (genericModelNamedInteger) String() string               { panic("must not call String") }
func (genericModelNamedInteger) MarshalJSON() ([]byte, error) { panic("must not marshal") }

type genericModelOpaque struct{ value string }

func (genericModelOpaque) String() string               { panic("must not call opaque String") }
func (genericModelOpaque) MarshalJSON() ([]byte, error) { panic("must not marshal opaque value") }
func (genericModelOpaque) Value() (driver.Value, error) { panic("must not call opaque Valuer") }

type genericModelCodec struct{}

func (genericModelCodec) Encode(any) (any, error) { panic("must not encode") }
func (genericModelCodec) Decode(any) (any, error) { panic("must not decode") }

func TestGenericModelFieldSupportedIsExplicit(t *testing.T) {
	for _, kind := range []models.Kind{
		models.SmallInteger, models.Integer, models.BigInteger, models.PositiveSmallInteger,
		models.PositiveInteger, models.PositiveBigInteger, models.SmallAuto, models.Auto, models.BigAuto,
		models.UUID, models.Decimal, models.Float, models.Boolean, models.Char, models.Text, models.Slug,
		models.Email, models.URL, models.GenericIPAddress, models.FilePath, models.Date, models.Time,
		models.DateTime, models.Duration,
	} {
		field := models.Field{Name: "value", Kind: kind, MaxDigits: 20, DecimalPlaces: 2}
		if !genericModelFieldSupported(field, true) || !genericModelFieldSupported(field, false) {
			t.Fatal("supported builtin refused", kind)
		}
		field.Codec = genericModelCodec{}
		if genericModelFieldSupported(field, false) {
			t.Fatal("custom codec accepted", kind)
		}
		field.Codec = nil
		field.Relation = &models.Relation{Target: "app.Target"}
		if genericModelFieldSupported(field, false) {
			t.Fatal("relation descriptor accepted", kind)
		}
	}
	jsonField := models.JSONField("value")
	if genericModelFieldSupported(jsonField, true) || !genericModelFieldSupported(jsonField, false) {
		t.Fatal("JSON lookup/output distinction lost")
	}
	for _, kind := range []models.Kind{
		models.ForeignKey, models.OneToOne, models.ManyToMany, models.Generated, models.Array,
		models.HStore, models.Range, models.SearchVector, models.Geometry, models.Geography,
		models.Raster, models.Custom, models.Binary, models.File, models.Image, "future",
	} {
		field := models.Field{Name: "value", Kind: kind}
		if genericModelFieldSupported(field, true) || genericModelFieldSupported(field, false) {
			t.Fatal("unsupported field accepted", kind)
		}
		if value, err := normalizeGenericModelValue(field, nil); value != nil || err != ErrUnavailable {
			t.Fatal("unsupported field null bypassed validation", kind, value, err)
		}
	}
	for _, field := range []models.Field{
		{Kind: models.Decimal}, {Kind: models.Decimal, MaxDigits: 2, DecimalPlaces: -1},
		{Kind: models.Decimal, MaxDigits: 2, DecimalPlaces: 3},
	} {
		if genericModelFieldSupported(field, true) {
			t.Fatal("invalid precision metadata accepted", field)
		}
	}
}

func TestGenericModelValuesUseIntrinsicParsersWithoutWriteCallbacks(t *testing.T) {
	for _, test := range []struct {
		field models.Field
		raw   any
		want  any
	}{
		{models.BigIntegerField("value"), genericModelNamedInteger(9007199254740993), int64(9007199254740993)},
		{models.IntegerField("value"), genericModelNamedText("+0042"), int64(42)},
		{models.IntegerField("value"), []byte("42"), int64(42)},
		{models.DecimalField("value", 24, 4), genericModelNamedText("+009007199254740993.1200"), "+009007199254740993.1200"},
		{models.FloatField("value"), genericModelNamedText("1.25e2"), float64(125)},
		{models.FloatField("value"), float32(0.1), float64(float32(0.1))},
		{models.BooleanField("value"), genericModelNamedText("TRUE"), true},
		{models.BooleanField("value"), false, false},
		{models.UUIDField("value"), []byte("A987FBC9-4BED-3078-CF07-9141BA07C9F3"), "a987fbc9-4bed-3078-cf07-9141ba07c9f3"},
		{models.GenericIPAddressField("value"), "2001:0DB8:0:0:0:0:0:1", "2001:db8::1"},
		{models.GenericIPAddressField("value"), netip.MustParseAddr("127.0.0.1"), "127.0.0.1"},
		{models.EmailField("value"), genericModelNamedText("not a current email"), "not a current email"},
		{models.SlugField("value"), "old value with spaces", "old value with spaces"},
		{models.URLField("value"), "not a current URL", "not a current URL"},
		{models.FilePathField("value"), "/application/relative-policy/../stored-name", "/application/relative-policy/../stored-name"},
		{models.TextField("value"), "", ""},
		{models.DurationField("value"), "1h2m3.000000004s", time.Hour + 2*time.Minute + 3*time.Second + 4},
		{models.DurationField("value"), genericModelNamedInteger(-1234), time.Duration(-1234)},
	} {
		t.Run(string(test.field.Kind)+"/"+reflect.TypeOf(test.raw).String(), func(t *testing.T) {
			test.field.Validators = []models.Validator{func(context.Context, any) error { panic("must not validate") }}
			test.field.DefaultFunc = func() any { panic("must not evaluate default") }
			test.field.Choices = []models.Choice{{Value: genericModelOpaque{}, Label: "ignored"}}
			test.field.Min, test.field.Max = genericModelOpaque{}, genericModelOpaque{}
			test.field.MinLength, test.field.MaxLength = 100, 101
			for _, operation := range []func(models.Field, any) (any, error){decodeGenericLookupValue, normalizeGenericModelValue} {
				got, err := operation(test.field, test.raw)
				if err != nil || !reflect.DeepEqual(got, test.want) {
					t.Fatalf("intrinsic normalization failed: got %#v error %v; want %#v", got, err, test.want)
				}
			}
		})
	}
}

func TestGenericModelIntegerWidthsAndInvalidValues(t *testing.T) {
	for _, test := range []struct {
		kind      models.Kind
		low, high string
		invalid   []any
	}{
		{models.SmallInteger, "-32768", "32767", []any{"-32769", "32768"}},
		{models.SmallAuto, "-32768", "32767", []any{"32768"}},
		{models.Integer, "-2147483648", "2147483647", []any{"-2147483649", "2147483648"}},
		{models.Auto, "-2147483648", "2147483647", []any{"2147483648"}},
		{models.BigInteger, "-9223372036854775808", "9223372036854775807", []any{"9223372036854775808", uint64(math.MaxInt64) + 1}},
		{models.BigAuto, "-9223372036854775808", "9223372036854775807", []any{"-9223372036854775809"}},
		{models.PositiveSmallInteger, "0", "32767", []any{"-1", "32768"}},
		{models.PositiveInteger, "0", "2147483647", []any{"-1", "2147483648"}},
		{models.PositiveBigInteger, "0", "9223372036854775807", []any{"-1", "9223372036854775808"}},
	} {
		field := models.Field{Name: "id", Kind: test.kind}
		for _, valid := range []string{test.low, test.high} {
			want, _ := strconv.ParseInt(valid, 10, 64)
			if got, err := decodeGenericLookupValue(field, valid); err != nil || got != want {
				t.Fatal("native integer bound rejected", test.kind, valid, got, err)
			}
		}
		for _, invalid := range append(test.invalid, nil, "", "1.0", "1e2", float64(1), true, genericModelOpaque{}, new(int)) {
			if got, err := decodeGenericLookupValue(field, invalid); got != nil || err != ErrInvalidLookup {
				t.Fatal("invalid integer lookup accepted", test.kind, reflect.TypeOf(invalid), got, err)
			}
		}
	}
}

func TestGenericModelScalarLimitsNullsAndInvalidRepresentations(t *testing.T) {
	for _, test := range []struct {
		field models.Field
		raw   any
	}{
		{models.DecimalField("value", 5, 2), "1000.00"},
		{models.DecimalField("value", 5, 2), "1.001"},
		{models.DecimalField("value", 5, 2), "1e2"},
		{models.DecimalField("value", 5, 2), "1/2"},
		{models.DecimalField("value", 5, 2), float64(0.1)},
		{models.DecimalField("value", 5, 2), "+."},
		{models.FloatField("value"), math.NaN()},
		{models.FloatField("value"), math.Inf(1)},
		{models.FloatField("value"), "1e999999"},
		{models.BooleanField("value"), "yes"},
		{models.UUIDField("value"), "not-a-uuid"},
		{models.GenericIPAddressField("value"), "fe80::1%eth0"},
		{models.GenericIPAddressField("value"), netip.Addr{}},
		{models.TextField("value"), "invalid\xff"},
		{models.TextField("value"), "nul\x00"},
		{models.TextField("value"), []byte{0xff}},
		{models.TextField("value"), genericModelOpaque{}},
		{models.DurationField("value"), "999999999999999999h"},
		{models.DurationField("value"), float64(1)},
	} {
		if got, err := normalizeGenericModelValue(test.field, test.raw); got != nil || err != ErrUnavailable {
			t.Fatal("invalid provider value accepted", test.field.Kind, reflect.TypeOf(test.raw), got, err)
		}
		if got, err := projectGenericModelValue(test.field, test.raw); got != nil || err != ErrUnavailable {
			t.Fatal("invalid provider value projected", test.field.Kind, reflect.TypeOf(test.raw), got, err)
		}
		if got, err := decodeGenericLookupValue(test.field, test.raw); got != nil || err != ErrInvalidLookup {
			t.Fatal("invalid route key returned provider class", test.field.Kind, got, err)
		}
	}
	field := models.TextField("value")
	for _, operation := range []func(models.Field, any) (any, error){normalizeGenericModelValue, projectGenericModelValue} {
		if got, err := operation(field, nil); got != nil || err != ErrUnavailable {
			t.Fatal("non-null field accepted SQL NULL", got, err)
		}
		field.Null = true
		if got, err := operation(field, nil); got != nil || err != nil {
			t.Fatal("nullable field rejected SQL NULL", got, err)
		}
		field.Null = false
	}
	field.Null = true
	if got, err := decodeGenericLookupValue(field, nil); got != nil || err != ErrInvalidLookup {
		t.Fatal("nullable descriptor permitted null identity")
	}
	lookupLimit := strings.Repeat("x", genericLookupTextBytes)
	if got, err := decodeGenericLookupValue(field, lookupLimit); err != nil || got != lookupLimit {
		t.Fatal("exact lookup limit rejected", err)
	}
	if got, err := decodeGenericLookupValue(field, lookupLimit+"x"); got != nil || err != ErrInvalidLookup {
		t.Fatal("oversized lookup accepted", err)
	}
	for _, raw := range []any{strings.Repeat("x", templateContextMaxBytes+1), make([]byte, templateContextMaxBytes+1)} {
		if got, err := normalizeGenericModelValue(field, raw); got != nil || err != ErrUnavailable {
			t.Fatal("oversized provider text accepted", err)
		}
	}
}

func TestGenericModelTemporalCalendarPrecisionAndDetachment(t *testing.T) {
	source := time.Date(2024, time.January, 2, 0, 30, 0, 123456789, time.FixedZone("source", 14*3600))
	for _, test := range []struct {
		kind models.Kind
		want any
	}{
		{models.Date, "2024-01-02"},
		{models.Time, "00:30:00.123456789"},
		{models.DateTime, source.UTC()},
	} {
		field := models.Field{Name: "value", Kind: test.kind}
		canonical, err := normalizeGenericModelValue(field, source)
		if err != nil {
			t.Fatal(err)
		}
		instant := canonical.(time.Time)
		if instant.Location() == source.Location() || instant.Location() == time.UTC {
			t.Fatal("canonical timestamp retains shared Location", test.kind)
		}
		projected, err := projectGenericModelValue(field, source)
		if err != nil {
			t.Fatal(err)
		}
		if expected, ok := test.want.(time.Time); ok {
			if value := projected.(time.Time); !value.Equal(expected) || value.Location() == instant.Location() {
				t.Fatal("datetime instant changed or normalization views alias")
			}
		} else if projected != test.want {
			t.Fatal("calendar/clock value shifted", test.kind, projected, test.want)
		}
		*instant.Location() = *time.FixedZone("changed", -12*3600)
		if _, offset := source.Zone(); offset != 14*3600 {
			t.Fatal("policy timestamp mutated provider location")
		}
		if value, ok := projected.(time.Time); ok {
			if _, offset := value.Zone(); offset != 0 {
				t.Fatal("independent projected timestamp changed")
			}
		}
	}
	zero := time.Time{}
	value, err := normalizeGenericModelValue(models.DateTimeField("value"), zero)
	if err != nil || !value.(time.Time).IsZero() || value.(time.Time).Location() == zero.Location() {
		t.Fatal("zero instant was not detached", err)
	}
	for _, test := range []struct {
		kind models.Kind
		raw  string
	}{
		{models.Date, "2024-02-29"}, {models.Time, "23:59:59.123456789"},
		{models.DateTime, "2024-02-29T23:59:59.123456789+05:30"},
	} {
		field := models.Field{Name: "value", Kind: test.kind}
		a, err := decodeGenericLookupValue(field, genericModelNamedText(test.raw))
		if err != nil {
			t.Fatal("valid temporal lookup refused", test, err)
		}
		b, err := normalizeGenericModelValue(field, []byte(test.raw))
		if err != nil || !a.(time.Time).Equal(b.(time.Time)) || a.(time.Time).Location() == b.(time.Time).Location() {
			t.Fatal("temporal provider/lookup identities differ or alias", test, err)
		}
	}
	for _, test := range []struct {
		kind models.Kind
		raw  any
	}{
		{models.Date, "2023-02-29"}, {models.Date, "0000-01-01"}, {models.Date, "2024-2-01"},
		{models.Time, "24:00:00"}, {models.Time, "23:59:60"}, {models.Time, "01:02:03Z"},
		{models.Time, "01:02:03.1234567891"}, {models.Time, "01:02:03,5"},
		{models.DateTime, "2024-01-01T01:02:03.1234567891Z"}, {models.DateTime, "2024-01-01T01:02:03+24:00"},
		{models.DateTime, "2024-01-01T01:02:03+00:60"}, {models.DateTime, "2024-01-01T01:02:03"},
		{models.DateTime, "0001-01-01T00:00:00+01:00"},
		{models.DateTime, time.Date(2024, 1, 1, 0, 0, 0, 0, time.FixedZone("invalid", 86400))},
	} {
		field := models.Field{Name: "value", Kind: test.kind}
		if got, err := normalizeGenericModelValue(field, test.raw); got != nil || err != ErrUnavailable {
			t.Fatal("invalid or precision-losing temporal value accepted", test.kind, reflect.TypeOf(test.raw), got, err)
		}
	}
	if value, err := projectGenericModelValue(models.DurationField("value"), time.Minute+time.Nanosecond); err != nil || value != "1m0.000000001s" {
		t.Fatal("duration did not use exact Go spelling", value, err)
	}
}

func TestGenericModelProjectionStripsHTMLTrustAndCopiesBytes(t *testing.T) {
	field := models.TextField("value")
	for _, raw := range []any{templates.SafeHTML("<b>{{ x }}</b>"), htmltemplate.HTML("<b>{{ x }}</b>"), genericModelNamedText("<b>{{ x }}</b>")} {
		value, err := projectGenericModelValue(field, raw)
		text, plain := value.(string)
		if err != nil || !plain || text != "<b>{{ x }}</b>" {
			t.Fatal("model text retained HTML safety provenance", reflect.TypeOf(value), err)
		}
		engine := templates.New(templates.Config{Loaders: []templates.Loader{templates.MapLoader{"value.html": "{{ value }}"}}})
		output, err := engine.Render(context.Background(), "value.html", templates.Context{"value": value})
		if err != nil || output != "&lt;b&gt;{{ x }}&lt;/b&gt;" {
			t.Fatal("model string became source or raw HTML", output, err)
		}
	}
	raw := []byte("original")
	value, err := normalizeGenericModelValue(field, raw)
	if err != nil {
		t.Fatal(err)
	}
	raw[0] = 'X'
	if value != "original" {
		t.Fatal("provider byte storage escaped normalization")
	}
}

func TestGenericModelJSONPreservesPolicyNumbersNullsAndDetachedData(t *testing.T) {
	field := models.JSONField("value", models.Nullable)
	input := map[string]any{
		"large":    json.Number("9007199254740993123456789"),
		"small":    json.Number("-0.00000000000000000000100"),
		"exponent": json.Number("1e999999999999"), "text": "9007199254740993123456789",
		"list": []any{models.JSONNull, nil, genericModelNamedInteger(7), genericModelNamedText("<b>value</b>")},
		"raw":  json.RawMessage(`{"n":9007199254740993,"null":null}`),
	}
	canonical, err := normalizeGenericModelValue(field, input)
	if err != nil {
		t.Fatal(err)
	}
	policy := canonical.(map[string]any)
	if policy["large"] != json.Number("9007199254740993123456789") || policy["small"] != input["small"] || policy["exponent"] != input["exponent"] || reflect.TypeOf(policy["text"]) != reflect.TypeFor[string]() {
		t.Fatal("JSON numbers lost precision or string distinction", policy)
	}
	if values := policy["list"].([]any); values[0] != nil || values[1] != nil || values[2] != json.Number("7") || values[3] != "<b>value</b>" {
		t.Fatal("JSON nested canonical values changed", values)
	}
	projected, err := projectGenericModelValue(field, input)
	if err != nil {
		t.Fatal(err)
	}
	html := projected.(map[string]any)
	if html["large"] != "9007199254740993123456789" || html["small"] != "-0.00000000000000000000100" || html["exponent"] != "1e999999999999" || html["raw"].(map[string]any)["n"] != "9007199254740993" {
		t.Fatal("HTML JSON projection lost lexical precision", html)
	}
	input["large"] = json.Number("1")
	input["list"].([]any)[2] = json.Number("1")
	input["raw"].(json.RawMessage)[2] = 'x'
	if policy["large"] != json.Number("9007199254740993123456789") || policy["list"].([]any)[2] != json.Number("7") || policy["raw"].(map[string]any)["n"] != json.Number("9007199254740993") {
		t.Fatal("provider storage mutated canonical policy data")
	}
	policy["list"].([]any)[2] = json.Number("2")
	if html["list"].([]any)[2] != "7" {
		t.Fatal("separately delivered policy and output views alias")
	}
	for _, raw := range []any{models.JSONNull, json.RawMessage("null"), []byte("null"), map[string]any(nil), []any(nil)} {
		value, err := normalizeGenericModelValue(field, raw)
		if err != nil || value != models.JSONNull {
			t.Fatal("top-level JSON null lost marker", reflect.TypeOf(raw), value, err)
		}
		value, err = projectGenericModelValue(field, raw)
		if err != nil || value != nil {
			t.Fatal("JSON null projected as numeric zero", reflect.TypeOf(raw), value, err)
		}
	}
	for _, raw := range []any{nil, json.RawMessage(nil)} {
		if value, err := normalizeGenericModelValue(field, raw); value != nil || err != nil {
			t.Fatal("SQL NULL confused with JSON null", value, err)
		}
	}
	if value, err := normalizeGenericModelValue(field, "null"); err != nil || value != "null" {
		t.Fatal("native JSON string parsed as JSON null", value, err)
	}
}

func TestGenericModelJSONRejectsUnsupportedAndAmbiguousValues(t *testing.T) {
	field := models.JSONField("value")
	cycle := map[string]any{}
	cycle["self"] = cycle
	for _, raw := range []any{
		genericModelOpaque{}, &genericModelOpaque{}, time.Time{}, make(chan int), func() {}, new(int),
		math.NaN(), math.Inf(-1), json.Number("01"), json.Number("+1"), json.Number("1."), json.Number("NaN"), json.Number("1e"),
		map[int]any{1: "value"}, map[string]any{"bytes": []byte("1")}, cycle,
		json.RawMessage(`{"x":1,"x":2}`), json.RawMessage(`{"x":1,"\u0078":2}`),
		json.RawMessage(`"\ud800"`), json.RawMessage(`"\udc00"`), json.RawMessage(`"\ud800\u0041"`),
		json.RawMessage(`true false`), json.RawMessage(`{"x":`), json.RawMessage{}, []byte{},
		json.RawMessage{'"', 0xff, '"'}, "invalid\xff",
	} {
		if value, err := normalizeGenericModelValue(field, raw); value != nil || err != ErrUnavailable {
			t.Fatal("invalid JSON provider value accepted", reflect.TypeOf(raw), value, err)
		}
		if value, err := projectGenericModelValue(field, raw); value != nil || err != ErrUnavailable {
			t.Fatal("invalid JSON provider value projected", reflect.TypeOf(raw), value, err)
		}
	}
	for raw, want := range map[string]string{`"\ud83d\ude00"`: "😀", `"\\ud800"`: `\ud800`, `"\ufffd"`: "�"} {
		value, err := normalizeGenericModelValue(field, json.RawMessage(raw))
		if err != nil || value != want {
			t.Fatal("valid Unicode JSON string changed", raw, value, err)
		}
	}
}

func TestGenericModelJSONBoundsApplyBeforeAllocationAndAfterDecoding(t *testing.T) {
	field := models.JSONField("value")
	wrap := func(depth int) any {
		var value any
		for range depth {
			value = []any{value}
		}
		return value
	}
	for _, raw := range []any{
		wrap(templateContextMaxDepth), make([]any, templateContextMaxValues-1),
		strings.Repeat("x", templateContextMaxBytes),
		json.RawMessage(strings.Repeat("[", templateContextMaxDepth) + "null" + strings.Repeat("]", templateContextMaxDepth)),
	} {
		if _, err := normalizeGenericModelValue(field, raw); err != nil {
			t.Fatal("exact supported JSON bound rejected", reflect.TypeOf(raw), err)
		}
	}
	for _, raw := range []any{
		wrap(templateContextMaxDepth + 1), make([]any, templateContextMaxValues),
		strings.Repeat("x", templateContextMaxBytes+1),
		map[string]any{"key": strings.Repeat("x", templateContextMaxBytes)},
		json.Number("1" + strings.Repeat("0", templateContextMaxBytes)),
		json.RawMessage(strings.Repeat("[", templateContextMaxDepth+1) + "null" + strings.Repeat("]", templateContextMaxDepth+1)),
		json.RawMessage(strings.Repeat(" ", templateContextMaxBytes) + "null"),
	} {
		if value, err := normalizeGenericModelValue(field, raw); value != nil || err != ErrUnavailable {
			t.Fatal("JSON bound returned partial or oversized value", reflect.TypeOf(raw), err)
		}
	}
	keys := make(map[string]any, templateContextMaxValues/2)
	for i := range templateContextMaxValues / 2 {
		keys[strconv.Itoa(i)] = nil
	}
	if value, err := normalizeGenericModelValue(field, keys); value != nil || err != ErrUnavailable {
		t.Fatal("object keys did not consume traversal budget", err)
	}
	fragment := json.RawMessage(strings.Repeat(" ", templateContextMaxBytes/2-4) + "null")
	if value, err := normalizeGenericModelValue(field, []any{fragment, fragment}); err != nil || !reflect.DeepEqual(value, []any{nil, nil}) {
		t.Fatal("exact aggregate raw JSON work bound rejected", err)
	}
	fragment = append(fragment, ' ')
	if value, err := normalizeGenericModelValue(field, []any{fragment, fragment}); value != nil || err != ErrUnavailable {
		t.Fatal("nested raw messages multiplied JSON parse budget", err)
	}
}

func TestGenericModelJSONSharedPageBudget(t *testing.T) {
	field := models.JSONField("value", models.Nullable)
	newBudget := func() *genericModelJSONBudget {
		return &genericModelJSONBudget{
			values: templateContextMaxValues, text: templateContextMaxBytes,
			raw: templateContextMaxBytes,
		}
	}
	t.Run("raw parsing work across cells", func(t *testing.T) {
		fragment := json.RawMessage(strings.Repeat(" ", templateContextMaxBytes/2-4) + "null")
		budget := newBudget()
		for range 2 {
			if value, err := normalizeGenericModelValueWithJSONBudget(field, fragment, budget); err != nil || value != models.JSONNull {
				t.Fatal("exact page raw budget rejected", value, err)
			}
		}
		if budget.raw != 0 || budget.values != templateContextMaxValues-2 || budget.text != templateContextMaxBytes {
			t.Fatal("raw and decoded work budgets were conflated", budget)
		}
		if value, err := normalizeGenericModelValueWithJSONBudget(field, fragment, budget); value != nil || err != ErrUnavailable {
			t.Fatal("repeated whitespace-heavy cells bypassed page raw budget", value, err)
		}
		if value, err := normalizeGenericModelValue(field, fragment); err != nil || value != models.JSONNull {
			t.Fatal("shared page exhaustion changed standalone normalization", value, err)
		}
	})
	t.Run("decoded nodes across cells", func(t *testing.T) {
		cell := make([]any, templateContextMaxValues/2-1)
		budget := newBudget()
		for range 2 {
			value, err := normalizeGenericModelValueWithJSONBudget(field, cell, budget)
			if err != nil || len(value.([]any)) != len(cell) {
				t.Fatal("exact page node budget rejected", err)
			}
		}
		if budget.values != 0 {
			t.Fatal("decoded cells failed to share node budget", budget.values)
		}
		if value, err := normalizeGenericModelValueWithJSONBudget(field, models.JSONNull, budget); value != nil || err != ErrUnavailable {
			t.Fatal("new cell bypassed exhausted node budget", value, err)
		}
	})
	t.Run("decoded text and keys across cells", func(t *testing.T) {
		budget := newBudget()
		cell := map[string]any{"key": strings.Repeat("x", templateContextMaxBytes/2-3)}
		for range 2 {
			if _, err := normalizeGenericModelValueWithJSONBudget(field, cell, budget); err != nil {
				t.Fatal("exact page text budget rejected", err)
			}
		}
		if budget.text != 0 || budget.values != templateContextMaxValues-6 {
			t.Fatal("keys or text did not consume shared budget", budget)
		}
		if value, err := normalizeGenericModelValueWithJSONBudget(field, "x", budget); value != nil || err != ErrUnavailable {
			t.Fatal("new cell bypassed exhausted text budget", value, err)
		}
	})
	t.Run("canonical values and SQL null", func(t *testing.T) {
		budget := newBudget()
		for _, raw := range []any{nil, json.RawMessage(nil), models.JSONNull, json.RawMessage("null"), json.Number("9007199254740993"), map[string]any{"value": json.Number("0.00100")}} {
			want, err := normalizeGenericModelValue(field, raw)
			if err != nil {
				t.Fatal(err)
			}
			got, err := normalizeGenericModelValueWithJSONBudget(field, raw, budget)
			if err != nil || !reflect.DeepEqual(got, want) {
				t.Fatal("shared budget changed canonical data or SQL-null distinction", got, want, err)
			}
		}
		before := *budget
		if value, err := normalizeGenericModelValueWithJSONBudget(models.BigIntegerField("integer"), int64(9007199254740993), budget); err != nil || value != int64(9007199254740993) || *budget != before {
			t.Fatal("scalar normalization changed JSON budget", value, err)
		}
		for _, invalid := range []*genericModelJSONBudget{nil, {values: 1, text: 1, raw: 1, html: true}} {
			if value, err := normalizeGenericModelValueWithJSONBudget(field, json.Number("1"), invalid); value != nil || err != ErrUnavailable {
				t.Fatal("normalization accepted absent or HTML-mode shared budget", value, err)
			}
		}
	})
}

func FuzzGenericModelJSONValue(f *testing.F) {
	for _, raw := range []string{`null`, `{"number":9007199254740993,"text":"<b>"}`, `1e9999999`, `"\ud83d\ude00"`, `{"x":1,"x":2}`, `[[[null]]]`} {
		f.Add(raw)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		field := models.JSONField("value")
		canonical, err := normalizeGenericModelValue(field, json.RawMessage(raw))
		if err != nil {
			if canonical != nil || err != ErrUnavailable {
				t.Fatal("failed JSON normalization returned partial output")
			}
			return
		}
		again, err := normalizeGenericModelValue(field, canonical)
		if err != nil || !reflect.DeepEqual(again, canonical) {
			t.Fatal("canonical JSON value is not stable")
		}
		html, err := projectGenericModelValue(field, canonical)
		if err != nil {
			t.Fatal("canonical JSON value cannot be projected")
		}
		if canonical == models.JSONNull && html != nil {
			t.Fatal("JSON null acquired numeric output")
		}
	})
}
