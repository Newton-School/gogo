package http

import (
	"encoding/json"
	"strconv"
	"unicode/utf8"
)

type webSocketJSONValue struct {
	kind   byte
	raw    []byte
	text   string
	fields map[string]*webSocketJSONValue
	items  []*webSocketJSONValue
}
type webSocketJSONParser struct {
	data       []byte
	pos, nodes int
}

func webSocketJSON(data []byte) (*webSocketJSONValue, error) {
	if len(data) == 0 || len(data) > 8<<20 || !utf8.Valid(data) {
		return nil, ErrWebSocketInvalidMessage
	}
	p := webSocketJSONParser{data: data}
	v, ok := p.value(0)
	p.space()
	if !ok || p.pos != len(data) {
		return nil, ErrWebSocketInvalidMessage
	}
	return v, nil
}
func (p *webSocketJSONParser) space() {
	for p.pos < len(p.data) {
		switch p.data[p.pos] {
		case ' ', '\t', '\n', '\r':
			p.pos++
		default:
			return
		}
	}
}
func (p *webSocketJSONParser) take(c byte) bool {
	p.space()
	if p.pos < len(p.data) && p.data[p.pos] == c {
		p.pos++
		return true
	}
	return false
}
func (p *webSocketJSONParser) value(depth int) (*webSocketJSONValue, bool) {
	p.nodes++
	p.space()
	if depth > 32 || p.nodes > 65536 || p.pos == len(p.data) {
		return nil, false
	}
	start := p.pos
	v := &webSocketJSONValue{kind: p.data[start]}
	switch v.kind {
	case '"':
		text, ok := p.string()
		if !ok {
			return nil, false
		}
		v.text = text
	case '{':
		p.pos++
		v.fields = map[string]*webSocketJSONValue{}
		if !p.take('}') {
			for {
				p.nodes++
				if p.nodes > 65536 {
					return nil, false
				}
				p.space()
				key, ok := p.string()
				if !ok || v.fields[key] != nil || !p.take(':') {
					return nil, false
				}
				item, ok := p.value(depth + 1)
				if !ok {
					return nil, false
				}
				v.fields[key] = item
				if p.take('}') {
					break
				}
				if !p.take(',') {
					return nil, false
				}
			}
		}
	case '[':
		p.pos++
		if !p.take(']') {
			for {
				item, ok := p.value(depth + 1)
				if !ok {
					return nil, false
				}
				v.items = append(v.items, item)
				if p.take(']') {
					break
				}
				if !p.take(',') {
					return nil, false
				}
			}
		}
	case 'n', 't', 'f':
		word := "null"
		if v.kind == 't' {
			word = "true"
		}
		if v.kind == 'f' {
			word = "false"
		}
		if len(p.data)-p.pos < len(word) || string(p.data[p.pos:p.pos+len(word)]) != word {
			return nil, false
		}
		p.pos += len(word)
	default:
		v.kind = '0'
		if !p.number() {
			return nil, false
		}
	}
	v.raw = p.data[start:p.pos]
	return v, true
}
func (p *webSocketJSONParser) number() bool {
	if p.pos < len(p.data) && p.data[p.pos] == '-' {
		p.pos++
	}
	if p.pos == len(p.data) {
		return false
	}
	if p.data[p.pos] == '0' {
		p.pos++
	} else {
		if p.data[p.pos] < '1' || p.data[p.pos] > '9' {
			return false
		}
		for p.pos < len(p.data) && p.data[p.pos] >= '0' && p.data[p.pos] <= '9' {
			p.pos++
		}
	}
	if p.pos < len(p.data) && p.data[p.pos] == '.' {
		p.pos++
		start := p.pos
		for p.pos < len(p.data) && p.data[p.pos] >= '0' && p.data[p.pos] <= '9' {
			p.pos++
		}
		if p.pos == start {
			return false
		}
	}
	if p.pos < len(p.data) && (p.data[p.pos] == 'e' || p.data[p.pos] == 'E') {
		p.pos++
		if p.pos < len(p.data) && (p.data[p.pos] == '+' || p.data[p.pos] == '-') {
			p.pos++
		}
		start := p.pos
		for p.pos < len(p.data) && p.data[p.pos] >= '0' && p.data[p.pos] <= '9' {
			p.pos++
		}
		if p.pos == start {
			return false
		}
	}
	return true
}
func (p *webSocketJSONParser) hex4() (uint16, bool) {
	if len(p.data)-p.pos < 4 {
		return 0, false
	}
	var value uint16
	for range 4 {
		c := p.data[p.pos]
		p.pos++
		var n byte
		switch {
		case c >= '0' && c <= '9':
			n = c - '0'
		case c >= 'a' && c <= 'f':
			n = c - 'a' + 10
		case c >= 'A' && c <= 'F':
			n = c - 'A' + 10
		default:
			return 0, false
		}
		value = value*16 + uint16(n)
	}
	return value, true
}
func (p *webSocketJSONParser) string() (string, bool) {
	if p.pos == len(p.data) || p.data[p.pos] != '"' {
		return "", false
	}
	start := p.pos
	p.pos++
	for p.pos < len(p.data) {
		c := p.data[p.pos]
		p.pos++
		if c == '"' {
			var value string
			if json.Unmarshal(p.data[start:p.pos], &value) != nil {
				return "", false
			}
			return value, true
		}
		if c < 0x20 {
			return "", false
		}
		if c != '\\' {
			continue
		}
		if p.pos == len(p.data) {
			return "", false
		}
		escape := p.data[p.pos]
		p.pos++
		switch escape {
		case '"', '\\', '/', 'b', 'f', 'n', 'r', 't':
		case 'u':
			first, ok := p.hex4()
			if !ok {
				return "", false
			}
			if first >= 0xdc00 && first <= 0xdfff {
				return "", false
			}
			if first >= 0xd800 && first <= 0xdbff {
				if len(p.data)-p.pos < 2 || p.data[p.pos] != '\\' || p.data[p.pos+1] != 'u' {
					return "", false
				}
				p.pos += 2
				second, ok := p.hex4()
				if !ok || second < 0xdc00 || second > 0xdfff {
					return "", false
				}
			}
		default:
			return "", false
		}
	}
	return "", false
}
func webSocketDecodeEnvelope(data []byte) (WebSocketEnvelope, *webSocketJSONValue, error) {
	value, err := webSocketJSON(data)
	if err != nil || value.kind != '{' || len(value.fields) != 3 {
		return WebSocketEnvelope{}, nil, ErrWebSocketInvalidMessage
	}
	typ, version, payload := value.fields["type"], value.fields["version"], value.fields["payload"]
	if typ == nil || typ.kind != '"' || !webSocketName(typ.text, 64) || version == nil || version.kind != '0' || payload == nil || payload.kind != '{' {
		return WebSocketEnvelope{}, nil, ErrWebSocketInvalidMessage
	}
	n, err := strconv.ParseUint(string(version.raw), 10, 32)
	if err != nil || n == 0 {
		return WebSocketEnvelope{}, nil, ErrWebSocketInvalidMessage
	}
	return WebSocketEnvelope{Type: typ.text, Version: uint32(n), Payload: append(json.RawMessage(nil), payload.raw...)}, payload, nil
}
