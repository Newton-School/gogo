//go:build darwin || linux

package async

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"testing"
	"time"
)

func TestIsolationExitHelper(t *testing.T) {
	if os.Getenv("GOGO_TEST_ISOLATION_EXIT") != "1" {
		return
	}
	_, _ = os.Stdout.WriteString(`{"output":42}`)
	os.Exit(0)
}

func TestIsolatedSuccessfulExitPreservesStatus(t *testing.T) {
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, "-test.run=^TestIsolationExitHelper$")
	cmd.Env = []string{"GOGO_TEST_ISOLATION_EXIT=1", "GORACE=atexit_sleep_ms=0"}
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.WaitDelay = 100 * time.Millisecond
	if err := runIsolatedProcess(cmd); err != nil {
		t.Fatal(err, out.String())
	}
}
