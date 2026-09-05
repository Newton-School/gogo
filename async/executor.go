package async

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os/exec"
	"time"
)

type Execution struct {
	Envelope    Envelope    `json:"envelope"`
	TaskContext TaskContext `json:"task_context"`
}
type Executor interface {
	Execute(context.Context, Execution) (json.RawMessage, error)
}

// ProcessExecutor launches only an application-configured command, never a
// command from a broker message. Context termination kills the isolated child.
type ProcessExecutor struct {
	Command        []string
	Environment    []string
	MaxOutputBytes int
}
type childResponse struct {
	Output    json.RawMessage `json:"output,omitempty"`
	Failure   *Failure        `json:"failure,omitempty"`
	Retry     bool            `json:"retry"`
	Countdown *time.Duration  `json:"countdown,omitempty"`
	ETA       time.Time       `json:"eta,omitempty"`
}
type boundedBuffer struct {
	bytes.Buffer
	limit int
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	if len(p) > b.limit-b.Len() {
		return 0, ErrInvalid
	}
	return b.Buffer.Write(p)
}
func (p *ProcessExecutor) Execute(ctx context.Context, e Execution) (json.RawMessage, error) {
	if len(p.Command) == 0 || p.Command[0] == "" {
		return nil, ErrInvalid
	}
	payload, err := json.Marshal(e)
	if err != nil || len(payload) > 2*MaxPayloadBytes {
		return nil, ErrInvalid
	}
	maxBytes := p.MaxOutputBytes
	if maxBytes == 0 {
		maxBytes = MaxPayloadBytes
	}
	if maxBytes < 1 || maxBytes > MaxPayloadBytes {
		return nil, ErrInvalid
	}
	cmd := exec.CommandContext(ctx, p.Command[0], p.Command[1:]...)
	if p.Environment != nil {
		cmd.Env = append([]string(nil), p.Environment...)
	}
	cmd.Stdin = bytes.NewReader(payload)
	stdout := &boundedBuffer{limit: maxBytes}
	cmd.Stdout = stdout
	cmd.Stderr = io.Discard
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, Failure{Code: "CHILD_FAILED", Message: "Task subprocess exited unsuccessfully"}
	}
	var response childResponse
	if err := decodeJSON(stdout.Bytes(), &response); err != nil {
		return nil, ErrInvalid
	}
	if response.Retry {
		return nil, &RetryRequest{Cause: response.Failure, Countdown: response.Countdown, ETA: response.ETA}
	}
	if response.Failure != nil {
		return nil, *response.Failure
	}
	if !json.Valid(response.Output) {
		return nil, ErrInvalid
	}
	return response.Output, nil
}

// ServeChild implements the application side of the subprocess protocol. Wire
// input selects registered task code only; stdout is reserved for this response.
func (r *Registry) ServeChild(ctx context.Context, input io.Reader, output io.Writer) error {
	if err := r.Freeze(true); err != nil {
		return err
	}
	raw, err := io.ReadAll(io.LimitReader(input, 2*MaxPayloadBytes+1))
	if err != nil || len(raw) > 2*MaxPayloadBytes {
		return ErrInvalid
	}
	var execution Execution
	if err := decodeJSON(raw, &execution); err != nil {
		return ErrInvalid
	}
	if err := execution.Envelope.Validate(); err != nil {
		return err
	}
	d, err := r.lookup(execution.Envelope.Task, execution.Envelope.Version)
	if err != nil {
		return err
	}
	if err := d.validate(execution.Envelope.Args); err != nil {
		return err
	}
	tc := execution.TaskContext
	e := execution.Envelope
	if tc.ID != e.ID || tc.Scope != e.Scope || tc.Principal != e.Principal || tc.Retries != e.Retries {
		return ErrInvalid
	}
	response := func() (response childResponse) {
		defer func() {
			if recover() != nil {
				response = childResponse{Failure: &Failure{Code: "PANIC", Message: "Task subprocess panicked"}}
			}
		}()
		if d.options.Authorize != nil {
			if err := d.options.Authorize(ctx, tc); err != nil {
				return childResponse{Failure: &Failure{Code: "DENIED", Message: "Task authorization denied"}}
			}
		}
		childCtx := context.WithValue(ctx, workerContextKey{}, true)
		if d.options.SoftLimit > 0 {
			var cancel context.CancelFunc
			childCtx, cancel = context.WithTimeout(childCtx, d.options.SoftLimit)
			defer cancel()
		}
		value, err := d.call(childCtx, tc, e.Args)
		if childCtx.Err() != nil && err == nil {
			err = childCtx.Err()
		}
		if err == nil {
			return childResponse{Output: value}
		}
		failure := &Failure{Code: "FAILED", Message: "Task subprocess handler failed"}
		var retry *RetryRequest
		if errors.As(err, &retry) {
			return childResponse{Failure: failure, Retry: true, Countdown: retry.Countdown, ETA: retry.ETA}
		}
		if d.options.Retry.AutoRetryFor != nil && d.options.Retry.AutoRetryFor(err) {
			return childResponse{Failure: failure, Retry: true}
		}
		return childResponse{Failure: failure}
	}()
	return json.NewEncoder(output).Encode(response)
}
