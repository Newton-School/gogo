package docker_test

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

func setup(t *testing.T, directory string, success bool) []byte {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the Docker entrypoint is a POSIX shell script; verify inside its Linux image")
	}
	output, err := exec.Command("sh", "setup.sh", directory).CombinedOutput()
	if (err == nil) != success {
		t.Fatalf("setup success=%v: %v: %s", success, err, output)
	}
	return output
}

func TestCredentialsArePrivateRandomAndStable(t *testing.T) {
	directory := t.TempDir()
	output := setup(t, directory, true)
	values := map[string][]byte{}
	for _, name := range []string{"database-password", "signing-key", "admin-password"} {
		path := filepath.Join(directory, name)
		value, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !regexp.MustCompile(`^[0-9a-f]{64}$`).Match(value) {
			t.Fatal("invalid random credential")
		}
		if bytes.Contains(output, value) {
			t.Fatal("credential leaked into setup output")
		}
		for _, other := range values {
			if bytes.Equal(value, other) {
				t.Fatal("roles share credentials")
			}
		}
		values[name] = value
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != 0600 {
			t.Fatal("credential is not private")
		}
	}
	setup(t, directory, true)
	for name, before := range values {
		after, err := os.ReadFile(filepath.Join(directory, name))
		if err != nil || !bytes.Equal(before, after) {
			t.Fatal("restart changed a credential")
		}
	}
	other := t.TempDir()
	setup(t, other, true)
	value, err := os.ReadFile(filepath.Join(other, "signing-key"))
	if err != nil || bytes.Equal(values["signing-key"], value) {
		t.Fatal("independent installs share signing keys")
	}
}

func TestSetupResumesOnlyBeforeFirstCompletion(t *testing.T) {
	directory := t.TempDir()
	value := bytes.Repeat([]byte("a"), 64)
	path := filepath.Join(directory, "database-password")
	if err := os.WriteFile(path, value, 0600); err != nil {
		t.Fatal(err)
	}
	setup(t, directory, true)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	setup(t, directory, false)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("replaced a missing credential after completion")
	}
}

func TestSetupRejectsInvalidOrSymlinkedCredentials(t *testing.T) {
	for _, invalid := range []string{"", "partial", string(bytes.Repeat([]byte("z"), 64))} {
		directory := t.TempDir()
		path := filepath.Join(directory, "database-password")
		if err := os.WriteFile(path, []byte(invalid), 0600); err != nil {
			t.Fatal(err)
		}
		setup(t, directory, false)
		after, err := os.ReadFile(path)
		if err != nil || string(after) != invalid {
			t.Fatal("invalid secret was overwritten")
		}
	}
	directory := t.TempDir()
	target := filepath.Join(t.TempDir(), "target")
	if err := os.WriteFile(target, bytes.Repeat([]byte("a"), 64), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(directory, "database-password")); err != nil {
		t.Fatal(err)
	}
	setup(t, directory, false)
}

func TestCredentialDisplayFailsClosed(t *testing.T) {
	directory := t.TempDir()
	setup(t, directory, true)
	source, err := os.ReadFile("entrypoint.sh")
	if err != nil {
		t.Fatal(err)
	}
	// Substitute only the container mount, keeping the actual entrypoint logic.
	if strings.Count(string(source), "/run/showcase/") != 1 {
		t.Fatal("unexpected credential mount declarations")
	}
	script := strings.ReplaceAll(string(source), "/run/showcase/", "${SHOWCASE_TEST_CREDENTIALS}/")
	display := func(success bool) []byte {
		t.Helper()
		command := exec.Command("sh", "-c", script, "entrypoint", "credentials")
		command.Env = append(os.Environ(), "SHOWCASE_TEST_CREDENTIALS="+directory)
		output, err := command.CombinedOutput()
		if (err == nil) != success {
			t.Fatalf("credentials success=%v; error=%v", success, err)
		}
		if !success && bytes.Contains(output, []byte("Initial Admin password:")) {
			t.Fatal("printed bogus credentials after a read failure")
		}
		return output
	}
	output := display(true)
	for _, name := range []string{"database-password", "signing-key"} {
		value, err := os.ReadFile(filepath.Join(directory, name))
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(output, value) {
			t.Fatal("Admin credential command exposed an unrelated secret")
		}
	}
	path := filepath.Join(directory, "admin-password")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	display(false)
	if err := os.WriteFile(path, []byte("corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	display(false)
}
