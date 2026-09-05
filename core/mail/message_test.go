package mail

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	stdmail "net/mail"
	"strings"
	"sync"
	"testing"
)

func testMessage() Message {
	return Message{From: "Gogo <sender@example.test>", To: []string{"reader@example.test"}, Subject: "Résumé ✓", Text: "A message.\n.Second line", HTML: "<p>A message.</p>"}
}

func TestPrepareMultipartEnvelopeAndCloning(t *testing.T) {
	m := testMessage()
	m.Cc = []string{"reader@example.test", "Copy <copy@example.test>"}
	m.Bcc = []string{"hidden@example.test"}
	m.ReplyTo = []string{"reply@example.test"}
	m.Attachments = []Attachment{{Filename: "résumé.txt", ContentType: "text/plain", Data: []byte("attachment\x00data")}}
	p, err := Prepare(context.Background(), m, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Recipients()) != 3 || p.Sender() != "sender@example.test" {
		t.Fatal("bad envelope")
	}
	parsed, err := stdmail.ReadMessage(bytes.NewReader(p.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Header.Get("Bcc") != "" || bytes.Contains(p.Bytes(), []byte("hidden@example.test")) {
		t.Fatal("Bcc disclosed")
	}
	subject, err := new(mime.WordDecoder).DecodeHeader(parsed.Header.Get("Subject"))
	if err != nil || subject != m.Subject {
		t.Fatal("subject roundtrip")
	}
	kind, params, err := mime.ParseMediaType(parsed.Header.Get("Content-Type"))
	if err != nil || kind != "multipart/mixed" {
		t.Fatal("mixed missing")
	}
	mixed := multipart.NewReader(parsed.Body, params["boundary"])
	body, err := mixed.NextPart()
	if err != nil {
		t.Fatal(err)
	}
	kind, params, _ = mime.ParseMediaType(body.Header.Get("Content-Type"))
	if kind != "multipart/alternative" {
		t.Fatal("alternative missing")
	}
	alternatives := multipart.NewReader(body, params["boundary"])
	for _, want := range []string{strings.ReplaceAll(m.Text, "\n", "\r\n"), m.HTML} {
		part, err := alternatives.NextPart()
		if err != nil {
			t.Fatal(err)
		}
		raw, err := io.ReadAll(part)
		if err != nil || string(raw) != want {
			t.Fatalf("body mismatch %q", raw)
		}
	}
	attachment, err := mixed.NextPart()
	if err != nil {
		t.Fatal(err)
	}
	if attachment.FileName() != "résumé.txt" {
		t.Fatal("filename roundtrip")
	}
	raw, err := io.ReadAll(base64.NewDecoder(base64.StdEncoding, attachment))
	if err != nil || !bytes.Equal(raw, m.Attachments[0].Data) {
		t.Fatal("attachment roundtrip")
	}
	copyBytes := p.Bytes()
	copyBytes[0] = 'X'
	recipients := p.Recipients()
	recipients[0] = "changed@example.test"
	if p.Bytes()[0] == 'X' || p.Recipients()[0] == recipients[0] {
		t.Fatal("prepared aliases leaked")
	}
	clone := m.Clone()
	clone.Attachments[0].Data[0] = 'X'
	clone.To[0] = "changed@example.test"
	if m.Attachments[0].Data[0] == 'X' || m.To[0] == clone.To[0] {
		t.Fatal("message clone aliases")
	}
}

func TestPrepareRejectsUnsafeAndUnboundedInput(t *testing.T) {
	cases := map[string]func(*Message){
		"subject newline":      func(m *Message) { m.Subject = "hello\r\nBcc: victim@example.test" },
		"address newline":      func(m *Message) { m.To = []string{"reader@example.test\r\nDATA"} },
		"Bcc override":         func(m *Message) { m.Headers = map[string]string{"bCc": "hidden@example.test"} },
		"header duplicate":     func(m *Message) { m.Headers = map[string]string{"X-Test": "a", "x-test": "b"} },
		"header name":          func(m *Message) { m.Headers = map[string]string{"bad:name": "a"} },
		"header newline":       func(m *Message) { m.Headers = map[string]string{"X-Test": "a\nb"} },
		"attachment traversal": func(m *Message) { m.Attachments = []Attachment{{Filename: "../data.txt"}} },
		"attachment type newline": func(m *Message) {
			m.Attachments = []Attachment{{Filename: "data.txt", ContentType: "text/plain\r\nX-Bad: value"}}
		},
		"alternative newline": func(m *Message) { m.Alternatives = []Alternative{{"text/plain\r\nX-Bad: value", "x"}} },
		"unicode envelope":    func(m *Message) { m.To = []string{"é@example.test"} },
		"quoted local":        func(m *Message) { m.To = []string{"\"a@b\"@example.test"} },
		"invalid message id":  func(m *Message) { m.MessageID = "message@example.test>\r\nX: value" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			m := testMessage()
			mutate(&m)
			if _, err := Prepare(context.Background(), m, Limits{}); !errors.Is(err, ErrValidation) {
				t.Fatalf("expected safe validation error, got %v", err)
			}
		})
	}
	for _, limits := range []Limits{{MaxMessageBytes: 100}, {MaxHeaderBytes: 20}, {MaxAttachmentBytes: 1}, {MaxRecipients: 1}} {
		m := testMessage()
		m.Cc = []string{"copy@example.test"}
		m.Attachments = []Attachment{{Filename: "data.txt", Data: []byte("xx")}}
		if _, err := Prepare(context.Background(), m, limits); !errors.Is(err, ErrLimit) {
			t.Fatalf("expected limit error: %v", err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Prepare(ctx, testMessage(), Limits{}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestSensitiveMessageAndPreparedAreRedacted(t *testing.T) {
	m := testMessage()
	m.Sensitive = true
	m.Text = "secret-reset-token"
	m.Headers = map[string]string{"X-Private": "private"}
	if _, err := json.Marshal(m); !errors.Is(err, ErrSensitiveOutput) {
		t.Fatal(err)
	}
	p, err := Prepare(context.Background(), m, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := json.Marshal(p); !errors.Is(err, ErrSensitiveOutput) {
		t.Fatal(err)
	}
	outbox, _ := NewMemory(1, 1<<20, Limits{})
	_, _ = outbox.Send(context.Background(), m)
	transport, _ := NewSMTP(SMTPConfig{Host: "mail.example.test", Username: "synthetic", Password: "secret-reset-token"})
	defer transport.Close()
	for _, value := range []any{m, &m, p, &p, outbox, *outbox, transport, *transport, transport.config} {
		for _, format := range []string{"%v", "%+v", "%#v", "%d", "%s", "%x", "%q", "%f"} {
			if text := fmt.Sprintf(format, value); strings.Contains(text, "secret-reset-token") || strings.Contains(text, "reader@example.test") {
				t.Fatal("format leaked message")
			}
		}
	}
}

func TestMemoryOutboxIsBoundedConcurrentAndSimulated(t *testing.T) {
	outbox, err := NewMemory(4, 1<<20, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 10 {
		wg.Go(func() {
			receipt, err := outbox.Send(context.Background(), testMessage())
			if err != nil && !errors.Is(err, ErrLimit) {
				t.Error(err)
			}
			if err == nil && (!receipt.Simulated || receipt.AllAccepted()) {
				t.Error("simulation claimed acceptance")
			}
		})
	}
	wg.Wait()
	if len(outbox.Outbox()) != 4 {
		t.Fatal("outbox bound")
	}
	outbox.Clear()
	if len(outbox.Outbox()) != 0 {
		t.Fatal("clear")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	deliveries := SendMassMail(ctx, outbox, []Message{testMessage(), testMessage()})
	if len(deliveries) != 2 || !errors.Is(deliveries[1].Err, context.Canceled) || len(outbox.Outbox()) != 0 {
		t.Fatal("canceled batch submitted")
	}
}
