package http

import (
	"bytes"
	"context"
	"encoding"
	"encoding/json"
	"math"
	"reflect"
	"strconv"
	"strings"
	"unicode/utf8"
)

// WebSocketMessageDefinition is created only by WebSocketMessage. Its type
// schema and callback handles are private and immutable after construction.
type WebSocketMessageDefinition struct{ message *webSocketMessage }
type webSocketMessage struct {
	name          string
	version       uint32
	input, output *webSocketShape
	validate      func(context.Context, []byte) error
	invoke        func(context.Context, []byte, int) ([]byte, error)
}
type webSocketShape struct {
	kind    reflect.Kind
	typ     reflect.Type
	fields  []webSocketField
	element *webSocketShape
	size    int
}
type webSocketField struct {
	name  string
	index int
	shape *webSocketShape
}

// WebSocketMessage declares a concrete, explicitly tagged input/output struct.
// Missing fields are allowed; validate owns required and domain rules. It sees
// a separate decoded value from handle. Maps, interfaces, codecs, recursive or
// embedded types and JSON coercion tags are deliberately unsupported.
func WebSocketMessage[I, O any](name string, version uint32, validate func(context.Context, I) error, handle func(context.Context, I) (O, error)) (WebSocketMessageDefinition, error) {
	if !webSocketName(name, 64) || version == 0 || validate == nil || handle == nil {
		return WebSocketMessageDefinition{}, ErrWebSocketConfiguration
	}
	work := 0
	in, ok := webSocketType(reflect.TypeFor[I](), map[reflect.Type]bool{}, 0, &work)
	if !ok || in.kind != reflect.Struct {
		return WebSocketMessageDefinition{}, ErrWebSocketConfiguration
	}
	out, ok := webSocketType(reflect.TypeFor[O](), map[reflect.Type]bool{}, 0, &work)
	if !ok || out.kind != reflect.Struct {
		return WebSocketMessageDefinition{}, ErrWebSocketConfiguration
	}
	message := &webSocketMessage{name: name, version: version, input: in, output: out}
	message.validate = func(ctx context.Context, payload []byte) error {
		value, err := webSocketJSON(payload)
		if err != nil || !in.accept(value) {
			return ErrWebSocketInvalidMessage
		}
		var input I
		if json.Unmarshal(payload, &input) != nil {
			return ErrWebSocketInvalidMessage
		}
		return validate(ctx, input)
	}
	message.invoke = func(ctx context.Context, payload []byte, limit int) ([]byte, error) {
		inputValue, inputErr := webSocketJSON(payload)
		if inputErr != nil || !in.acceptLimit(inputValue, limit) {
			return nil, ErrWebSocketInvalidMessage
		}
		var input I
		if json.Unmarshal(payload, &input) != nil {
			return nil, ErrWebSocketInvalidMessage
		}
		output, err := handle(ctx, input)
		if err != nil {
			return nil, err
		}
		preflight := webSocketValueBudget{nodes: 65536, bytes: limit, memory: limit}
		if !preflight.check(reflect.ValueOf(output), out, 0, true) {
			return nil, ErrWebSocketInvalidMessage
		}
		budget := webSocketValueBudget{nodes: 65536, bytes: limit}
		copy, ok := budget.copy(reflect.ValueOf(output), out, 0)
		if !ok {
			return nil, ErrWebSocketInvalidMessage
		}
		// No callbacks between returned output and its bounded owned snapshot.
		encoded, err := json.Marshal(copy.Interface())
		if err != nil || len(encoded) > limit {
			return nil, ErrWebSocketInvalidMessage
		}
		value, err := webSocketJSON(encoded)
		if err != nil || !out.accept(value) {
			return nil, ErrWebSocketInvalidMessage
		}
		return encoded, nil
	}
	return WebSocketMessageDefinition{message: message}, nil
}

var webSocketJSONMethods = []reflect.Type{reflect.TypeFor[json.Marshaler](), reflect.TypeFor[json.Unmarshaler](), reflect.TypeFor[encoding.TextMarshaler](), reflect.TypeFor[encoding.TextUnmarshaler]()}

func webSocketName(name string, max int) bool {
	if len(name) == 0 || len(name) > max {
		return false
	}
	for i, c := range []byte(name) {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c == '_' || i > 0 && (c >= '0' && c <= '9' || c == '-' || c == '.')) {
			return false
		}
	}
	return true
}
func webSocketType(t reflect.Type, active map[reflect.Type]bool, depth int, work *int) (*webSocketShape, bool) {
	*work++
	if t == nil || depth > 32 || *work > 4096 || active[t] || t == reflect.TypeFor[json.Number]() || t.Size() > 8<<20 {
		return nil, false
	}
	for _, method := range webSocketJSONMethods {
		if t.Implements(method) || reflect.PointerTo(t).Implements(method) {
			return nil, false
		}
	}
	active[t] = true
	defer delete(active, t)
	s := &webSocketShape{kind: t.Kind(), typ: t}
	switch t.Kind() {
	case reflect.Bool, reflect.String, reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64, reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Float32, reflect.Float64:
	case reflect.Pointer, reflect.Slice, reflect.Array:
		if t.Kind() == reflect.Slice && t.Elem().Kind() == reflect.Uint8 {
			return nil, false
		}
		if t.Kind() == reflect.Array {
			s.size = t.Len()
			if s.size > 65536 {
				return nil, false
			}
		}
		var ok bool
		s.element, ok = webSocketType(t.Elem(), active, depth+1, work)
		if !ok {
			return nil, false
		}
	case reflect.Struct:
		if t.NumField() > 256 {
			return nil, false
		}
		seen := map[string]bool{}
		for i := range t.NumField() {
			field := t.Field(i)
			if field.Anonymous || field.PkgPath != "" {
				return nil, false
			}
			tag, ok := field.Tag.Lookup("json")
			if !ok {
				return nil, false
			}
			parts := strings.Split(tag, ",")
			if len(parts) > 2 || len(parts) == 2 && parts[1] != "omitempty" || !webSocketName(parts[0], 128) {
				return nil, false
			}
			fold := strings.ToLower(parts[0])
			if seen[fold] {
				return nil, false
			}
			seen[fold] = true
			shape, ok := webSocketType(field.Type, active, depth+1, work)
			if !ok {
				return nil, false
			}
			s.fields = append(s.fields, webSocketField{parts[0], i, shape})
		}
	default:
		return nil, false
	}
	return s, true
}

func (s *webSocketShape) accept(v *webSocketJSONValue) bool {
	return s.acceptLimit(v, 8<<20)
}
func (s *webSocketShape) acceptLimit(v *webSocketJSONValue, limit int) bool {
	if limit < 0 || limit > 8<<20 {
		return false
	}
	return s.acceptStorage(v, &limit, true)
}

// Predict Go storage from the parsed document before either typed decode.
// Omitted fixed fields still occupy space in each struct/slice element.
func (s *webSocketShape) acceptStorage(v *webSocketJSONValue, remaining *int, owned bool) bool {
	if v == nil {
		return false
	}
	if owned {
		if s.typ.Size() > uintptr(*remaining) {
			return false
		}
		*remaining -= int(s.typ.Size())
	}
	if v.kind == 'n' {
		return s.kind == reflect.Pointer || s.kind == reflect.Slice
	}
	if s.kind == reflect.Pointer {
		return s.element.acceptStorage(v, remaining, true)
	}
	switch s.kind {
	case reflect.Struct:
		if v.kind != '{' {
			return false
		}
		for name, child := range v.fields {
			found := false
			for _, field := range s.fields {
				if field.name == name {
					found = true
					if !field.shape.acceptStorage(child, remaining, false) {
						return false
					}
					break
				}
			}
			if !found {
				return false
			}
		}
		return true
	case reflect.Array, reflect.Slice:
		if v.kind != '[' || s.kind == reflect.Array && len(v.items) != s.size {
			return false
		}
		if s.kind == reflect.Slice {
			size := s.element.typ.Size()
			if size > 0 && uintptr(len(v.items)) > uintptr(*remaining)/size {
				return false
			}
			*remaining -= int(uintptr(len(v.items)) * size)
		}
		for _, item := range v.items {
			if !s.element.acceptStorage(item, remaining, false) {
				return false
			}
		}
		return true
	case reflect.String:
		if v.kind != '"' || len(v.text) > *remaining {
			return false
		}
		*remaining -= len(v.text)
		return true
	case reflect.Bool:
		return v.kind == 't' || v.kind == 'f'
	default:
		if v.kind != '0' {
			return false
		}
		if s.kind == reflect.Float32 || s.kind == reflect.Float64 {
			n, err := strconv.ParseFloat(string(v.raw), s.typ.Bits())
			return err == nil && !math.IsInf(n, 0) && !math.IsNaN(n)
		}
		if s.kind >= reflect.Int && s.kind <= reflect.Int64 {
			_, err := strconv.ParseInt(string(v.raw), 10, s.typ.Bits())
			return err == nil
		}
		_, err := strconv.ParseUint(string(v.raw), 10, s.typ.Bits())
		return err == nil
	}
}

type webSocketValueBudget struct{ nodes, bytes, memory int }

// Validate the whole returned value without allocating any destination. In
// particular, a short slice of huge fixed composites must be refused before
// reflect.MakeSlice or reflect.New can materialize its backing memory.
func (b *webSocketValueBudget) check(v reflect.Value, s *webSocketShape, depth int, owned bool) bool {
	b.nodes--
	b.bytes -= 8
	if b.nodes < 0 || b.bytes < 0 || depth > 32 || !v.IsValid() || v.Type() != s.typ {
		return false
	}
	if owned {
		if v.Type().Size() > uintptr(b.memory) {
			return false
		}
		b.memory -= int(v.Type().Size())
	}
	switch s.kind {
	case reflect.Pointer:
		return v.IsNil() || b.check(v.Elem(), s.element, depth+1, true)
	case reflect.Array, reflect.Slice:
		if v.Len() > b.nodes {
			return false
		}
		if s.kind == reflect.Slice {
			size := v.Type().Elem().Size()
			if size > 0 && uintptr(v.Len()) > uintptr(b.memory)/size {
				return false
			}
			b.memory -= int(uintptr(v.Len()) * size)
		}
		for i := range v.Len() {
			if !b.check(v.Index(i), s.element, depth+1, false) {
				return false
			}
		}
	case reflect.Struct:
		for _, field := range s.fields {
			b.nodes--
			b.bytes -= len(field.name)
			if b.nodes < 0 || b.bytes < 0 || !b.check(v.Field(field.index), field.shape, depth+1, false) {
				return false
			}
		}
	case reflect.String:
		size, ok := webSocketStringBytes(v.String(), b.bytes)
		if !ok {
			return false
		}
		b.bytes -= size
	case reflect.Float32, reflect.Float64:
		b.bytes -= 24
		if b.bytes < 0 || math.IsInf(v.Float(), 0) || math.IsNaN(v.Float()) {
			return false
		}
	default:
		b.bytes -= 24
		if b.bytes < 0 {
			return false
		}
	}
	return true
}

func (b *webSocketValueBudget) copy(v reflect.Value, s *webSocketShape, depth int) (reflect.Value, bool) {
	if b.nodes <= 0 || b.bytes < 8 || depth > 32 || !v.IsValid() || v.Type() != s.typ {
		return reflect.Value{}, false
	}
	o := reflect.New(v.Type()).Elem()
	if !b.copyInto(o, v, s, depth) {
		return reflect.Value{}, false
	}
	return o, true
}

// Fill fixed fields and array slots in place so the snapshot allocates each
// preflighted composite only once. Only pointers and slices own further storage.
func (b *webSocketValueBudget) copyInto(o, v reflect.Value, s *webSocketShape, depth int) bool {
	b.nodes--
	b.bytes -= 8 // Bound container punctuation and scalar work before encoding.
	if b.nodes < 0 || b.bytes < 0 || depth > 32 || !v.IsValid() || v.Type() != s.typ {
		return false
	}
	switch s.kind {
	case reflect.Pointer:
		if v.IsNil() {
			return true
		}
		o.Set(reflect.New(v.Type().Elem()))
		return b.copyInto(o.Elem(), v.Elem(), s.element, depth+1)
	case reflect.Slice, reflect.Array:
		if v.Len() > b.nodes {
			return false
		}
		if s.kind == reflect.Slice {
			if v.IsNil() {
				return true
			}
			o.Set(reflect.MakeSlice(v.Type(), v.Len(), v.Len()))
		}
		for i := range v.Len() {
			if !b.copyInto(o.Index(i), v.Index(i), s.element, depth+1) {
				return false
			}
		}
	case reflect.Struct:
		for _, field := range s.fields {
			b.nodes--
			b.bytes -= len(field.name)
			if b.nodes < 0 || b.bytes < 0 {
				return false
			}
			if !b.copyInto(o.Field(field.index), v.Field(field.index), field.shape, depth+1) {
				return false
			}
		}
	case reflect.String:
		size, ok := webSocketStringBytes(v.String(), b.bytes)
		if !ok {
			return false
		}
		b.bytes -= size
		o.Set(v)
	case reflect.Float32, reflect.Float64:
		if math.IsInf(v.Float(), 0) || math.IsNaN(v.Float()) {
			return false
		}
		o.Set(v)
	default:
		b.bytes -= 24
		if b.bytes < 0 {
			return false
		}
		o.Set(v)
	}
	return true
}

func webSocketStringBytes(value string, max int) (int, bool) {
	if !utf8.ValidString(value) {
		return 0, false
	}
	n := 0
	for _, r := range value {
		switch {
		case r == '"' || r == '\\':
			n += 2
		case r < 0x20 || r == '<' || r == '>' || r == '&' || r == 0x2028 || r == 0x2029:
			n += 6
		default:
			n += utf8.RuneLen(r)
		}
		if n > max {
			return 0, false
		}
	}
	return n, true
}

func webSocketEnvelopeBytes(value WebSocketEnvelope, max int) ([]byte, error) {
	encoded, err := json.Marshal(value)
	if err != nil || len(encoded) > max {
		return nil, ErrWebSocketInvalidMessage
	}
	return bytes.Clone(encoded), nil
}
