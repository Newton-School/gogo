package examples_test

import (
	"crypto/rand"
	"fmt"

	"github.com/Newton-School/gogo/core/auth"
)

func Example_password() {
	// Test-only input. Never print or persist the submitted plaintext password.
	password := rand.Text()
	encoded, err := auth.HashPassword(password)
	if err != nil {
		panic(err)
	}
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
	// Output:
	// correct password: true
	// wrong password: false
}
