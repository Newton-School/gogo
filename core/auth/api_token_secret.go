package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

var ErrToken = fmt.Errorf("invalid API token: %w", ErrUnauthenticated)

// TokenSecret is returned only after confirmed issuance. Reveal is an explicit
// credential handoff to a trusted caller; never log it, place it in a URL, or
// send it through ordinary JSON/task serialization.
type TokenSecret struct{ value string }

func (TokenSecret) String() string                   { return "auth.TokenSecret{redacted}" }
func (s TokenSecret) Format(state fmt.State, _ rune) { _, _ = fmt.Fprint(state, s.String()) }
func (TokenSecret) MarshalJSON() ([]byte, error) {
	return nil, errors.New("auth: API token secret cannot be serialized")
}
func (s TokenSecret) Reveal() string { return s.value }

func newAPIToken() (id string, secret TokenSecret, digest string, err error) {
	id, err = newAccountID()
	if err != nil {
		return
	}
	var random [32]byte
	if _, err = rand.Read(random[:]); err != nil {
		return
	}
	secret.value = "gogo_" + id + "." + base64.RawURLEncoding.EncodeToString(random[:])
	digest = apiTokenDigest(id, random[:])
	return
}

func parseAPIToken(bearer string) (id, digest string, err error) {
	if len(bearer) != 85 || !strings.HasPrefix(bearer, "gogo_") || bearer[41] != '.' {
		return "", "", ErrToken
	}
	id = bearer[5:41]
	if !validAccountID(id) || id != strings.ToLower(id) {
		return "", "", ErrToken
	}
	secret, err := base64.RawURLEncoding.Strict().DecodeString(bearer[42:])
	if err != nil || len(secret) != 32 {
		return "", "", ErrToken
	}
	return id, apiTokenDigest(id, secret), nil
}

func apiTokenDigest(id string, secret []byte) string {
	hash := sha256.New()
	_, _ = hash.Write([]byte("gogo.auth.api-token.v1\x00" + id + "\x00"))
	_, _ = hash.Write(secret)
	return hex.EncodeToString(hash.Sum(nil))
}
