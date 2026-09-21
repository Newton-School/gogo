package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strings"
)

var ErrResetToken = errors.New("invalid or expired password reset token")

// A reset bearer has a public UUID and 256 random secret bits. The digest binds
// both its purpose and public identity, so copying a digest to another token
// record cannot make the original secret redeemable under the new identity.
func newResetToken() (id, bearer, digest string, err error) {
	id, err = newAccountID()
	if err != nil {
		return "", "", "", err
	}
	var secret [32]byte
	if _, err = rand.Read(secret[:]); err != nil {
		return "", "", "", err
	}
	bearer = id + "." + base64.RawURLEncoding.EncodeToString(secret[:])
	return id, bearer, resetDigest(id, secret[:]), nil
}

func parseResetToken(bearer string) (id, digest string, err error) {
	if len(bearer) != 80 || bearer[36] != '.' {
		return "", "", ErrResetToken
	}
	id = bearer[:36]
	if !validUUID(id) || id != strings.ToLower(id) {
		return "", "", ErrResetToken
	}
	secret, err := base64.RawURLEncoding.Strict().DecodeString(bearer[37:])
	if err != nil || len(secret) != 32 {
		return "", "", ErrResetToken
	}
	return id, resetDigest(id, secret), nil
}

func resetDigest(id string, secret []byte) string {
	hash := sha256.New()
	_, _ = hash.Write([]byte("gogo.auth.password-reset.v1\x00" + id + "\x00"))
	_, _ = hash.Write(secret)
	return hex.EncodeToString(hash.Sum(nil))
}

func resetDigestEqual(a, b string) bool {
	return len(a) == 64 && len(b) == 64 && subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}
