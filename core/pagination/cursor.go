package pagination

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"time"
	"unicode/utf8"

	"github.com/Newton-School/gogo/core/security"
)

const MaxCursorBytes = 8192

// CursorBinding identifies one resource version and its already-authorized
// query. Scope must change when visibility/tenant policy changes. Only its hash
// enters the cursor. Positions are signed, NOT encrypted: use only public,
// immutable ordering keys, and reapply scope/permissions on every page.
type CursorBinding struct{ Resource, Version, Scope, Query string }
type CursorConfig struct {
	Signer *security.Signer
	MaxAge time.Duration
}
type CursorCodec struct {
	signer *security.Signer
	maxAge time.Duration
}
type cursorPayload struct {
	Version  int               `json:"v"`
	Binding  string            `json:"b"`
	Position []json.RawMessage `json:"p"`
}

func NewCursorCodec(config CursorConfig) (*CursorCodec, error) {
	if config.MaxAge == 0 {
		config.MaxAge = time.Hour
	}
	if config.Signer == nil || config.MaxAge < time.Second || config.MaxAge > 24*time.Hour {
		return nil, ErrInvalid
	}
	return &CursorCodec{signer: config.Signer, maxAge: config.MaxAge}, nil
}

func cursorBinding(binding CursorBinding) (string, error) {
	for _, value := range []string{binding.Resource, binding.Version, binding.Scope, binding.Query} {
		if value == "" || len(value) > 4096 || !utf8.ValidString(value) {
			return "", ErrInvalid
		}
	}
	encoded, err := json.Marshal(binding)
	if err != nil {
		return "", ErrInvalid
	}
	hash := sha256.Sum256(encoded)
	return hex.EncodeToString(hash[:]), nil
}
func validPosition(position []json.RawMessage) bool {
	if len(position) < 1 || len(position) > 16 {
		return false
	}
	total := 0
	for _, raw := range position {
		total += len(raw)
		if len(raw) > 1024 || total > 4096 || !utf8.Valid(raw) {
			return false
		}
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.UseNumber()
		var value any
		if decoder.Decode(&value) != nil {
			return false
		}
		if _, err := decoder.Token(); err != io.EOF {
			return false
		}
		switch value.(type) {
		case string, json.Number, bool, nil:
		default:
			return false
		}
	}
	return true
}

func (c *CursorCodec) Encode(binding CursorBinding, position []json.RawMessage) (string, error) {
	if c == nil || c.signer == nil || !validPosition(position) {
		return "", ErrInvalid
	}
	hash, err := cursorBinding(binding)
	if err != nil {
		return "", err
	}
	payload, err := json.Marshal(cursorPayload{Version: 1, Binding: hash, Position: position})
	if err != nil {
		return "", ErrInvalid
	}
	token, err := c.signer.Sign(payload)
	if err != nil {
		return "", err
	}
	if len(token) > MaxCursorBytes {
		return "", ErrInvalid
	}
	return token, nil
}
func (c *CursorCodec) Decode(binding CursorBinding, token string) ([]json.RawMessage, error) {
	if c == nil || c.signer == nil || token == "" || len(token) > MaxCursorBytes {
		return nil, ErrInvalid
	}
	hash, err := cursorBinding(binding)
	if err != nil {
		return nil, ErrInvalid
	}
	raw, err := c.signer.Verify(token, c.maxAge)
	if err != nil || len(raw) > 6144 {
		return nil, ErrInvalid
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var payload cursorPayload
	if decoder.Decode(&payload) != nil {
		return nil, ErrInvalid
	}
	if _, err := decoder.Token(); err != io.EOF {
		return nil, ErrInvalid
	}
	if payload.Version != 1 || payload.Binding != hash || !validPosition(payload.Position) {
		return nil, ErrInvalid
	}
	return payload.Position, nil
}
