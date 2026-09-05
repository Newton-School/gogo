package async

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"
	"time"
)

// WorkerCommand is a closed set, not a remote function name or executable.
type WorkerCommand string

const ShutdownWorker WorkerCommand = "shutdown"

const (
	MaxWorkerControlLifetime  = 5 * time.Minute
	MaxRetainedWorkerControls = 128
)

// WorkerControlRequest binds a request ID to one exact runner and queue set.
// A multi-worker operation uses the same ID in separate worker partitions; it
// is not a distributed transaction. Reuse the exact request after an unknown
// submission outcome. Never resolve its InstanceID again when retrying.
// Requests contain no serialized code, credentials or application arguments.
type WorkerControlRequest struct {
	ID         string        `json:"id"`
	WorkerID   string        `json:"worker_id"`
	InstanceID string        `json:"instance_id"`
	Command    WorkerCommand `json:"command"`
	Queues     []string      `json:"queues"`
	ExpiresAt  time.Time     `json:"expires_at"`
}

type WorkerControlOutcome string

const (
	// ControlAccepted acknowledges the request to stop reserving and drain.
	// It does not prove a runner exited, a handler stopped, or an external
	// effect was undone. A crash can occur between this reply and the action.
	ControlAccepted WorkerControlOutcome = "accepted"
	// ControlQueueChanged rejects a stale/forged target's queue declaration.
	ControlQueueChanged WorkerControlOutcome = "rejected_queue_changed"
)

type WorkerControlReply struct {
	RequestID  string               `json:"request_id"`
	WorkerID   string               `json:"worker_id"`
	InstanceID string               `json:"instance_id"`
	Command    WorkerCommand        `json:"command"`
	Outcome    WorkerControlOutcome `json:"outcome"`
	At         time.Time            `json:"at"`
}

// WorkerControlStore is a trusted adapter port, not an authorization boundary.
// Application callers use Control; backend credentials must restrict direct
// writes. Submission requires an online exact instance and a server-clock
// expiry in (now, now+MaxWorkerControlLifetime]. Same-ID exact retries return
// the original acceptance; changed bodies conflict, even after a reply.
//
// Receive/Reply require the secret live presence lease, the original instance,
// and an unexpired request for first acceptance. Receive is non-destructive;
// accepted requests can repeat until expiry so an unknown reply-write response
// does not silently discard the lifecycle action. Rejected requests stop delivery.
// Reply persists one immutable outcome before the runner acts. No store or
// transport claims atomicity between the reply and a process lifecycle effect.
//
// Requests/replies are retained for at most 24h after submission, with at most
// MaxRetainedWorkerControls retained entries per worker. Full storage returns
// ErrConflict, never evicts active control records. Expiry prevents execution;
// missing reply is ErrNotFound and never evidence of successful shutdown.
// All implementations must honor context cancellation and bounded payloads.
type WorkerControlStore interface {
	WorkerPresenceStore
	SubmitWorkerControl(context.Context, WorkerControlRequest) error
	ReceiveWorkerControls(context.Context, WorkerLease, int) ([]WorkerControlRequest, error)
	ReplyWorkerControl(context.Context, WorkerLease, WorkerControlRequest, WorkerControlOutcome) (WorkerControlReply, error)
	LookupWorkerControl(context.Context, WorkerControlRequest) (WorkerControlReply, error)
}

func ValidateWorkerControlRequest(request WorkerControlRequest) error {
	if !idPattern.MatchString(request.ID) || !ValidWorkerID(request.WorkerID) || !idPattern.MatchString(request.InstanceID) || request.Command != ShutdownWorker || request.ExpiresAt.IsZero() || request.ExpiresAt.UnixMilli() <= 0 || len(request.Queues) < 1 || len(request.Queues) > 64 {
		return ErrInvalid
	}
	seen := map[string]bool{}
	for _, queue := range request.Queues {
		if !namePattern.MatchString(queue) || seen[queue] {
			return ErrInvalid
		}
		seen[queue] = true
	}
	data, err := json.Marshal(request)
	if err != nil || len(data) > 16*1024 {
		return ErrInvalid
	}
	return nil
}

func ValidateWorkerControlReceiver(lease WorkerLease, limit int) error {
	if !ValidWorkerID(lease.WorkerID) || !idPattern.MatchString(lease.Token) || limit < 1 || limit > 16 {
		return ErrInvalid
	}
	return nil
}

// WorkerControlDigest is the immutable identity binding shared by adapters.
// Only metadata is hashed; this is not a signature or an authorization grant.
func WorkerControlDigest(request WorkerControlRequest) (string, error) {
	if err := ValidateWorkerControlRequest(request); err != nil {
		return "", err
	}
	data, err := json.Marshal(request)
	if err != nil {
		return "", ErrInvalid
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:]), nil
}

func ValidateWorkerControlReply(request WorkerControlRequest, reply WorkerControlReply) error {
	if ValidateWorkerControlRequest(request) != nil || reply.RequestID != request.ID || reply.WorkerID != request.WorkerID || reply.InstanceID != request.InstanceID || reply.Command != request.Command || reply.At.IsZero() || reply.At.UnixMilli() <= 0 || reply.At.After(request.ExpiresAt) || reply.Outcome != ControlAccepted && reply.Outcome != ControlQueueChanged {
		return ErrInvalid
	}
	return nil
}

func sameWorkerQueues(left, right []string) bool {
	left, right = slices.Clone(left), slices.Clone(right)
	slices.Sort(left)
	slices.Sort(right)
	return slices.Equal(left, right)
}
