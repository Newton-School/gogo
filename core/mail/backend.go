package mail

import (
	"context"
	"errors"
	"fmt"
)

// Backend submits one message without implicit retries. Implementations must
// distinguish provider acceptance from uncertain acceptance and simulation.
type Backend interface {
	Send(context.Context, Message) (Receipt, error)
}

type RecipientState string

const (
	Accepted     RecipientState = "accepted"
	Rejected     RecipientState = "rejected"
	Unknown      RecipientState = "unknown"
	NotAttempted RecipientState = "not_attempted"
	Simulated    RecipientState = "simulated"
)

type RecipientReceipt struct {
	Address string
	State   RecipientState
	Code    int // Numeric SMTP reply only; provider text is never retained.
}

func (RecipientReceipt) String() string                      { return "mail.RecipientReceipt{address:redacted}" }
func (r RecipientReceipt) GoString() string                  { return r.String() }
func (r RecipientReceipt) Format(state fmt.State, verb rune) { _, _ = fmt.Fprint(state, r.String()) }

type Receipt struct {
	Backend, MessageID string
	Recipients         []RecipientReceipt
	Simulated          bool
}

func (r Receipt) String() string {
	return fmt.Sprintf("mail.Receipt{backend:%s,recipients:%d,simulated:%t}", r.Backend, len(r.Recipients), r.Simulated)
}
func (r Receipt) GoString() string                  { return r.String() }
func (r Receipt) Format(state fmt.State, verb rune) { _, _ = fmt.Fprint(state, r.String()) }

// AllAccepted means every envelope recipient was accepted by the provider,
// not that any message reached an inbox. A simulated receipt is never accepted.
func (r Receipt) AllAccepted() bool {
	if r.Simulated || len(r.Recipients) == 0 {
		return false
	}
	for _, recipient := range r.Recipients {
		if recipient.State != Accepted {
			return false
		}
	}
	return true
}

func receiptFor(backend string, p Prepared) Receipt {
	r := Receipt{Backend: backend, MessageID: p.messageID}
	for _, address := range p.recipients {
		r.Recipients = append(r.Recipients, RecipientReceipt{Address: address, State: NotAttempted})
	}
	return r
}

var (
	ErrClosed    = errors.New("mail: backend closed")
	ErrTransport = errors.New("mail: transport failure")
	ErrRejected  = errors.New("mail: recipient rejected")
	ErrUnknown   = errors.New("mail: acceptance unknown; reconcile before retrying")
)

// SendError is intentionally free of provider text, credentials, addresses and
// message content. Receipt is the authority for individual recipient outcomes.
type SendError struct {
	Kind         error
	Stage        string
	Code         int
	contextError error
}

func (e *SendError) Error() string {
	return fmt.Sprintf("mail: %s failure (SMTP code %d)", e.Stage, e.Code)
}
func (e *SendError) Unwrap() error { return e.Kind }
func (e *SendError) Is(target error) bool {
	return errors.Is(e.Kind, target) || e.contextError != nil && errors.Is(e.contextError, target)
}

type Delivery struct {
	Receipt Receipt
	Err     error
}

// SendMail submits a declaration through an explicitly selected backend.
func SendMail(ctx context.Context, backend Backend, message Message) (Receipt, error) {
	if backend == nil || ctx == nil {
		return Receipt{}, ErrValidation
	}
	return backend.Send(ctx, message)
}

// SendMassMail returns ordered results and never retries a failed or uncertain
// message. Cancellation leaves remaining messages unsubmitted.
func SendMassMail(ctx context.Context, backend Backend, messages []Message) []Delivery {
	out := make([]Delivery, len(messages))
	for i, message := range messages {
		if ctx == nil || backend == nil {
			out[i].Err = ErrValidation
			continue
		}
		if err := ctx.Err(); err != nil {
			out[i].Err = err
			continue
		}
		out[i].Receipt, out[i].Err = backend.Send(ctx, message)
	}
	return out
}
