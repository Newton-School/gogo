package http

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

func FuzzWebSocketJSONSyntax(f *testing.F) {
	for _, seed := range []string{
		`{"value":"hello","items":[null,true,false,9007199254740993]}`,
		`{"value":"\ud83d\ude00"}`, `{"value":"\ud800"}`,
		`{"x":1,"\u0078":2}`, `1e9999999999999999`, `[-0,1.20,1e-5]`,
		"\r\n{}\t ", `{"a":[],"b":{}}`, `"\\u0000"`, "",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		if len(raw) > 1<<20 {
			t.Skip()
		}
		data := []byte(raw)
		before := bytes.Clone(data)
		value, err := webSocketJSON(data)
		if !bytes.Equal(data, before) {
			t.Fatal("parser changed caller bytes")
		}
		if err != nil {
			if value != nil {
				t.Fatal("invalid JSON exposed a partial value")
			}
			return
		}
		if !json.Valid(data) || !bytes.Equal(value.raw, bytes.Trim(data, " \t\r\n")) {
			t.Fatal("accepted invalid syntax or lost token boundaries")
		}
		decoder := json.NewDecoder(bytes.NewReader(data))
		decoder.UseNumber()
		var standard any
		if err := decoder.Decode(&standard); err != nil {
			t.Fatal("stdlib rejected accepted syntax", err)
		}
		assertWebSocketJSONValue(t, value, standard)
		encoded, err := json.Marshal(standard)
		if err != nil {
			t.Fatal("accepted value could not be encoded", err)
		}
		again, err := webSocketJSON(encoded)
		if err != nil {
			t.Fatal("owned JSON round trip failed", err)
		}
		assertWebSocketJSONValue(t, again, standard)
		spaced := append([]byte("\t\r\n "), data...)
		spaced = append(spaced, []byte(" \r\n\t")...)
		if _, err := webSocketJSON(spaced); err != nil {
			t.Fatal("legal surrounding whitespace changed acceptance", err)
		}
		if _, err := webSocketJSON(append(bytes.Clone(data), []byte(" null")...)); err == nil {
			t.Fatal("accepted trailing JSON document")
		}
	})
}

func assertWebSocketJSONValue(t *testing.T, value *webSocketJSONValue, expected any) {
	t.Helper()
	if value == nil {
		t.Fatal("missing parsed value")
	}
	switch expected := expected.(type) {
	case nil:
		if value.kind != 'n' {
			t.Fatal("null changed")
		}
	case bool:
		if expected && value.kind != 't' || !expected && value.kind != 'f' {
			t.Fatal("boolean changed")
		}
	case string:
		if value.kind != '"' || value.text != expected {
			t.Fatal("string changed")
		}
	case json.Number:
		if value.kind != '0' || string(value.raw) != string(expected) {
			t.Fatal("number rounded or rewritten")
		}
	case []any:
		if value.kind != '[' || len(value.items) != len(expected) {
			t.Fatal("array shape changed")
		}
		for i, child := range expected {
			assertWebSocketJSONValue(t, value.items[i], child)
		}
	case map[string]any:
		if value.kind != '{' || len(value.fields) != len(expected) {
			t.Fatal("object shape changed")
		}
		for name, child := range expected {
			assertWebSocketJSONValue(t, value.fields[name], child)
		}
	default:
		t.Fatalf("unexpected stdlib type %T", expected)
	}
}

func FuzzWebSocketTypedInput(f *testing.F) {
	type child struct {
		Count uint64 `json:"count"`
	}
	type input struct {
		Text     string    `json:"text"`
		Number   int64     `json:"number"`
		Float    float64   `json:"float"`
		Items    []child   `json:"items"`
		Fixed    [2]string `json:"fixed"`
		Optional *child    `json:"optional"`
	}
	definition, err := WebSocketMessage("typed", 1, func(context.Context, input) error { return nil }, func(_ context.Context, value input) (input, error) { return value, nil })
	if err != nil {
		f.Fatal(err)
	}
	for _, seed := range []string{`{}`, `{"number":9007199254740993}`, `{"items":[{"count":18446744073709551615}]}`, `{"fixed":["a","b"],"optional":null}`, `{"items":null}`, `{"Text":"wrong case"}`, `{"float":1e999}`, `{"number":null}`} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		if len(raw) > 64<<10 {
			t.Skip()
		}
		value, err := webSocketJSON([]byte(raw))
		if err != nil || !definition.message.input.accept(value) {
			return
		}
		var decoded input
		decoder := json.NewDecoder(strings.NewReader(raw))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&decoded); err != nil {
			t.Fatal("typed shape allowed a decoder failure", err)
		}
		if err := definition.message.validate(context.Background(), []byte(raw)); err != nil {
			t.Fatal("shape and validation decoding differ", err)
		}
		output, err := definition.message.invoke(context.Background(), []byte(raw), 8<<20)
		if err != nil {
			t.Fatal("small valid typed input failed bounded echo", err)
		}
		var observed input
		if json.Unmarshal(output, &observed) != nil || !reflect.DeepEqual(decoded, observed) {
			t.Fatal("typed echo lost data")
		}
	})
}

func FuzzWebSocketRequestSnapshot(f *testing.F) {
	f.Add("/socket/", "name=value", "Bearer test", "retained")
	f.Add("/socket/%", "", "", "")
	f.Fuzz(func(t *testing.T, path, query, header, private string) {
		if len(path)+len(query)+len(header)+len(private) > 64<<10 {
			t.Skip()
		}
		request := webSocketHandshakeRequest()
		request.URL.Path, request.URL.RawQuery = path, query
		request.RequestURI = path
		request.Header.Set("Authorization", header)
		request.SetPathValue("private", private)
		request.Form = map[string][]string{"private": {private}}
		owned, ok := webSocketRequest(request)
		if !ok {
			if owned != nil {
				t.Fatal("failed request snapshot exposed a value")
			}
			return
		}
		if owned.URL == request.URL || owned.Body != http.NoBody || owned.GetBody != nil || owned.Form != nil || owned.PostForm != nil || owned.MultipartForm != nil || owned.TLS != nil || owned.Response != nil || owned.Cancel != nil || owned.PathValue("private") != "" {
			t.Fatal("private or aliased request state escaped")
		}
		fresh := webSocketFreshRequest(owned, context.Background())
		fresh.URL.Path = "/changed/"
		fresh.Header["Authorization"][0] = "changed"
		fresh.SetPathValue("private", "changed")
		request.URL.RawQuery = "changed"
		request.Header.Set("Authorization", "changed")
		if owned.URL.Path != path || owned.URL.RawQuery != query || owned.Header.Get("Authorization") != header || owned.PathValue("private") != "" {
			t.Fatal("callback or caller mutation retargeted the snapshot")
		}
	})
}
