package examples_test

import (
	"crypto/rand"
	"errors"
	"fmt"
	"time"

	"github.com/Newton-School/gogo/core/security"
)

func Example_signing() {
	// Test-only key. An application loads a stable private key from settings.
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		panic(err)
	}
	// docs:begin signing-config
	signer, err := security.NewSigner(security.SigningKey{ID: "primary", Value: key}, nil, "catalog-link")
	if err != nil {
		panic(err)
	}
	// docs:end signing-config
	// docs:begin signing-token
	token, err := signer.Sign([]byte("product:42"))
	if err != nil {
		panic(err)
	}
	// docs:end signing-token
	// docs:begin signing-verify
	value, err := signer.Verify(token, time.Minute)
	if err != nil {
		panic(err)
	}
	fmt.Println(string(value))
	_, err = signer.Verify(token+"x", time.Minute)
	fmt.Println("tampering rejected:", errors.Is(err, security.ErrBadSignature))
	// docs:end signing-verify
	// Output:
	// product:42
	// tampering rejected: true
}
