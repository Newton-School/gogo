package testing

import (
	"context"

	"github.com/Newton-School/gogo/email"
)

type MailOutbox struct {
	backend *email.MemoryBackend
}

func NewMailOutbox() *MailOutbox {
	return &MailOutbox{backend: email.NewMemoryBackend()}
}

func (o *MailOutbox) Send(ctx context.Context, messages ...email.Message) (int, error) {
	return o.backend.SendMessages(ctx, messages)
}

func (o *MailOutbox) Messages() []email.Message {
	return o.backend.Messages()
}

func (o *MailOutbox) Clear() {
	o.backend.Clear()
}

func (o *MailOutbox) Backend() *email.MemoryBackend {
	return o.backend
}

func (o *MailOutbox) AssertEmailSent(t TestHelper, subject string) {
	t.Helper()
	for _, message := range o.Messages() {
		if message.Subject == subject {
			return
		}
	}
	t.Fatalf("email with subject %q was not sent; outbox=%#v", subject, o.Messages())
}
