//go:build !darwin && !linux

package async_test

import (
	"context"
	"errors"
	"testing"

	"github.com/Newton-School/gogo/async"
)

func TestProcessExecutorUnsupportedPlatformFailsBeforeLaunch(t *testing.T) {
	p := &async.ProcessExecutor{Command: []string{"must-not-run"}}
	if err := p.Validate(); !errors.Is(err, async.ErrUnavailable) {
		t.Fatal(err)
	}
	if _, err := p.Execute(context.Background(), async.Execution{}); !errors.Is(err, async.ErrUnavailable) {
		t.Fatal(err)
	}
}
