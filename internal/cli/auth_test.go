package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Newton-School/gogo/auth"
)

func TestCreateSuperuserNonInteractiveCreatesUser(t *testing.T) {
	store, _ := auth.NewMemoryUserStore()
	command := NewCreateSuperuserCommand(store)
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	err := command.(interface {
		runWithIO(context.Context, []string, io.Writer, io.Writer) error
	}).runWithIO(context.Background(), []string{
		"--username", "Admin",
		"--email", "Admin@Example.COM",
		"--password", "CorrectHorseBatteryStaple42",
		"--database", "default",
		"--noinput",
	}, &stdout, &stderr)
	if err != nil {
		t.Fatalf("createsuperuser error = %v", err)
	}

	user, ok, err := store.FindByUsername(context.Background(), "admin")
	if err != nil || !ok {
		t.Fatalf("FindByUsername(admin) = %#v, %v, %v", user, ok, err)
	}
	if !user.IsSuperuser || !user.IsStaff || !user.IsActive || user.Email != "admin@example.com" {
		t.Fatalf("created user = %#v", user)
	}
	if ok, _ := auth.CheckPassword("CorrectHorseBatteryStaple42", user.Password); !ok {
		t.Fatalf("created password does not verify")
	}
	if !strings.Contains(stdout.String(), "created superuser admin on database default") {
		t.Fatalf("stdout = %q", stdout.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestCreateSuperuserRejectsDuplicateInvalidPasswordAndUnsafePasswordFlag(t *testing.T) {
	store, _ := auth.NewMemoryUserStore()
	command := NewCreateSuperuserCommand(store)
	run := func(args ...string) error {
		return command.(interface {
			runWithIO(context.Context, []string, io.Writer, io.Writer) error
		}).runWithIO(context.Background(), args, io.Discard, io.Discard)
	}

	if err := run("--username", "admin", "--password", "CorrectHorseBatteryStaple42", "--noinput"); err != nil {
		t.Fatalf("first createsuperuser error = %v", err)
	}
	if err := run("--username", "admin", "--password", "AnotherStrongPassword42", "--noinput"); !errors.Is(err, auth.ErrDuplicateUser) {
		t.Fatalf("duplicate error = %v, want ErrDuplicateUser", err)
	}
	if err := run("--username", "weak", "--password", "password", "--noinput"); !errors.Is(err, auth.ErrPasswordValidation) {
		t.Fatalf("invalid password error = %v, want ErrPasswordValidation", err)
	}
	if err := run("--username", "unsafe", "--password", "CorrectHorseBatteryStaple42"); !errors.Is(err, ErrCommandFailed) {
		t.Fatalf("unsafe password flag error = %v, want ErrCommandFailed", err)
	}
}

func TestChangePasswordUpdatesExistingUser(t *testing.T) {
	hash, err := auth.EncodePBKDF2PasswordWithIterations("old-secret", "salt", 1)
	if err != nil {
		t.Fatalf("EncodePBKDF2PasswordWithIterations() error = %v", err)
	}
	store, _ := auth.NewMemoryUserStore(auth.User{AbstractUser: auth.AbstractUser{
		AbstractBaseUser: auth.AbstractBaseUser{ID: 1, Password: hash, IsActive: true},
		Username:         "admin",
	}})
	command := NewChangePasswordCommand(store)
	var stdout bytes.Buffer

	err = command.(interface {
		runWithIO(context.Context, []string, io.Writer, io.Writer) error
	}).runWithIO(context.Background(), []string{
		"--username", "admin",
		"--password", "CorrectHorseBatteryStaple42",
		"--database", "default",
		"--noinput",
	}, &stdout, io.Discard)
	if err != nil {
		t.Fatalf("changepassword error = %v", err)
	}

	user, ok, err := store.FindByUsername(context.Background(), "admin")
	if err != nil || !ok {
		t.Fatalf("FindByUsername(admin) = %#v, %v, %v", user, ok, err)
	}
	if ok, _ := auth.CheckPassword("CorrectHorseBatteryStaple42", user.Password); !ok {
		t.Fatalf("changed password does not verify")
	}
	if !strings.Contains(stdout.String(), "changed password for admin on database default") {
		t.Fatalf("stdout = %q", stdout.String())
	}
}

func TestChangePasswordAcceptsDjangoStylePositionalUsername(t *testing.T) {
	hash, err := auth.EncodePBKDF2PasswordWithIterations("old-secret", "salt", 1)
	if err != nil {
		t.Fatalf("EncodePBKDF2PasswordWithIterations() error = %v", err)
	}
	store, _ := auth.NewMemoryUserStore(auth.User{AbstractUser: auth.AbstractUser{
		AbstractBaseUser: auth.AbstractBaseUser{ID: 1, Password: hash, IsActive: true},
		Username:         "admin",
	}})
	command := NewChangePasswordCommand(store)
	var stdout bytes.Buffer

	err = command.(interface {
		runWithIO(context.Context, []string, io.Writer, io.Writer) error
	}).runWithIO(context.Background(), []string{
		"admin",
		"--password", "CorrectHorseBatteryStaple42",
		"--noinput",
	}, &stdout, io.Discard)
	if err != nil {
		t.Fatalf("changepassword positional error = %v", err)
	}

	user, ok, err := store.FindByUsername(context.Background(), "admin")
	if err != nil || !ok {
		t.Fatalf("FindByUsername(admin) = %#v, %v, %v", user, ok, err)
	}
	if ok, _ := auth.CheckPassword("CorrectHorseBatteryStaple42", user.Password); !ok {
		t.Fatalf("changed password does not verify")
	}
	if !strings.Contains(stdout.String(), "changed password for admin on database default") {
		t.Fatalf("stdout = %q", stdout.String())
	}
}

func TestDefaultAuthStorePersistsAcrossRootExecutions(t *testing.T) {
	root := t.TempDir()
	writeTextFile(t, filepath.Join(root, "go.mod"), "module sampleproject\n\ngo 1.26.4\n")
	withAuthTestWorkingDirectory(t, root, func() {
		var createOut bytes.Buffer
		if err := NewRoot().Execute(context.Background(), []string{
			"createsuperuser",
			"--username", "admin",
			"--email", "admin@example.com",
			"--password", "CorrectHorseBatteryStaple42",
			"--noinput",
		}, &createOut, &bytes.Buffer{}); err != nil {
			t.Fatalf("createsuperuser error = %v", err)
		}

		var changeOut bytes.Buffer
		if err := NewRoot().Execute(context.Background(), []string{
			"changepassword",
			"--username", "admin",
			"--password", "CorrectHorseBatteryStaple43",
			"--noinput",
		}, &changeOut, &bytes.Buffer{}); err != nil {
			t.Fatalf("changepassword error = %v", err)
		}
		if !strings.Contains(changeOut.String(), "changed password for admin") {
			t.Fatalf("changepassword stdout = %q", changeOut.String())
		}

		data, err := os.ReadFile(filepath.Join(root, ".gogo", "auth_users.json"))
		if err != nil {
			t.Fatalf("read persisted auth store: %v", err)
		}
		for _, forbidden := range []string{"CorrectHorseBatteryStaple42", "CorrectHorseBatteryStaple43"} {
			if strings.Contains(string(data), forbidden) {
				t.Fatalf("auth store contains plaintext password %q", forbidden)
			}
		}
	})
}

func withAuthTestWorkingDirectory(t *testing.T, dir string, fn func()) {
	t.Helper()
	previous, err := os.Getwd()
	if err != nil {
		t.Fatalf("get working directory: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir %s: %v", dir, err)
	}
	defer func() {
		if err := os.Chdir(previous); err != nil {
			t.Fatalf("restore working directory: %v", err)
		}
	}()
	fn()
}
