package async

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"time"
)

const ProtocolVersion = 1
const MaxPayloadBytes = 256 << 10

var (
	ErrInvalid       = errors.New("async: invalid input")
	ErrUnknownTask   = errors.New("async: unknown task or version")
	ErrFrozen        = errors.New("async: registry frozen")
	ErrNotFound      = errors.New("async: result not found")
	ErrBusy          = errors.New("async: lease already held")
	ErrLeaseLost     = errors.New("async: execution lease lost")
	ErrConflict      = errors.New("async: identity or revision conflict")
	ErrResultExpired = errors.New("async: result payload expired")
	ErrPinned        = errors.New("async: active coordination prevents deletion")
	ErrWorkerJoin    = errors.New("async: synchronous result waits inside workers are forbidden")
	ErrDenied        = errors.New("async: operation denied")
	ErrUnavailable   = errors.New("async: backend unavailable")
)

var namePattern = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_.-]{0,191}$`)
var idPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

type State string

const (
	Unknown   State = "UNKNOWN"
	Scheduled State = "SCHEDULED"
	Queued    State = "QUEUED"
	Running   State = "RUNNING"
	RetryWait State = "RETRY_WAIT"
	Succeeded State = "SUCCEEDED"
	Failed    State = "FAILED"
	Revoked   State = "REVOKED"
	Expired   State = "EXPIRED"
)

func (s State) Terminal() bool { return s == Succeeded || s == Failed || s == Revoked || s == Expired }

// Envelope is the versioned JSON wire format. IDs never contain secrets.
// Retries counts explicit application retries, not broker delivery attempts.
type Envelope struct {
	ProtocolVersion int               `json:"protocol_version"`
	ID              string            `json:"task_id"`
	Task            string            `json:"task_name"`
	Version         int               `json:"task_version"`
	Args            json.RawMessage   `json:"args"`
	CreatedAt       time.Time         `json:"created_at"`
	Queue           string            `json:"queue"`
	Retries         int               `json:"retries"`
	MaxRetries      *int              `json:"max_retries"`
	ETA             time.Time         `json:"eta,omitempty"`
	ExpiresAt       time.Time         `json:"expires_at,omitempty"`
	Priority        int               `json:"priority"`
	WorkflowID      string            `json:"workflow_id,omitempty"`
	ParentID        string            `json:"parent_id,omitempty"`
	RootID          string            `json:"root_id,omitempty"`
	Scope           string            `json:"scope,omitempty"`
	Principal       string            `json:"principal,omitempty"`
	IdempotencyKey  string            `json:"idempotency_key,omitempty"`
	CorrelationID   string            `json:"correlation_id,omitempty"`
	Headers         map[string]string `json:"headers,omitempty"`
	TraceContext    map[string]string `json:"trace_context,omitempty"`
	Stamps          map[string]string `json:"stamps,omitempty"`
	Callbacks       []Signature       `json:"callbacks,omitempty"`
	Errbacks        []Signature       `json:"errbacks,omitempty"`
}

func (e Envelope) Validate() error {
	if e.ProtocolVersion != ProtocolVersion || !idPattern.MatchString(e.ID) || !namePattern.MatchString(e.Task) || e.Version < 1 || !namePattern.MatchString(e.Queue) || e.CreatedAt.IsZero() || e.Retries < 0 || e.Priority < 0 || e.Priority > 9 || !json.Valid(e.Args) {
		return ErrInvalid
	}
	if e.MaxRetries != nil && (*e.MaxRetries < 0 || e.Retries > *e.MaxRetries) {
		return ErrInvalid
	}
	for _, id := range []string{e.WorkflowID, e.ParentID, e.RootID} {
		if id != "" && !idPattern.MatchString(id) {
			return ErrInvalid
		}
	}
	if len(e.Scope) > 256 || len(e.Principal) > 256 || len(e.IdempotencyKey) > 256 || len(e.CorrelationID) > 256 {
		return ErrInvalid
	}
	for _, headers := range []map[string]string{e.Headers, e.TraceContext, e.Stamps} {
		if len(headers) > 32 {
			return ErrInvalid
		}
		for k, v := range headers {
			if !namePattern.MatchString(k) || len(v) > 1024 {
				return ErrInvalid
			}
		}
	}
	b, err := json.Marshal(e)
	if err != nil || len(b) > MaxPayloadBytes {
		return ErrInvalid
	}
	return nil
}

func DecodeEnvelope(b []byte) (Envelope, error) {
	var e Envelope
	if len(b) > MaxPayloadBytes {
		return e, ErrInvalid
	}
	if err := decodeJSON(b, &e); err != nil {
		return e, ErrInvalid
	}
	return e, e.Validate()
}

// Digest binds stable task identity to immutable inputs. ETA and retry count may
// change only through a fenced retry transition and are not part of the digest.
func (e Envelope) Digest() string {
	e.ETA = time.Time{}
	e.Retries = 0
	b, _ := json.Marshal(e)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func NewID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return formatID(b[:]), nil
}

func StableID(parent, label string) string {
	b := sha256.Sum256([]byte(parent + "\x00" + label))
	b[6] = (b[6] & 0x0f) | 0x50
	b[8] = (b[8] & 0x3f) | 0x80
	return formatID(b[:16])
}

func formatID(b []byte) string {
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[:4], b[4:6], b[6:8], b[8:10], b[10:])
}

func decodeJSON(b []byte, v any) error {
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		return err
	}
	var extra any
	if err := d.Decode(&extra); !errors.Is(err, io.EOF) {
		return ErrInvalid
	}
	return nil
}

func cloneJSON[T any](v T) T {
	b, _ := json.Marshal(v)
	var out T
	_ = json.Unmarshal(b, &out)
	return out
}

// Failure contains a safe public code, never the handler's potentially sensitive
// raw error text or a stack trace. Local diagnostic hooks may inspect the error.
type Failure struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (f Failure) Error() string { return f.Code + ": " + f.Message }
