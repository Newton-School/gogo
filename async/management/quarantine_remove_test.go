package management_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/Newton-School/gogo/async"
	commands "github.com/Newton-School/gogo/async/management"
	fakes "github.com/Newton-School/gogo/async/testing"
	core "github.com/Newton-School/gogo/core/management"
)

func TestQuarantineRemovalCommandRequiresCompleteIdentityAndDistinctGrant(t *testing.T) {
	isolateCommandEnvironment(t)
	ctx := context.Background()
	registry := async.NewRegistry()
	task, err := async.Register(registry, "test.remove_cli", 1, func(_ context.Context, _ async.TaskContext, value string) (string, error) { return value, nil }, async.TaskOptions{})
	if err != nil {
		t.Fatal(err)
	}
	backend := fakes.NewMemory()
	deny := true
	client, err := async.NewClient(async.ClientConfig{Registry: registry, Broker: backend, Results: backend, Authorize: func(_ context.Context, action, queue, id string) error {
		if action == "remove_quarantine" {
			if queue != "default" || id == "" {
				t.Fatal("removal grant not scoped to full identity")
			}
			if deny {
				return async.ErrDenied
			}
		}
		return nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := task.Delay(ctx, client, "synthetic-private-argument"); err != nil {
		t.Fatal(err)
	}
	delivery, err := backend.Consume(ctx, async.ConsumeOptions{Queues: []string{"default"}, Consumer: "worker"})
	if err != nil {
		t.Fatal(err)
	}
	if err := backend.Reject(ctx, delivery, "rejected", false); err != nil {
		t.Fatal(err)
	}
	page, err := (async.Control{Client: client}).InspectQuarantine(ctx, "default", "", 1)
	if err != nil || len(page.Entries) != 1 {
		t.Fatal(page, err)
	}
	encoded, err := json.Marshal(page.Entries[0])
	if err != nil {
		t.Fatal(err)
	}
	factories := 0
	project := core.Project{Root: t.TempDir(), Commands: commands.Commands(commands.Factories{Client: func(context.Context, *core.Invocation) (*async.Client, error) { factories++; return client, nil }})}
	var output bytes.Buffer
	options := core.Options{Stdout: &output, Stderr: &output}
	priority := `"Priority":` + strconv.Itoa(page.Entries[0].Record.Priority)
	invalid := []string{
		"*", `{}`, `{"Record":{"Queue":"default"}}`, string(encoded) + ` {}`,
		strings.Replace(string(encoded), `"Record":`, `"Secret":"private","Record":`, 1),
		strings.Replace(string(encoded), ","+priority, "", 1),
		strings.Replace(string(encoded), priority, `"Priority":null`, 1),
		strings.Repeat("a", (16<<10)+1),
	}
	for _, key := range []string{"Cursor", "Record", "ID", "Queue", "SourceReceipt", "Reason", "Digest", "FirstSeen", "Priority"} {
		needle := `"` + key + `":`
		invalid = append(invalid,
			strings.Replace(string(encoded), needle, needle+`"replaced",`+needle, 1),
			strings.Replace(string(encoded), needle, `"`+strings.ToLower(key)+`":"replaced",`+needle, 1),
			strings.Replace(string(encoded), needle, `"`+strings.ToLower(key)+`":`, 1),
		)
	}
	invalid = append(invalid, strings.Replace(string(encoded), `"ID":`, `"\u0049D":"replaced","ID":`, 1))
	for _, payload := range invalid {
		before := factories
		if err := core.Call(ctx, project, []string{"queues", "quarantine-remove", payload}, options); err == nil || factories != before {
			t.Fatal("invalid removal input opened application resources", err)
		}
	}
	args := []string{"queues", "quarantine-remove", string(encoded)}
	output.Reset()
	if err := core.Call(ctx, project, args, options); !errors.Is(err, async.ErrDenied) || !strings.Contains(output.String(), `"not_attempted"`) {
		t.Fatal("inspection grant silently allowed removal", output.String(), err)
	}
	deny = false
	for _, want := range []string{"removed", "already_absent"} {
		output.Reset()
		if err := core.Call(ctx, project, args, options); err != nil || !strings.Contains(output.String(), `"`+want+`"`) || strings.Contains(output.String(), "synthetic-private-argument") {
			t.Fatal("wrong removal receipt", output.String(), err)
		}
	}
}
