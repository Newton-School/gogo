package http

import (
	htmltemplate "html/template"
	"math"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
	"unsafe"

	"github.com/Newton-School/gogo/core/templates"
)

type templateSnapshotString string

func (templateSnapshotString) String() string { panic("Stringer must not run") }

type templateSnapshotObject struct {
	Title  templateSnapshotString
	Values map[templateSnapshotString][]int
	hidden func()
}

func (templateSnapshotObject) String() string { panic("Stringer must not run") }
func (templateSnapshotObject) MarshalJSON() ([]byte, error) {
	panic("marshaler must not run")
}

func TestGenericTemplateSnapshotCopiesExportedDataWithoutMethods(t *testing.T) {
	object := &templateSnapshotObject{Title: "initial", Values: map[templateSnapshotString][]int{"numbers": {1, 2}}, hidden: func() { panic("private method") }}
	input := templates.Context{"first": object, "second": object, "array": [2]string{"a", "b"}}
	result, err := snapshotTemplateContext(input)
	if err != nil {
		t.Fatal(err)
	}
	wantObject := templates.Context{"Title": "initial", "Values": templates.Context{"numbers": []any{int64(1), int64(2)}}}
	if !reflect.DeepEqual(result["first"], wantObject) || !reflect.DeepEqual(result["second"], wantObject) || !reflect.DeepEqual(result["array"], []any{"a", "b"}) {
		t.Fatal("incorrect data projection", result)
	}
	object.Title = "changed"
	object.Values["numbers"][0] = 9
	delete(input, "second")
	if !reflect.DeepEqual(result["first"], wantObject) || !reflect.DeepEqual(result["second"], wantObject) {
		t.Fatal("source mutation reached snapshot", result)
	}
	first := result["first"].(templates.Context)
	first["Values"].(templates.Context)["numbers"].([]any)[1] = int64(8)
	if !reflect.DeepEqual(result["second"], wantObject) || object.Values["numbers"][1] != 2 {
		t.Fatal("output branches or source retain mutable aliases")
	}
}

func TestGenericTemplateSnapshotCanonicalScalarsAndExactHTMLMarkers(t *testing.T) {
	type namedBool bool
	type namedInt int16
	type namedUint uint16
	type namedFloat float32
	type namedSafe templates.SafeHTML
	type namedHTML htmltemplate.HTML
	safe, html := templates.SafeHTML("<b>safe</b>"), htmltemplate.HTML("<i>safe</i>")
	result, err := snapshotTemplateContext(templates.Context{
		"bool": namedBool(true), "int": namedInt(-2), "uint": namedUint(2), "uintptr": uintptr(3),
		"maximum": uint64(math.MaxUint64), "float": namedFloat(1.25), "string": templateSnapshotString("text"),
		"safe": safe, "html": html, "pointer_safe": &safe, "pointer_html": &html,
		"named_safe": namedSafe("<b>plain</b>"), "named_html": namedHTML("<i>plain</i>"),
	})
	if err != nil {
		t.Fatal(err)
	}
	want := templates.Context{
		"bool": true, "int": int64(-2), "uint": uint64(2), "uintptr": uint64(3),
		"maximum": uint64(math.MaxUint64), "float": float64(1.25), "string": "text",
		"safe": safe, "html": html, "pointer_safe": safe, "pointer_html": html,
		"named_safe": "<b>plain</b>", "named_html": "<i>plain</i>",
	}
	if !reflect.DeepEqual(result, want) {
		t.Fatal("scalar identity or HTML provenance changed", result)
	}
}

func TestGenericTemplateSnapshotPreservesNilAndQueryValues(t *testing.T) {
	if result, err := snapshotTemplateContext(nil); err != nil || result != nil {
		t.Fatal("nil context changed", result, err)
	}
	var pointer *int
	var dictionary map[string]int
	var sequence []int
	var queryNil url.Values
	query := url.Values{"multi": {"first", "second"}, "nil": nil, "empty": {}}
	result, err := snapshotTemplateContext(templates.Context{"nil": nil, "pointer": pointer, "map": dictionary, "slice": sequence, "query": &query, "query_nil": queryNil})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"nil", "pointer", "map", "slice"} {
		if result[name] != nil {
			t.Fatal("nil value changed", name)
		}
	}
	copy, ok := result["query"].(url.Values)
	if !ok || !reflect.DeepEqual(copy, query) || copy["nil"] != nil || copy["empty"] == nil {
		t.Fatal("query shape/type changed", result)
	}
	if value, ok := result["query_nil"].(url.Values); !ok || value != nil {
		t.Fatal("nil query identity changed")
	}
	query["multi"][0] = "changed"
	copy["multi"][1] = "detached"
	if copy.Get("multi") != "first" || query["multi"][1] != "second" {
		t.Fatal("query values share storage")
	}
}

func TestGenericTemplateSnapshotDetachesTimeLocation(t *testing.T) {
	zone := time.FixedZone("initial", 3600)
	instant := time.Date(2026, 1, 2, 3, 4, 5, 6, zone)
	result, err := snapshotTemplateContext(templates.Context{"time": instant, "pointer": &instant, "zero": time.Time{}})
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"time", "pointer"} {
		value, ok := result[key].(time.Time)
		if !ok || !value.Equal(instant) || value.Location() == zone || value.Location().String() != "initial" {
			t.Fatal("time snapshot changed or retained source location", key)
		}
	}
	*zone = *time.FixedZone("source_change", 7200)
	value := result["time"].(time.Time)
	if value.Location().String() != "initial" {
		t.Fatal("source Location assignment changed snapshot")
	}
	*value.Location() = *time.FixedZone("result_change", 0)
	if result["pointer"].(time.Time).Location().String() != "initial" || zone.String() != "source_change" {
		t.Fatal("snapshot Location assignment changed another owner")
	}
	zero := result["zero"].(time.Time)
	if !zero.IsZero() || zero.Location() == time.UTC {
		t.Fatal("zero time lost identity or retained shared UTC pointer")
	}
	*zero.Location() = *time.FixedZone("detached_utc", 3600)
	if time.UTC.String() != "UTC" {
		t.Fatal("snapshot exposed shared UTC")
	}
}

func TestGenericTemplateSnapshotRejectsUnsupportedOrMalformedData(t *testing.T) {
	var nilFunction func()
	var nilChannel chan int
	for _, value := range []any{
		nilFunction, func() {}, nilChannel, make(chan int), complex(1, 2), complex64(0), unsafe.Pointer(new(int)),
		map[int]string{}, map[int]string(nil), math.NaN(), math.Inf(1), math.Inf(-1), float32(math.Inf(1)),
		"bad\xff", templates.SafeHTML("bad\xff"), htmltemplate.HTML("bad\xff"),
		map[string]int{"bad\xff": 1}, url.Values{"bad\xff": {"value"}}, url.Values{"key": {"bad\xff"}},
		time.Now().In(time.FixedZone("bad\xff", 0)),
	} {
		result, err := snapshotTemplateContext(templates.Context{"value": value})
		if result != nil || err != ErrUnavailable {
			t.Fatalf("unsupported %T returned result=%v err=%v", value, result, err)
		}
	}
}

func TestGenericTemplateSnapshotBoundsCyclesSharedGraphsAndDepth(t *testing.T) {
	cyclicMap := map[string]any{}
	cyclicMap["cycle"] = cyclicMap
	cyclicSlice := make([]any, 1)
	cyclicSlice[0] = cyclicSlice
	var cyclicPointer any
	cyclicPointer = &cyclicPointer
	type branch struct{ Left, Right *branch }
	shared := &branch{}
	// This DAG fits the depth limit but its expanded copy exceeds node budget.
	for range 14 {
		shared = &branch{Left: shared, Right: shared}
	}
	for _, input := range []any{cyclicMap, cyclicSlice, cyclicPointer, shared} {
		if result, err := snapshotTemplateContext(templates.Context{"value": input}); result != nil || err != ErrUnavailable {
			t.Fatal("unbounded graph accepted", err)
		}
	}
	for _, depth := range []int{30, 31} {
		value := reflect.ValueOf(1)
		for range depth {
			pointer := reflect.New(value.Type())
			pointer.Elem().Set(value)
			value = pointer
		}
		// Root map depth0, interface value depth1, then pointer chain and int.
		result, err := snapshotTemplateContext(templates.Context{"value": value.Interface()})
		if depth == 30 {
			if err != nil || result["value"] != int64(1) {
				t.Fatal("exact depth limit rejected", err)
			}
		} else if err != ErrUnavailable || result != nil {
			t.Fatal("depth limit exceeded", err)
		}
	}
}

func TestGenericTemplateSnapshotCountsAllValuesAndMapKeys(t *testing.T) {
	// Four visits: context, map key, interface and slice; each byte is a value.
	for _, length := range []int{templateContextMaxValues - 4, templateContextMaxValues - 3} {
		result, err := snapshotTemplateContext(templates.Context{"v": make([]byte, length)})
		if length == templateContextMaxValues-4 {
			if err != nil || len(result["v"].([]any)) != length {
				t.Fatal("exact value budget rejected", err)
			}
		} else if result != nil || err != ErrUnavailable {
			t.Fatal("value budget exceeded", err)
		}
	}
	values := templates.Context{}
	for index := 0; index < templateContextMaxValues/2; index++ {
		values[strconv.Itoa(index)] = nil
	}
	if result, err := snapshotTemplateContext(values); result != nil || err != ErrUnavailable {
		t.Fatal("map keys were not charged to the value budget", err)
	}
}

func TestGenericTemplateSnapshotCountsAggregateUTF8Bytes(t *testing.T) {
	for _, size := range []int{templateContextMaxBytes - 1, templateContextMaxBytes} {
		result, err := snapshotTemplateContext(templates.Context{"v": strings.Repeat("a", size)})
		if size == templateContextMaxBytes-1 {
			if err != nil || len(result["v"].(string)) != size {
				t.Fatal("exact byte budget rejected", err)
			}
		} else if result != nil || err != ErrUnavailable {
			t.Fatal("string budget exceeded", err)
		}
	}
	chunk := strings.Repeat("é", (1<<20)/2)
	values := templates.Context{}
	for i := range 8 {
		values[strconv.Itoa(i)] = chunk
	}
	if result, err := snapshotTemplateContext(values); result != nil || err != ErrUnavailable {
		t.Fatal("repeated strings or UTF8 bytes escaped aggregate budget", err)
	}
	for _, value := range []any{url.Values{"key": {strings.Repeat("a", templateContextMaxBytes)}}, time.Now().In(time.FixedZone(strings.Repeat("a", templateContextMaxBytes), 0))} {
		if result, err := snapshotTemplateContext(templates.Context{"v": value}); result != nil || err != ErrUnavailable {
			t.Fatal("recognized type bypassed string budget", err)
		}
	}
}
