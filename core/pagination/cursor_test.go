package pagination

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Newton-School/gogo/core/security"
)

func TestCursorAuthenticatesEveryBindingDimensionAndPreservesExactPositions(t *testing.T) {
	secret, err := security.RandomToken(32)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := security.NewSigner(security.SigningKey{ID: "fixture", Value: []byte(secret)}, nil, "gogo.pagination.cursor.v1")
	if err != nil {
		t.Fatal(err)
	}
	codec, err := NewCursorCodec(CursorConfig{Signer: signer})
	if err != nil {
		t.Fatal(err)
	}
	binding := CursorBinding{Resource: "catalog.products", Version: "v1", Scope: "tenant/actor/policy-version", Query: "filter-order-hash"}
	position := []json.RawMessage{json.RawMessage(`"a/b"`), json.RawMessage(`9223372036854775807`)}
	token, err := codec.Encode(binding, position)
	if err != nil {
		t.Fatal(err)
	}
	var zero CursorCodec
	if _, err := zero.Encode(binding, position); err != ErrInvalid {
		t.Fatal("zero codec encode", err)
	}
	if _, err := zero.Decode(binding, token); err != ErrInvalid {
		t.Fatal("zero codec decode", err)
	}
	decoded, err := codec.Decode(binding, token)
	if err != nil || string(decoded[1]) != "9223372036854775807" || string(decoded[0]) != `"a/b"` {
		t.Fatal("position changed", err)
	}
	for _, field := range []string{"resource", "version", "scope", "query"} {
		changed := binding
		switch field {
		case "resource":
			changed.Resource = "other"
		case "version":
			changed.Version = "v2"
		case "scope":
			changed.Scope = "another tenant"
		case "query":
			changed.Query = "another filter"
		}
		if _, err := codec.Decode(changed, token); err != ErrInvalid {
			t.Fatal("binding replay accepted", field, err)
		}
	}
	for _, invalid := range []string{"", token + "x", token + ".x", strings.Repeat("a", MaxCursorBytes+1)} {
		if _, err := codec.Decode(binding, invalid); err != ErrInvalid {
			t.Fatal("invalid cursor accepted", err)
		}
	}
	for _, invalid := range [][]json.RawMessage{nil, {json.RawMessage(`{}`)}, {json.RawMessage(`[]`)}, {json.RawMessage(`1 2`)}, {json.RawMessage(`"` + strings.Repeat("x", 1024) + `"`)}} {
		if _, err := codec.Encode(binding, invalid); err != ErrInvalid {
			t.Fatal("invalid position accepted", err)
		}
	}
	for _, config := range []CursorConfig{{}, {Signer: signer, MaxAge: time.Nanosecond}, {Signer: signer, MaxAge: 25 * time.Hour}} {
		if _, err := NewCursorCodec(config); err != ErrInvalid {
			t.Fatal("invalid cursor config", err)
		}
	}
	otherSigner, _ := security.NewSigner(security.SigningKey{ID: "fixture", Value: []byte(secret)}, nil, "another-purpose")
	other, _ := NewCursorCodec(CursorConfig{Signer: otherSigner})
	if _, err := other.Decode(binding, token); err != ErrInvalid {
		t.Fatal("cross-purpose token accepted")
	}
}
