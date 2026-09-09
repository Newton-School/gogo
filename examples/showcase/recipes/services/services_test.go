// These recipes run without external services. Memory mail is intentionally a
// test outbox; it does not establish SMTP delivery.
package services_test

import (
	"context"
	"crypto/rand"
	"errors"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/Newton-School/gogo/core/mail"
	"github.com/Newton-School/gogo/core/pagination"
	"github.com/Newton-School/gogo/core/security"
	"github.com/Newton-School/gogo/core/signals"
)

func TestMultipartMailAndHeaderDenial(t *testing.T) {
	box, err := mail.NewMemory(10, 1<<20, mail.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	message := mail.Message{From: "store@example.test", To: []string{"buyer@example.test"},
		Bcc: []string{"archive@example.test"}, Subject: "Order received", Text: "Thank you", HTML: "<p>Thank you</p>",
		Attachments: []mail.Attachment{{Filename: "receipt.txt", ContentType: "text/plain", Data: []byte("Sample receipt")}}}
	receipt, err := box.Send(context.Background(), message)
	if err != nil || !receipt.Simulated {
		t.Fatalf("expected simulated receipt: %v", err)
	}
	items := box.Outbox()
	if len(items) != 1 || len(items[0].Recipients()) != 2 {
		t.Fatal("envelope recipients missing")
	}
	mime := string(items[0].Bytes())
	if strings.Contains(mime, "archive@example.test") || !strings.Contains(mime, "multipart/mixed") {
		t.Fatal("MIME privacy or attachment contract")
	}
	message.Subject = "Order\r\nBcc: injected@example.test"
	if _, err := box.Send(context.Background(), message); !errors.Is(err, mail.ErrValidation) {
		t.Fatal("header injection accepted")
	}
	if len(box.Outbox()) != 1 {
		t.Fatal("rejected mail entered the outbox")
	}
	box.Clear()
	if len(box.Outbox()) != 0 {
		t.Fatal("outbox not cleared")
	}
}

func TestSignalsStopVersusRobust(t *testing.T) {
	type Published struct{ ProductID int64 }
	var event signals.Signal[Published]
	var received []string
	failure := errors.New("sample receiver failure")
	if err := event.Connect("failing", 0, func(context.Context, Published) error { received = append(received, "first"); return failure }); err != nil {
		t.Fatal(err)
	}
	if err := event.Connect("audit", 1, func(_ context.Context, p Published) error {
		if p.ProductID != 42 {
			t.Fatal("payload")
		}
		received = append(received, "second")
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	results, err := event.Send(context.Background(), Published{42})
	if !errors.Is(err, failure) || len(results) != 1 || len(received) != 1 {
		t.Fatal("ordinary send must stop")
	}
	received = nil
	results, err = event.SendRobust(context.Background(), Published{42})
	if !errors.Is(err, failure) || len(results) != 2 || len(received) != 2 {
		t.Fatal("robust send must retain failure and continue")
	}
	if !event.Disconnect("failing") {
		t.Fatal("disconnect")
	}
}

func TestSigningPurposeAndTamperDenial(t *testing.T) {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	signer, err := security.NewSigner(security.SigningKey{ID: "active", Value: key}, nil, "catalog-link")
	if err != nil {
		t.Fatal(err)
	}
	token, err := signer.Sign([]byte("product:42"))
	if err != nil {
		t.Fatal(err)
	}
	value, err := signer.Verify(token, time.Minute)
	if err != nil || string(value) != "product:42" {
		t.Fatal("signed value", err)
	}
	if _, err := signer.Verify(token+"x", time.Minute); !errors.Is(err, security.ErrBadSignature) {
		t.Fatal("tamper accepted")
	}
	other, err := security.NewSigner(security.SigningKey{ID: "active", Value: key}, nil, "password-reset")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := other.Verify(token, time.Minute); !errors.Is(err, security.ErrBadSignature) {
		t.Fatal("cross-purpose replay accepted")
	}
}

func TestBoundedPaginationModes(t *testing.T) {
	for _, mode := range []pagination.Mode{pagination.PageNumber, pagination.LimitOffset} {
		pager, err := pagination.New(pagination.Config{Mode: mode, DefaultSize: 10, MaxSize: 20})
		if err != nil {
			t.Fatal(err)
		}
		query := url.Values{"page": {"2"}}
		if mode == pagination.LimitOffset {
			query = url.Values{"offset": {"10"}}
		}
		page, err := pager.Parse(query)
		if err != nil || page.Offset != 10 || page.Size != 10 {
			t.Fatal("page", page, err)
		}
		next, previous := pager.Links(query, page, true)
		if !strings.HasPrefix(next, "?") || !strings.HasPrefix(previous, "?") {
			t.Fatal("relative navigation")
		}
		if _, err := pager.Parse(url.Values{"page": {"2"}, "offset": {"10"}}); !errors.Is(err, pagination.ErrInvalid) {
			t.Fatal("mixed modes accepted")
		}
	}
}
