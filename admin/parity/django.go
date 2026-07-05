package parity

import (
	"os"
	"os/exec"
	"testing"
)

// RunOptionalDjangoReference validates the optional local Django comparator setup.
func RunOptionalDjangoReference(t *testing.T) {
	t.Helper()
	if os.Getenv("GOGO_ADMIN_PARITY_DJANGO") != "1" {
		t.Skip("set GOGO_ADMIN_PARITY_DJANGO=1 to enable the optional Django reference check")
	}
	command := exec.Command("python3", "-c", "import django; print(django.get_version())")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("optional Django reference unavailable: %v\n%s", err, string(output))
	}
}
