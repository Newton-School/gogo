package management

import (
	"os"
	"strings"
	"testing"
)

// Command fixtures construct their own project settings. Ambient integration
// flags and developer settings must not override them. Setenv records cleanup
// and disallows parallel use before Unsetenv removes the key entirely: the
// strict configuration schema also rejects unknown keys with empty values.
func isolateCommandEnvironment(t *testing.T) {
	t.Helper()
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(key, "GOGO_") {
			t.Setenv(key, "")
			if err := os.Unsetenv(key); err != nil {
				t.Fatal(err)
			}
		}
	}
}
