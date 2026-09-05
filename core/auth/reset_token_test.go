package auth

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestResetTokensUseCanonicalPurposeBoundSecrets(t *testing.T) {
	seen := map[string]bool{}
	for range 100 {
		id, bearer, digest, err := newResetToken()
		if err != nil {
			t.Fatal(err)
		}
		parsed, computed, err := parseResetToken(bearer)
		if err != nil || parsed != id || !resetDigestEqual(computed, digest) || seen[bearer] || strings.Contains(digest, bearer) {
			t.Fatal("invalid generated token")
		}
		seen[bearer] = true
		another, _, _, err := newResetToken()
		if err != nil {
			t.Fatal(err)
		}
		_, different, err := parseResetToken(another + bearer[36:])
		if err != nil || resetDigestEqual(different, digest) {
			t.Fatal("digest not bound to token identity")
		}
		for _, invalid := range []string{"", bearer + "=", bearer[:79], strings.ToUpper(id) + bearer[36:], " " + bearer, bearer[:37] + strings.Repeat("!", 43)} {
			if invalid == bearer {
				continue
			}
			if _, _, err := parseResetToken(invalid); !errors.Is(err, ErrResetToken) {
				t.Fatal("noncanonical token accepted")
			}
		}
	}
	if resetDigestEqual("", "") || resetDigestEqual("abc", "abc") {
		t.Fatal("invalid digest accepted")
	}
}

func TestPasswordResetRecordFormattingAndSeparateMigration(t *testing.T) {
	record := PasswordResetRecord{ID: "private-record-identity", SecretDigest: "private-digest", UserID: "private-user"}
	for _, verb := range []string{"%v", "%+v", "%#v", "%d", "%s", "%x", "%q", "%f"} {
		for _, value := range []any{record, &record} {
			if strings.Contains(fmt.Sprintf(verb, value), "private-") {
				t.Fatal("record rendered", verb)
			}
		}
	}
	if len(Migrations()) != 1 || Migrations()[0].Name != "0001_initial" || len(Migrations()[0].Operations) != 6 {
		t.Fatal("initial account migration changed")
	}
	reset := PasswordResetMigrations()
	if len(reset) != 1 || reset[0].Name != "0002_password_resets" || len(reset[0].Operations) != 1 || reset[0].Dependencies[0] != "gogo_auth.0001_initial" {
		t.Fatal("reset migration graph")
	}
	if err := (&PasswordResetRecord{}).Schema().Validate(); err != nil {
		t.Fatal(err)
	}
}
