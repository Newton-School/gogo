package examples_test

import (
	"crypto/rand"
	"fmt"

	"github.com/Newton-School/gogo/core/auth"
)

func Example_password() {
	// Test-only input. Never print or persist the submitted plaintext password.
	password := rand.Text()
	// docs:begin password-hash
	encoded, err := auth.HashPassword(password)
	if err != nil {
		panic(err)
	}
	// docs:end password-hash
	// docs:begin password-verify
	valid, _, err := auth.VerifyPassword(password, encoded)
	if err != nil {
		panic(err)
	}
	fmt.Println("correct password:", valid)
	valid, _, err = auth.VerifyPassword(password+"wrong", encoded)
	if err != nil {
		panic(err)
	}
	fmt.Println("wrong password:", valid)
	// docs:end password-verify
	// Output:
	// correct password: true
	// wrong password: false
}
