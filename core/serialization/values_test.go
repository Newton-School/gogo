package serialization

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/Newton-School/gogo/core/models"
	"math"
	"math/big"
	"os"
	"os/exec"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestFixtureCellsPreserveTypedValuesAndNulls(t *testing.T) {
	cases := []struct {
		name  string
		field models.Field
		value any
		wire  string
	}{
		{"large-int", models.Field{Kind: models.BigInteger}, int64(9007199254740993), `{"value":9007199254740993}`},
		{"decimal", models.Field{Kind: models.Decimal, MaxDigits: 30, DecimalPlaces: 3}, "-9007199254740993.125", `{"value":"-9007199254740993.125"}`},
		{"decimal-padding", models.Field{Kind: models.Decimal, MaxDigits: 6, DecimalPlaces: 3}, "2.5", `{"value":"2.500"}`},
		{"sql-null", models.Field{Kind: models.JSON, Null: true}, nil, `{"sql_null":true}`},
		{"json-null", models.Field{Kind: models.JSON}, models.JSONNull, `{"value":null}`},
		{"json-number", models.Field{Kind: models.JSON}, map[string]any{"big": json.Number("9007199254740993"), "null": nil}, `{"value":{"big":9007199254740993,"null":null}}`},
		{"binary", models.Field{Kind: models.Binary}, []byte{0, 1, 255}, `{"value":"AAH/"}`},
		{"empty-binary", models.Field{Kind: models.Binary}, []byte{}, `{"value":""}`},
		{"date", models.Field{Kind: models.Date}, time.Date(2026, 4, 5, 0, 0, 0, 0, time.UTC), `{"value":"2026-04-05"}`},
		{"time", models.Field{Kind: models.Time}, time.Date(0, 1, 1, 12, 34, 56, 123456000, time.UTC), `{"value":"12:34:56.123456"}`},
		{"datetime", models.Field{Kind: models.DateTime}, time.Date(2026, 4, 5, 6, 7, 8, 123456000, time.UTC), `{"value":"2026-04-05T06:07:08.123456Z"}`},
		{"duration", models.Field{Kind: models.Duration}, -time.Hour - 3*time.Microsecond, `{"value":-3600000003}`},
		{"bool", models.Field{Kind: models.Boolean}, true, `{"value":true}`},
		{"float", models.Field{Kind: models.Float}, 1.25, `{"value":1.25}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			data, w, e := cell(tc.field, tc.value, false)
			if e != nil {
				t.Fatal(e)
			}
			raw, e := json.Marshal(w)
			if e != nil || string(raw) != tc.wire {
				t.Fatal(string(raw), tc.wire, e)
			}
			d := json.NewDecoder(bytes.NewReader(raw))
			d.UseNumber()
			nodes := 65536
			decoded, e := strictValue(d, 0, &nodes)
			if e != nil {
				t.Fatal(e)
			}
			loaded, e := fromWire(tc.field, decoded, false)
			if e != nil || !reflect.DeepEqual(data, loaded) {
				t.Fatal(data, loaded, e)
			}
			if ts, ok := data.(time.Time); ok {
				if ts.Location() == time.UTC || ts.Location() == time.Local {
					t.Fatal("shared location")
				}
				*ts.Location() = *time.FixedZone("caller", 3600)
				_, again, e := cell(tc.field, tc.value, false)
				b, _ := json.Marshal(again)
				if e != nil || string(b) != tc.wire {
					t.Fatal("timestamp aliased caller", string(b), e)
				}
			}
		})
	}
}

func TestFixtureCellInvalidTypesNeverCallMethods(t *testing.T) {
	for _, tc := range []struct {
		field models.Field
		value any
	}{
		{models.Field{Kind: models.BigInteger}, "1"}, {models.Field{Kind: models.Text}, json.Number("1")}, {models.Field{Kind: models.Boolean}, "true"},
		{models.Field{Kind: models.Decimal, MaxDigits: 3, DecimalPlaces: 1}, "123.4"}, {models.Field{Kind: models.Decimal, MaxDigits: 3, DecimalPlaces: 1}, "1e1"},
		{models.Field{Kind: models.Float}, json.Number("1e999")}, {models.Field{Kind: models.UUID}, "bad"}, {models.Field{Kind: models.SmallInteger}, json.Number("32768")},
		{models.Field{Kind: models.Duration}, json.Number("9223372036854775807")}, {models.Field{Kind: models.DateTime}, "2026-04-05T00:00:00.000000001Z"},
	} {
		if _, e := fromWire(tc.field, map[string]any{"value": tc.value}, false); e == nil {
			t.Fatal(tc)
		}
	}
	for _, v := range []any{fixtureOpaque{}, map[string]any{"opaque": fixtureOpaque{}}, math.NaN(), math.Inf(1), json.Number("true")} {
		if _, _, e := canonical(models.Field{Kind: models.JSON}, v, false); e == nil {
			t.Fatal("accepted unsupported JSON", reflect.TypeOf(v))
		}
	}
	cycle := map[string]any{}
	cycle["self"] = cycle
	if _, _, e := canonical(models.Field{Kind: models.JSON}, cycle, false); !errors.Is(e, ErrLimit) {
		t.Fatal(e)
	}
}

type fixtureOpaque struct{}

func (fixtureOpaque) MarshalJSON() ([]byte, error) { panic("untrusted marshaler invoked") }
func (fixtureOpaque) String() string               { panic("untrusted Stringer invoked") }

func TestFixtureMalformedInputNeverBegins(t *testing.T) {
	valid := fixtureWire(1, "one", "allowed")
	for _, raw := range []string{"", `null`, `{}`, `[`, "[" + valid + "," + valid + "]", "[" + strings.Replace(valid, `"version":1`, `"version":1,"version":1`, 1) + "]", "[" + strings.Replace(valid, "example.Note", "unknown.Note", 1) + "]", "[" + strings.Replace(valid, `"title":{"value":"one"}`, `"title":{"value":null}`, 1) + "]", "[" + strings.Replace(valid, "one", `\ud800`, 1) + "]", "[" + strings.Replace(valid, "one", string([]byte{0xff}), 1) + "]", "[" + valid + "] false", "[" + strings.Replace(valid, `"title":{"value":"one"}`, `"private":{"value":"secret"}`, 1) + "]"} {
		b := &fixtureBackend{}
		f := fixture(t, b, fixtureProfile())
		r, e := f.Load(context.Background(), strings.NewReader(raw), loadOptions())
		if e == nil || r != (LoadResult{}) || b.begins != 0 || strings.Contains(e.Error(), "secret") {
			t.Fatal(raw, r, e, b.begins)
		}
	}
}

func TestFixtureJSONLBlankLinesBoundedAllocation(t *testing.T) {
	f := fixture(t, &fixtureBackend{}, fixtureProfile())
	selected, e := f.state.selectModels(JSONL, []string{"example.Note"}, true)
	if e != nil {
		t.Fatal(e)
	}
	// Line iteration must not allocate one []byte header per newline. Measure
	// allocated bytes (Split uses one large allocation, not many small ones).
	raw := bytes.Repeat([]byte{'\n'}, 1<<16)
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	rows, e := f.state.parse(raw, JSONL, selected)
	runtime.ReadMemStats(&after)
	if e != nil || len(rows) != 0 {
		t.Fatal(rows, e)
	}
	if allocated := after.TotalAlloc - before.TotalAlloc; allocated > 512<<10 {
		t.Fatal("blank line header materialization", allocated)
	}
}

func TestFixturesRejectMissingDeclaredPKAndTrailingProviderJSON(t *testing.T) {
	p := fixtureProfile()
	p.Schema.PrimaryKey = []string{"id", "missing"}
	if _, e := New(Config{Backend: &fixtureBackend{}, Profiles: []ModelProfile{p}}); e != ErrConfiguration {
		t.Fatal(e)
	}
	for _, raw := range []string{`{"a":1} {"private":2}`, `null false`} {
		if _, e := decodeRaw(models.Field{Kind: models.JSON}, []byte(raw), fixtureDialect{}); e == nil {
			t.Fatal("trailing provider JSON accepted")
		}
	}
}

func TestFixtureJSONNumberCanonicalPrecision(t *testing.T) {
	for _, tc := range []struct{ input, want string }{{"1e2", "100"}, {"100.00", "100"}, {"-0.00", "0"}, {"9007199254740993", "9007199254740993"}, {"1e100000", "1e100000"}, {"0.0000001", "1e-7"}, {"123456789012345678901234567890", "1.2345678901234567890123456789e29"}} {
		n, e := canonicalNumber(tc.input)
		if e != nil || string(n) != tc.want || !json.Valid([]byte(n)) {
			t.Fatal(tc, n, e)
		}
	}
}

func TestFixtureJSONNumberCanonicalClosedBounds(t *testing.T) {
	for _, raw := range []string{strings.Repeat("1", 128), "-" + strings.Repeat("9", 127), "99e2147483647", "0.1e-2147483648", "1e2147483776", "1e-2147483776"} {
		canonical, e := canonicalNumber(raw)
		if e != nil {
			t.Fatal(raw, e)
		}
		for i := 0; i < 4; i++ {
			again, e := canonicalNumber(string(canonical))
			if e != nil || again != canonical {
				t.Fatal("canonical output was not closed", raw, canonical, again, e)
			}
			canonical = again
		}
		field := models.Field{Kind: models.JSON}
		data, wire, e := cell(field, map[string]any{"number": json.Number(raw)}, false)
		if e != nil {
			t.Fatal(e)
		}
		encoded, e := json.Marshal(wire)
		if e != nil {
			t.Fatal(e)
		}
		decoder := json.NewDecoder(bytes.NewReader(encoded))
		decoder.UseNumber()
		nodes := 65536
		parsed, e := strictValue(decoder, 0, &nodes)
		if e != nil {
			t.Fatal(e)
		}
		loaded, e := fromWire(field, parsed, false)
		if e != nil || !reflect.DeepEqual(data, loaded) {
			t.Fatal(raw, data, loaded, e)
		}
	}
}

func TestFixtureExactRecordLimitJSONAndJSONLParity(t *testing.T) {
	a, z := fixtureWire(1, "one", "allowed"), fixtureWire(2, "two", "allowed")
	if len(a) != len(z) {
		t.Fatal("fixture widths differ")
	}
	f, e := New(Config{Backend: &fixtureBackend{}, Profiles: []ModelProfile{fixtureProfile()}, Limits: Limits{MaxRecordBytes: len(a)}})
	if e != nil {
		t.Fatal(e)
	}
	selected, _ := f.state.selectModels(JSON, []string{"example.Note"}, true)
	for _, tc := range []struct {
		format Format
		raw    string
	}{{JSON, "[" + a + "," + z + "]"}, {JSON, "[ \n" + a + ", \t\n" + z + " ]"}, {JSONL, a + "\n" + z + "\n"}} {
		rows, e := f.state.parse([]byte(tc.raw), tc.format, selected)
		if e != nil || len(rows) != 2 {
			t.Fatal(tc.format, e, len(rows))
		}
	}
	// Exercise the actual public exporter/importer at the same exact boundary.
	b := &fixtureBackend{rows: [][]any{{int64(1), "one", "allowed"}, {int64(2), "two", "allowed"}}}
	source, e := New(Config{Backend: b, Profiles: []ModelProfile{fixtureProfile()}, Limits: Limits{MaxRecordBytes: len(a)}})
	if e != nil {
		t.Fatal(e)
	}
	for _, format := range []Format{JSON, JSONL} {
		var out bytes.Buffer
		do := dumpOptions()
		do.Format = format
		if _, e := source.Dump(context.Background(), &out, do); e != nil {
			t.Fatal(e)
		}
		target, e := New(Config{Backend: &fixtureBackend{}, Profiles: []ModelProfile{fixtureProfile()}, Limits: Limits{MaxRecordBytes: len(a)}})
		if e != nil {
			t.Fatal(e)
		}
		lo := loadOptions()
		lo.Format = format
		if r, e := target.Load(context.Background(), &out, lo); e != nil || r.Records != 2 || !r.Committed {
			t.Fatal(r, e)
		}
	}
}

func FuzzFixtureNumberCanonical(f *testing.F) {
	for _, raw := range []string{"0", "-0.00", "1e2", "9007199254740993", strings.Repeat("1", 128), "99e2147483647", "0.1e-2147483648", "1e100000", "invalid"} {
		f.Add(raw)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		if len(raw) > 512 {
			t.Skip()
		}
		n, e := canonicalNumber(raw)
		if e != nil {
			return
		}
		again, e := canonicalNumber(string(n))
		if e != nil || n != again || !json.Valid([]byte(n)) {
			t.Fatal("non-idempotent canonical", raw, n, again, e)
		}
		// Math proof is bounded independently: huge exponent values still get
		// idempotence checks but never allocate huge big.Rat integers in fuzzing.
		bounded := true
		for _, s := range []string{raw, string(n)} {
			if i := strings.IndexAny(s, "eE"); i >= 0 {
				exp, e := strconv.ParseInt(s[i+1:], 10, 64)
				if e != nil || exp > 1000 || exp < -1000 {
					bounded = false
				}
			}
		}
		if bounded {
			a, ok := new(big.Rat).SetString(raw)
			if !ok {
				t.Fatal(raw)
			}
			b, ok := new(big.Rat).SetString(string(n))
			if !ok || a.Cmp(b) != 0 {
				t.Fatal("value changed", raw, n)
			}
		}
		field := models.Field{Kind: models.JSON}
		d, w, e := cell(field, map[string]any{"n": json.Number(raw)}, false)
		if e != nil {
			t.Fatal(e)
		}
		encoded, e := json.Marshal(w)
		if e != nil {
			t.Fatal(e)
		}
		decoder := json.NewDecoder(bytes.NewReader(encoded))
		decoder.UseNumber()
		nodes := 65536
		parsed, e := strictValue(decoder, 0, &nodes)
		if e != nil {
			t.Fatal(e)
		}
		r, e := fromWire(field, parsed, false)
		if e != nil || !reflect.DeepEqual(d, r) {
			t.Fatal("JSON cell failed roundtrip", raw, e)
		}
	})
}

func TestFixtureParserRecordAndDepthBudgets(t *testing.T) {
	f := fixture(t, &fixtureBackend{}, fixtureProfile())
	selected, _ := f.state.selectModels(JSON, []string{"example.Note"}, true)
	for _, raw := range []string{strings.Repeat("[", 34) + "0" + strings.Repeat("]", 34), `[{"version":1,"model":"example.Note","pk":{"id":{"value":1}},"fields":{"title":{"value":"` + strings.Repeat("x", MaxRecordBytes) + `"},"tenant":{"value":"allowed"}}}]`} {
		if _, e := f.state.parse([]byte(raw), JSON, selected); e == nil {
			t.Fatal("budget accepted")
		}
	}
}

type fixtureMutationContext struct {
	context.Context
	hook func()
	err  error
}

func (c *fixtureMutationContext) Err() error {
	if c.hook != nil {
		c.hook()
	}
	return c.err
}
func TestFixturesEntrySnapshotsAndTimeBeforeClose(t *testing.T) {
	b := &fixtureBackend{rows: [][]any{{int64(1), "title", "allowed"}}}
	p := fixtureProfile()
	f := fixture(t, b, p)
	other := fixture(t, &fixtureBackend{}, p)
	o := dumpOptions()
	first := true
	ctx := &fixtureMutationContext{Context: context.Background(), hook: func() {
		if first {
			first = false
			*f = *other
			o.Models[0] = "unknown.Model"
		}
	}}
	var out bytes.Buffer
	r, e := f.Dump(ctx, &out, o)
	if e != nil || r.Records != 1 {
		t.Fatal(r, e, out.String())
	}
	// The provider may retain a timestamp's Location and replace it at Close.
	// Freeze the raw cell before that callback, without mutating shared UTC.
	stamp := time.Date(2026, 4, 5, 6, 7, 8, 0, time.FixedZone("provider", 0))
	p = fixtureProfile()
	p.Schema.Fields[1].Kind = models.DateTime
	b = &fixtureBackend{rows: [][]any{{int64(1), stamp, "allowed"}}}
	b.rowHook = func(rows *fixtureRows) {
		rows.closeHook = func() { *stamp.Location() = *time.FixedZone("changed", 3600) }
	}
	f = fixture(t, b, p)
	out.Reset()
	if _, e = f.Dump(context.Background(), &out, dumpOptions()); e != nil || !strings.Contains(out.String(), "2026-04-05T06:07:08Z") {
		t.Fatal(out.String(), e)
	}
}

func TestFixtureLazyLocalTimeSnapshot(t *testing.T) {
	if os.Getenv("GOGO_FIXTURE_LOCAL_CHILD") == "1" {
		// Do not resolve Local via Date/Format/Zone before the snapshot under
		// test. This process's TZ affects no global location in the parent.
		original := time.Unix(0, 0).In(time.Local)
		budget := valueBudget{nodes: 16, text: 1024}
		v, e := detachRaw(original, &budget)
		if e != nil {
			t.Fatal(e)
		}
		stamp := v.(time.Time)
		if stamp.Location() == time.Local || stamp.Location() == time.UTC {
			t.Fatal("shared location")
		}
		for _, tc := range []struct {
			kind models.Kind
			want string
		}{{models.Date, "1969-12-31"}, {models.Time, "14:00:00"}, {models.DateTime, "1970-01-01T00:00:00Z"}} {
			_, wire, e := canonical(models.Field{Kind: tc.kind}, stamp, false)
			if e != nil || wire != tc.want {
				t.Fatal(tc.kind, wire, e)
			}
		}
		return
	}
	childContext, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	command := exec.CommandContext(childContext, os.Args[0], "-test.run=^TestFixtureLazyLocalTimeSnapshot$", "-test.count=1")
	for _, item := range os.Environ() {
		if !strings.HasPrefix(item, "TZ=") && !strings.HasPrefix(item, "GOGO_") {
			command.Env = append(command.Env, item)
		}
	}
	command.Env = append(command.Env, "TZ=Pacific/Honolulu", "GOGO_FIXTURE_LOCAL_CHILD=1")
	if output, e := command.CombinedOutput(); e != nil {
		t.Fatalf("fresh local-zone snapshot: %v\n%s", e, output)
	}
}

func FuzzFixtureJSONBoundary(f *testing.F) {
	for _, raw := range []string{"[]", "[" + fixtureWire(1, "one", "allowed") + "]", `null`, `[{"version":1}]`, `["\ud800"]`} {
		f.Add(raw)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		if len(raw) > 2*MaxRecordBytes {
			t.Skip()
		}
		b := &fixtureBackend{}
		g := fixture(t, b, fixtureProfile())
		selected, _ := g.state.selectModels(JSON, []string{"example.Note"}, true)
		records, e := g.state.parse([]byte(raw), JSON, selected)
		if e != nil {
			return
		}
		for _, record := range records {
			w, e := makeWire(record.p, record.record)
			if e != nil {
				t.Fatal(e)
			}
			encoded, _ := json.Marshal([]wireRecord{w})
			again, e := g.state.parse(encoded, JSON, selected)
			if e != nil || len(again) != 1 {
				t.Fatal(e)
			}
		}
		if b.begins != 0 || b.queries != 0 {
			t.Fatal("parser used provider")
		}
	})
}
