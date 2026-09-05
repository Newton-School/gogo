// Package security provides signing, CSRF and browser trust boundaries.
package security

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

var ErrBadSignature = errors.New("bad signature")
var ErrExpiredSignature = errors.New("expired signature")

type SigningKey struct {
	ID       string
	Value    []byte
	NotAfter time.Time
}
type Signer struct {
	current   SigningKey
	fallbacks []SigningKey
	purpose   string
	now       func() time.Time
	maxBytes  int
}

func NewSigner(current SigningKey, fallbacks []SigningKey, purpose string) (*Signer, error) {
	if len(current.Value) < 32 || current.ID == "" || purpose == "" {
		return nil, errors.New("signing key, identity and purpose required")
	}
	seen := map[string]bool{current.ID: true}
	current.Value = append([]byte(nil), current.Value...)
	copyKeys := make([]SigningKey, len(fallbacks))
	for i, k := range fallbacks {
		if len(k.Value) < 32 || k.ID == "" || seen[k.ID] || k.NotAfter.IsZero() {
			return nil, errors.New("fallback signing keys require distinct IDs and expiry")
		}
		seen[k.ID] = true
		k.Value = append([]byte(nil), k.Value...)
		copyKeys[i] = k
	}
	return &Signer{current: current, fallbacks: copyKeys, purpose: purpose, now: time.Now, maxBytes: 1 << 20}, nil
}

type signedPayload struct {
	Key     string `json:"k"`
	Purpose string `json:"p"`
	Issued  int64  `json:"t"`
	Value   []byte `json:"v"`
}

func (s *Signer) Sign(value []byte) (string, error) {
	if len(value) > s.maxBytes {
		return "", errors.New("signing payload too large")
	}
	now := s.now()
	if !s.current.NotAfter.IsZero() && !now.Before(s.current.NotAfter) {
		return "", ErrExpiredSignature
	}
	raw, err := json.Marshal(signedPayload{s.current.ID, s.purpose, now.Unix(), value})
	if err != nil {
		return "", err
	}
	payload := base64.RawURLEncoding.EncodeToString(raw)
	mac := hmac.New(sha256.New, s.current.Value)
	_, _ = mac.Write([]byte(payload))
	return payload + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}
func (s *Signer) Verify(token string, maxAge time.Duration) ([]byte, error) {
	if maxAge <= 0 || len(token) > s.maxBytes*2+1024 {
		return nil, ErrBadSignature
	}
	payload, tag, ok := strings.Cut(token, ".")
	if !ok || strings.Contains(tag, ".") {
		return nil, ErrBadSignature
	}
	raw, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil {
		return nil, ErrBadSignature
	}
	supplied, err := base64.RawURLEncoding.DecodeString(tag)
	if err != nil || len(supplied) != sha256.Size {
		return nil, ErrBadSignature
	}
	var p signedPayload
	if json.Unmarshal(raw, &p) != nil || p.Purpose != s.purpose || len(p.Value) > s.maxBytes {
		return nil, ErrBadSignature
	}
	now := s.now()
	var key []byte
	for _, k := range append([]SigningKey{s.current}, s.fallbacks...) {
		if k.ID == p.Key && (k.NotAfter.IsZero() || now.Before(k.NotAfter)) {
			key = k.Value
			break
		}
	}
	if key == nil {
		return nil, ErrBadSignature
	}
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(payload))
	if !hmac.Equal(mac.Sum(nil), supplied) {
		return nil, ErrBadSignature
	}
	issued := time.Unix(p.Issued, 0)
	if issued.After(now.Add(30 * time.Second)) {
		return nil, ErrBadSignature
	}
	if now.Sub(issued) > maxAge {
		return nil, ErrExpiredSignature
	}
	return append([]byte(nil), p.Value...), nil
}
func RandomToken(bytes int) (string, error) {
	if bytes < 16 || bytes > 1024 {
		return "", errors.New("invalid random token size")
	}
	value := make([]byte, bytes)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}
