package mail

import (
	"context"
	"fmt"
	"io"
	"sync"
)

// Console writes validated MIME to an explicitly supplied development writer.
// It refuses Sensitive messages. Writers must provide their own bounded Write:
// an arbitrary io.Writer cannot be safely interrupted by context cancellation.
type Console struct{ *consoleState }
type consoleState struct {
	mu     sync.Mutex
	writer io.Writer
	limits Limits
}

func NewConsole(writer io.Writer, limits Limits) (*Console, error) {
	if writer == nil {
		return nil, ErrValidation
	}
	limits, err := limits.defaults()
	if err != nil {
		return nil, err
	}
	return &Console{&consoleState{writer: writer, limits: limits}}, nil
}
func (Console) String() string                      { return "mail.Console{output:redacted}" }
func (c Console) GoString() string                  { return c.String() }
func (c Console) Format(state fmt.State, verb rune) { _, _ = fmt.Fprint(state, c.String()) }
func (Console) MarshalJSON() ([]byte, error)        { return nil, ErrSensitiveOutput }

func simulation(backend string, p Prepared) Receipt {
	r := receiptFor(backend, p)
	r.Simulated = true
	for i := range r.Recipients {
		r.Recipients[i].State = Simulated
	}
	return r
}

func (c *Console) Send(ctx context.Context, message Message) (Receipt, error) {
	if message.Sensitive {
		return Receipt{}, ErrSensitiveOutput
	}
	p, err := Prepare(ctx, message, c.limits)
	if err != nil {
		return Receipt{}, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return receiptFor("console", p), err
	}
	n, err := c.writer.Write(p.data)
	if err != nil || n != len(p.data) {
		return receiptFor("console", p), &SendError{Kind: ErrTransport, Stage: "output"}
	}
	return simulation("console", p), nil
}

// Dummy validates messages but neither sends nor retains them.
type Dummy struct{ limits Limits }

func NewDummy(limits Limits) (*Dummy, error) {
	limits, err := limits.defaults()
	if err != nil {
		return nil, err
	}
	return &Dummy{limits}, nil
}
func (d *Dummy) Send(ctx context.Context, message Message) (Receipt, error) {
	p, err := Prepare(ctx, message, d.limits)
	if err != nil {
		return Receipt{}, err
	}
	return simulation("dummy", p), nil
}

// Service snapshots configured administrative recipients. Recipient helpers
// replace all caller recipient lists so notices cannot escape their configured
// audience through a leftover Cc or Bcc declaration.
type Service struct {
	backend          Backend
	from, prefix     string
	admins, managers []string
}
type ServiceConfig struct {
	Backend             Backend
	From, SubjectPrefix string
	Admins, Managers    []string
}

func NewService(config ServiceConfig) (*Service, error) {
	if config.Backend == nil || !safeHeader(config.SubjectPrefix) || len(config.SubjectPrefix) > 256 {
		return nil, ErrValidation
	}
	if _, err := address(config.From); err != nil {
		return nil, err
	}
	for _, group := range [][]string{config.Admins, config.Managers} {
		if len(group) > 1000 {
			return nil, ErrLimit
		}
		for _, recipient := range group {
			if _, err := address(recipient); err != nil {
				return nil, err
			}
		}
	}
	return &Service{config.Backend, config.From, config.SubjectPrefix, append([]string(nil), config.Admins...), append([]string(nil), config.Managers...)}, nil
}
func (Service) String() string                            { return "mail.Service{recipients:redacted}" }
func (s Service) GoString() string                        { return s.String() }
func (s Service) Format(state fmt.State, verb rune)       { _, _ = fmt.Fprint(state, s.String()) }
func (Service) MarshalJSON() ([]byte, error)              { return nil, ErrSensitiveOutput }
func (ServiceConfig) String() string                      { return "mail.ServiceConfig{recipients:redacted}" }
func (s ServiceConfig) GoString() string                  { return s.String() }
func (s ServiceConfig) Format(state fmt.State, verb rune) { _, _ = fmt.Fprint(state, s.String()) }
func (ServiceConfig) MarshalJSON() ([]byte, error)        { return nil, ErrSensitiveOutput }

func (s *Service) Send(ctx context.Context, message Message) (Receipt, error) {
	if message.From == "" {
		message.From = s.from
	}
	return SendMail(ctx, s.backend, message)
}
func (s *Service) MailAdmins(ctx context.Context, message Message) (Receipt, error) {
	return s.notice(ctx, s.admins, message)
}
func (s *Service) MailManagers(ctx context.Context, message Message) (Receipt, error) {
	return s.notice(ctx, s.managers, message)
}
func (s *Service) notice(ctx context.Context, recipients []string, message Message) (Receipt, error) {
	message = message.Clone()
	message.From = s.from
	message.To = append([]string(nil), recipients...)
	message.Cc = nil
	message.Bcc = nil
	message.Subject = s.prefix + message.Subject
	return s.Send(ctx, message)
}
