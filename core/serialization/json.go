package serialization

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/Newton-School/gogo/core/models"
	"io"
	"strconv"
	"unicode/utf8"
)

type wireRecord struct {
	Version int            `json:"version"`
	Model   string         `json:"model"`
	PK      map[string]any `json:"pk"`
	Fields  map[string]any `json:"fields"`
}
type loadedRecord struct {
	p      profile
	record Record
	wire   wireRecord
	index  int
}

func cell(f models.Field, v any, primary bool) (any, any, error) {
	d, w, e := canonical(f, v, primary)
	if e != nil {
		return nil, nil, e
	}
	if d == nil {
		return nil, map[string]any{"sql_null": true}, nil
	}
	return d, map[string]any{"value": w}, nil
}
func makeWire(p profile, r Record) (wireRecord, error) {
	w := wireRecord{Version: 1, Model: r.Model, PK: map[string]any{}, Fields: map[string]any{}}
	for _, n := range p.fields {
		f, _ := p.schema.Field(n)
		v := r.Fields[n]
		if isPK(p, n) {
			v = r.PK[n]
		}
		_, c, e := cell(f, v, isPK(p, n))
		if e != nil {
			return wireRecord{}, e
		}
		if isPK(p, n) {
			w.PK[n] = c
		} else {
			w.Fields[n] = c
		}
	}
	return w, nil
}
func isPK(p profile, n string) bool {
	for _, k := range p.pk {
		if k == n {
			return true
		}
	}
	return false
}

func readInput(ctx context.Context, r io.Reader, limit int) ([]byte, error) {
	if nilValue(r) {
		return nil, ErrInvalid
	}
	var out bytes.Buffer
	buf := make([]byte, 32768)
	empty := 0
	for {
		if e := contextError(ctx); e != nil {
			return nil, e
		}
		want := min(len(buf), limit+1-out.Len())
		n, e := r.Read(buf[:want])
		if n < 0 || n > want {
			return nil, ErrUnavailable
		}
		if n > 0 {
			out.Write(buf[:n])
			empty = 0
		} else {
			empty++
		}
		if out.Len() > limit {
			return nil, ErrLimit
		}
		if e2 := contextError(ctx); e2 != nil {
			return nil, e2
		}
		if e != nil {
			if e == io.EOF {
				return out.Bytes(), nil
			}
			return nil, ErrUnavailable
		}
		if empty > 100 {
			return nil, ErrUnavailable
		}
	}
}

// strictValue rejects duplicate keys and enforces depth/work limits before
// recursively allocating containers. Decoder numbers retain exact text.
func strictValue(d *json.Decoder, depth int, nodes *int) (any, error) {
	if depth > 32 || *nodes <= 0 {
		return nil, ErrLimit
	}
	*nodes--
	t, e := d.Token()
	if e != nil {
		return nil, ErrInvalid
	}
	if delim, ok := t.(json.Delim); ok {
		switch delim {
		case '{':
			m := map[string]any{}
			for d.More() {
				k, e := d.Token()
				if e != nil {
					return nil, ErrInvalid
				}
				s, ok := k.(string)
				if !ok {
					return nil, ErrInvalid
				}
				if _, ok := m[s]; ok {
					return nil, ErrInvalid
				}
				*nodes--
				v, e := strictValue(d, depth+1, nodes)
				if e != nil {
					return nil, e
				}
				m[s] = v
			}
			end, e := d.Token()
			if e != nil || end != json.Delim('}') {
				return nil, ErrInvalid
			}
			return m, nil
		case '[':
			a := []any{}
			for d.More() {
				v, e := strictValue(d, depth+1, nodes)
				if e != nil {
					return nil, e
				}
				a = append(a, v)
			}
			end, e := d.Token()
			if e != nil || end != json.Delim(']') {
				return nil, ErrInvalid
			}
			return a, nil
		}
		return nil, ErrInvalid
	}
	return t, nil
}

// encoding/json replaces unmatched surrogate escapes; fixture identities must
// instead refuse them. This also preflights original UTF-8 before decoding.
func validJSONText(raw []byte) bool {
	if !utf8.Valid(raw) {
		return false
	}
	quoted := false
	for i := 0; i < len(raw); i++ {
		c := raw[i]
		if c == '"' {
			quoted = !quoted
			continue
		}
		if !quoted || c != '\\' {
			continue
		}
		i++
		if i >= len(raw) {
			return false
		}
		if raw[i] != 'u' {
			continue
		}
		if i+4 >= len(raw) {
			return false
		}
		u, e := strconv.ParseUint(string(raw[i+1:i+5]), 16, 16)
		if e != nil {
			return false
		}
		i += 4
		if u >= 0xdc00 && u <= 0xdfff {
			return false
		}
		if u >= 0xd800 && u <= 0xdbff {
			if i+6 >= len(raw) || raw[i+1] != '\\' || raw[i+2] != 'u' {
				return false
			}
			v, e := strconv.ParseUint(string(raw[i+3:i+7]), 16, 16)
			if e != nil || v < 0xdc00 || v > 0xdfff {
				return false
			}
			i += 6
		}
	}
	return !quoted
}

func (s fixtureState) parse(raw []byte, format Format, selected []profile) ([]loadedRecord, error) {
	if !validJSONText(raw) {
		return nil, ErrInvalid
	}
	profiles := map[string]profile{}
	for _, p := range selected {
		profiles[p.schema.Key()] = p
	}
	out := []loadedRecord{}
	seen := map[string]bool{}
	appendValue := func(v any, size int) error {
		index := len(out) + 1
		if index > s.limits.MaxRecords || size > s.limits.MaxRecordBytes {
			return &Error{Record: index, kind: ErrLimit}
		}
		record, e := parseRecord(v, profiles, index)
		if e != nil {
			return e
		}
		id, _ := json.Marshal(record.wire.PK)
		key := record.p.schema.Key() + "\x00" + string(id)
		if seen[key] {
			return &Error{Record: index, kind: ErrInvalid}
		}
		seen[key] = true
		out = append(out, record)
		return nil
	}
	if format == JSONL {
		for line := range bytes.SplitSeq(raw, []byte{'\n'}) {
			line = bytes.TrimSpace(line)
			if len(line) == 0 {
				continue
			}
			if len(line) > s.limits.MaxRecordBytes {
				return nil, &Error{Record: len(out) + 1, kind: ErrLimit}
			}
			d := json.NewDecoder(bytes.NewReader(line))
			d.UseNumber()
			nodes := 65536
			v, e := strictValue(d, 0, &nodes)
			if e != nil {
				return nil, &Error{Record: len(out) + 1, kind: e}
			}
			if _, e = d.Token(); e != io.EOF {
				return nil, &Error{Record: len(out) + 1, kind: ErrInvalid}
			}
			if e = appendValue(v, len(line)); e != nil {
				return nil, e
			}
		}
		return out, nil
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	t, e := d.Token()
	if e != nil || t != json.Delim('[') {
		return nil, ErrInvalid
	}
	for d.More() {
		start := int(d.InputOffset())
		// More's peek can leave the array separator unconsumed. The record
		// budget covers the value itself, not its comma or surrounding space.
		for start < len(raw) && (raw[start] == ',' || raw[start] == ' ' || raw[start] == '\t' || raw[start] == '\r' || raw[start] == '\n') {
			start++
		}
		nodes := 65536
		v, e := strictValue(d, 0, &nodes)
		if e != nil {
			return nil, &Error{Record: len(out) + 1, kind: e}
		}
		if e = appendValue(v, int(d.InputOffset())-start); e != nil {
			return nil, e
		}
	}
	t, e = d.Token()
	if e != nil || t != json.Delim(']') {
		return nil, ErrInvalid
	}
	if _, e = d.Token(); e != io.EOF {
		return nil, ErrInvalid
	}
	return out, nil
}

func parseRecord(v any, profiles map[string]profile, index int) (loadedRecord, error) {
	fail := func(field string) (loadedRecord, error) {
		return loadedRecord{}, &Error{Record: index, Field: field, kind: ErrInvalid}
	}
	m, ok := v.(map[string]any)
	if !ok || len(m) != 4 || m["version"] != json.Number("1") {
		return fail("")
	}
	model, ok := m["model"].(string)
	if !ok {
		return fail("")
	}
	p, ok := profiles[model]
	if !ok {
		return fail("")
	}
	pk, ok := m["pk"].(map[string]any)
	if !ok || len(pk) != len(p.pk) {
		return fail("")
	}
	fields, ok := m["fields"].(map[string]any)
	if !ok || len(fields) != len(p.fields)-len(p.pk) {
		return fail("")
	}
	r := Record{Model: model, PK: map[string]any{}, Fields: map[string]any{}}
	for _, n := range p.fields {
		f, _ := p.schema.Field(n)
		c, exists := fields[n]
		if isPK(p, n) {
			c, exists = pk[n]
		}
		if !exists {
			return fail(n)
		}
		d, e := fromWire(f, c, isPK(p, n))
		if e != nil {
			return fail(n)
		}
		if isPK(p, n) {
			r.PK[n] = d
		} else {
			r.Fields[n] = d
		}
	}
	w, e := makeWire(p, r)
	if e != nil {
		return fail("")
	}
	return loadedRecord{p: p, record: r, wire: w, index: index}, nil
}
