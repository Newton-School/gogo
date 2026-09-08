package http

import (
	"context"
	"encoding/json"
	"math"
	"reflect"
	"strings"
	"testing"
)

type webSocketInput struct {
	Text   string   `json:"text"`
	Number int64    `json:"number"`
	Values []string `json:"values,omitempty"`
}

func TestWebSocketOutputPreflightRejectsBeforeCompositeAllocation(t *testing.T) {
	type huge struct {
		Data [2048][8192]byte `json:"data"`
	}
	webSocketRejectType[huge](t)
	type block struct {
		Data [8192]byte `json:"data"`
	}
	type result struct {
		Blocks []block `json:"blocks"`
	}
	work := 0
	shape, ok := webSocketType(reflect.TypeFor[result](), map[reflect.Type]bool{}, 0, &work)
	if !ok {
		t.Fatal("fixture schema")
	}
	value := reflect.ValueOf(result{Blocks: make([]block, 256)})
	allocs := testing.AllocsPerRun(5, func() {
		budget := webSocketValueBudget{nodes: 65536, bytes: 128, memory: 128}
		if budget.check(value, shape, 0, true) {
			t.Fatal("accepted large backing allocation")
		}
	})
	if allocs != 0 {
		t.Fatal("refusal allocated a snapshot", allocs)
	}
}

func TestWebSocketCopyAllocatesFixedCompositesOnce(t *testing.T) {
	type output struct {
		Values [1][1][1][1][128]string `json:"values"`
	}
	work := 0
	shape, ok := webSocketType(reflect.TypeFor[output](), map[reflect.Type]bool{}, 0, &work)
	if !ok {
		t.Fatal("fixture schema")
	}
	value := reflect.ValueOf(output{})
	preflight := webSocketValueBudget{nodes: 65536, bytes: 4096, memory: 4096}
	if !preflight.check(value, shape, 0, true) {
		t.Fatal("bounded fixed-array fixture failed preflight")
	}
	var snapshot reflect.Value
	allocs := testing.AllocsPerRun(5, func() {
		budget := webSocketValueBudget{nodes: 65536, bytes: 4096}
		var copied bool
		snapshot, copied = budget.copy(value, shape, 0)
		if !copied {
			t.Fatal("bounded fixed-array snapshot failed")
		}
	})
	if allocs > 2 {
		t.Fatal("fixed fields/array slots allocated separate snapshots", allocs)
	}
	if snapshot.Type() != value.Type() || snapshot.Interface().(output) != (output{}) {
		t.Fatal("snapshot changed fixed values")
	}
	snapshot.Field(0).Index(0).Index(0).Index(0).Index(0).Index(0).SetString("detached")
	if value.Interface().(output).Values[0][0][0][0][0] != "" {
		t.Fatal("snapshot retained fixed-array source storage")
	}
}

func TestWebSocketInputPreflightCountsOmittedFixedStorage(t *testing.T) {
	type block struct {
		Huge [65536]string `json:"huge"`
	}
	type input struct {
		Items []block `json:"items"`
	}
	work := 0
	shape, ok := webSocketType(reflect.TypeFor[input](), map[reflect.Type]bool{}, 0, &work)
	if !ok {
		t.Fatal("fixture schema")
	}
	one, err := webSocketJSON([]byte(`{"items":[{}]}`))
	if err != nil || !shape.acceptLimit(one, 2<<20) {
		t.Fatal("positive storage budget", err)
	}
	many, err := webSocketJSON([]byte(`{"items":[` + strings.Repeat(`{},`, 999) + `{}]}`))
	if err != nil {
		t.Fatal(err)
	}
	allocs := testing.AllocsPerRun(5, func() {
		if shape.acceptLimit(many, 8<<20) {
			t.Fatal("accepted gigabyte decode allocation")
		}
	})
	if allocs != 0 {
		t.Fatal("preflight allocated typed input", allocs)
	}
	// Do not decode or construct the rejected giant value in this regression.
}

type webSocketOutput struct {
	Text   string `json:"text"`
	Number int64  `json:"number"`
}

func webSocketDefinition(t testing.TB, handle func(context.Context, webSocketInput) (webSocketOutput, error)) WebSocketMessageDefinition {
	t.Helper()
	if handle == nil {
		handle = func(_ context.Context, input webSocketInput) (webSocketOutput, error) {
			return webSocketOutput{input.Text, input.Number}, nil
		}
	}
	definition, err := WebSocketMessage("echo", 1, func(context.Context, webSocketInput) error { return nil }, handle)
	if err != nil {
		t.Fatal(err)
	}
	return definition
}

type webSocketCodec struct {
	Value string `json:"value"`
}

func (webSocketCodec) MarshalJSON() ([]byte, error) { panic("codec must not execute") }
func (*webSocketCodec) UnmarshalJSON([]byte) error  { panic("codec must not execute") }

type webSocketRecursive struct {
	Next *webSocketRecursive `json:"next"`
}
type webSocketEmbedded struct{ webSocketInput }
type webSocketHidden struct {
	secret string
}
type webSocketBytes struct {
	Value []byte `json:"value"`
}
type webSocketNumber struct {
	Value json.Number `json:"value"`
}
type webSocketCoercion struct {
	Value int `json:"value,string"`
}
type webSocketZero struct {
	Value string `json:"value,omitzero"`
}
type webSocketAmbiguous struct {
	A string `json:"value"`
	B string `json:"Value"`
}

func webSocketRejectType[T any](t *testing.T) {
	t.Helper()
	if _, err := WebSocketMessage("bad", 1, func(context.Context, T) error { return nil }, func(context.Context, T) (webSocketOutput, error) { return webSocketOutput{}, nil }); err != ErrWebSocketConfiguration {
		t.Fatalf("accepted type %T", *new(T))
	}
}
func TestWebSocketRejectsUnboundedOrImplicitCodecs(t *testing.T) {
	webSocketRejectType[webSocketCodec](t)
	webSocketRejectType[webSocketRecursive](t)
	webSocketRejectType[webSocketEmbedded](t)
	webSocketRejectType[webSocketHidden](t)
	webSocketRejectType[webSocketBytes](t)
	webSocketRejectType[webSocketNumber](t)
	webSocketRejectType[webSocketCoercion](t)
	webSocketRejectType[webSocketZero](t)
	webSocketRejectType[webSocketAmbiguous](t)
	webSocketRejectType[map[string]string](t)
	webSocketRejectType[struct {
		Value any `json:"value"`
	}](t)
	webSocketRejectType[struct{ Value string }](t)
	webSocketRejectType[*webSocketInput](t)
	// Build the deliberately invalid tagged private field dynamically so vet
	// still checks ordinary source declarations without a known-bad fixture.
	hidden := reflect.StructOf([]reflect.StructField{{
		Name: "secret", PkgPath: "http", Type: reflect.TypeFor[string](), Tag: `json:"secret"`,
	}})
	work := 0
	if _, ok := webSocketType(hidden, map[reflect.Type]bool{}, 0, &work); ok {
		t.Fatal("accepted tagged unexported field")
	}
	if _, err := WebSocketMessage("echo", 0, func(context.Context, webSocketInput) error { return nil }, func(context.Context, webSocketInput) (webSocketOutput, error) { return webSocketOutput{}, nil }); err == nil {
		t.Fatal("accepted zero version")
	}
}
func TestWebSocketStrictJSONAndDetachedValidation(t *testing.T) {
	validations := 0
	d, err := WebSocketMessage("echo", 1, func(_ context.Context, input webSocketInput) error {
		validations++
		input.Values[0] = "mutated"
		return nil
	}, func(_ context.Context, input webSocketInput) (webSocketOutput, error) {
		if input.Values[0] != "original" {
			t.Fatal("validation leaked into effect")
		}
		return webSocketOutput{input.Text, input.Number}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	data := []byte(`{"type":"echo","version":1,"payload":{"text":"ok","number":9007199254740993,"values":["original"]}}`)
	envelope, payload, err := webSocketDecodeEnvelope(data)
	if err != nil || !d.message.input.accept(payload) {
		t.Fatal(err)
	}
	if err := d.message.validate(context.Background(), envelope.Payload); err != nil {
		t.Fatal(err)
	}
	output, err := d.message.invoke(context.Background(), envelope.Payload, 1024)
	if err != nil || validations != 1 || !strings.Contains(string(output), `9007199254740993`) {
		t.Fatal(string(output), err)
	}
	for _, bad := range []string{
		`{"type":"echo","type":"echo","version":1,"payload":{}}`,
		`{"type":"echo","version":1,"payload":{"text":"a","te\u0078t":"b"}}`,
		`{"type":"echo","version":1,"payload":{"Text":"a"}}`,
		`{"type":"echo","version":1,"payload":{"unknown":true}}`,
		`{"type":"echo","version":1,"payload":{"text":null}}`,
		`{"type":"echo","version":1,"payload":{"number":1.2}}`,
		`{"type":"echo","version":1,"payload":{"number":9223372036854775808}}`,
		`{"type":"echo","version":1,"payload":{"text":"\ud800"}}`,
		`{"type":"echo","version":1,"payload":{"text":"\udc00"}}`,
		`{"type":"echo","version":1,"payload":{"text":"` + string([]byte{0xff}) + `"}}`,
		`{"type":"echo","version":1,"payload":{}} true`,
	} {
		_, p, err := webSocketDecodeEnvelope([]byte(bad))
		if err == nil && d.message.input.accept(p) {
			t.Fatal("accepted", bad)
		}
	}
	for _, good := range []string{`{}`, `{"values":null}`, `{"text":"\ud83d\ude00"}`} {
		p, err := webSocketJSON([]byte(good))
		if err != nil || !d.message.input.accept(p) {
			t.Fatal("rejected", good, err)
		}
	}
}
func TestWebSocketArrayNullAndOutputLimits(t *testing.T) {
	type input struct {
		Fixed    [2]int `json:"fixed"`
		Optional *int   `json:"optional"`
	}
	type output struct {
		Value float64 `json:"value"`
		Text  string  `json:"text"`
	}
	d, err := WebSocketMessage("array", 1, func(context.Context, input) error { return nil }, func(context.Context, input) (output, error) { return output{math.Inf(1), ""}, nil })
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{`{"fixed":[1]}`, `{"fixed":[1,2,3]}`, `{"fixed":null}`} {
		v, e := webSocketJSON([]byte(raw))
		if e == nil && d.message.input.accept(v) {
			t.Fatal("lossy array", raw)
		}
	}
	v, err := webSocketJSON([]byte(`{"fixed":[1,2],"optional":null}`))
	if err != nil || !d.message.input.accept(v) {
		t.Fatal(err)
	}
	if _, err := d.message.invoke(context.Background(), []byte(`{}`), 1024); err == nil {
		t.Fatal("accepted infinite output")
	}
	echo := webSocketDefinition(t, nil)
	if _, err := echo.message.invoke(context.Background(), []byte(`{"text":"`+strings.Repeat("x", 256)+`"}`), 128); err == nil {
		t.Fatal("accepted oversized output")
	}
	if _, err := webSocketJSON([]byte(strings.Repeat("[", 34) + "0" + strings.Repeat("]", 34))); err == nil {
		t.Fatal("accepted deep JSON")
	}
	if _, err := webSocketJSON([]byte("[" + strings.Repeat("0,", 65536) + "0]")); err == nil {
		t.Fatal("accepted too many nodes")
	}
}

func FuzzWebSocketEnvelope(f *testing.F) {
	for _, seed := range []string{`{"type":"echo","version":1,"payload":{"text":"ok"}}`, `{"type":"echo","version":1,"payload":{"text":"\ud800"}}`, `{"type":"echo","version":1,"payload":{"a":1,"\u0061":2}}`, "", `[]`} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		if len(raw) > 1<<20 {
			t.Skip()
		}
		envelope, _, err := webSocketDecodeEnvelope([]byte(raw))
		if err != nil {
			if envelope.Type != "" || envelope.Payload != nil {
				t.Fatal("partial error")
			}
			return
		}
		encoded, err := webSocketEnvelopeBytes(envelope, 8<<20)
		if err != nil {
			t.Fatal(err)
		}
		again, _, err := webSocketDecodeEnvelope(encoded)
		if err != nil || again.Type != envelope.Type || again.Version != envelope.Version {
			t.Fatal("unstable envelope", err)
		}
	})
}
