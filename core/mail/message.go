// Package mail builds bounded MIME messages and submits them through explicit
// delivery backends. Transport acceptance never guarantees inbox delivery.
package mail

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	stdmail "net/mail"
	"net/textproto"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

var (
	ErrValidation      = errors.New("mail: invalid message")
	ErrLimit           = errors.New("mail: message limit exceeded")
	ErrSensitiveOutput = errors.New("mail: sensitive message cannot use an ordinary serialization or output backend")
)

type Alternative struct{ ContentType, Content string }
type Attachment struct {
	Filename, ContentType string
	Data                  []byte
}

// Message is a caller-owned declaration. Do not mutate it concurrently with
// sending. All built-in backends validate it before provider effects.
// Sensitive messages cannot be serialized as ordinary JSON or written to the
// console/file backends. The in-memory test outbox remains explicitly readable.
type Message struct {
	From                 string
	To, Cc, Bcc, ReplyTo []string
	Subject, Text, HTML  string
	Alternatives         []Alternative
	Attachments          []Attachment
	Headers              map[string]string
	MessageID            string
	Date                 time.Time
	Sensitive            bool
}

func (Message) String() string                      { return "mail.Message{content:redacted}" }
func (m Message) GoString() string                  { return m.String() }
func (m Message) Format(state fmt.State, verb rune) { _, _ = fmt.Fprint(state, m.String()) }
func (m Message) MarshalJSON() ([]byte, error) {
	if m.Sensitive {
		return nil, ErrSensitiveOutput
	}
	type declaration Message
	return json.Marshal(declaration(m))
}

func (m Message) Clone() Message {
	m.To, m.Cc, m.Bcc, m.ReplyTo = slices.Clone(m.To), slices.Clone(m.Cc), slices.Clone(m.Bcc), slices.Clone(m.ReplyTo)
	m.Alternatives = slices.Clone(m.Alternatives)
	m.Attachments = slices.Clone(m.Attachments)
	for i := range m.Attachments {
		m.Attachments[i].Data = slices.Clone(m.Attachments[i].Data)
	}
	m.Headers = maps.Clone(m.Headers)
	return m
}

type Limits struct{ MaxRecipients, MaxAttachments, MaxAttachmentBytes, MaxMessageBytes, MaxHeaderBytes int }

func (l Limits) defaults() (Limits, error) {
	if l.MaxRecipients == 0 {
		l.MaxRecipients = 100
	}
	if l.MaxAttachments == 0 {
		l.MaxAttachments = 20
	}
	if l.MaxAttachmentBytes == 0 {
		l.MaxAttachmentBytes = 8 << 20
	}
	if l.MaxMessageBytes == 0 {
		l.MaxMessageBytes = 10 << 20
	}
	if l.MaxHeaderBytes == 0 {
		l.MaxHeaderBytes = 64 << 10
	}
	if l.MaxRecipients < 1 || l.MaxRecipients > 1000 || l.MaxAttachments < 1 || l.MaxAttachments > 1000 || l.MaxAttachmentBytes < 1 || l.MaxMessageBytes < 1 || l.MaxMessageBytes > 100<<20 || l.MaxHeaderBytes < 1 || l.MaxHeaderBytes > 1<<20 {
		return l, ErrLimit
	}
	return l, nil
}

// Prepared is immutable validated MIME and envelope data for backend authors.
// Bytes intentionally returns secret-bearing bytes: never log them or enqueue
// them as ordinary task JSON. Bcc recipients occur only in the envelope.
type Prepared struct {
	sender     string
	recipients []string
	data       []byte
	messageID  string
	sensitive  bool
}

func (p Prepared) Sender() string                    { return p.sender }
func (p Prepared) Recipients() []string              { return slices.Clone(p.recipients) }
func (p Prepared) Bytes() []byte                     { return slices.Clone(p.data) }
func (p Prepared) MessageID() string                 { return p.messageID }
func (p Prepared) Sensitive() bool                   { return p.sensitive }
func (Prepared) String() string                      { return "mail.Prepared{content:redacted}" }
func (p Prepared) GoString() string                  { return p.String() }
func (p Prepared) Format(state fmt.State, verb rune) { _, _ = fmt.Fprint(state, p.String()) }
func (Prepared) MarshalJSON() ([]byte, error)        { return nil, ErrSensitiveOutput }

type boundedBuffer struct {
	bytes.Buffer
	maximum int
	ctx     context.Context
}

func (b *boundedBuffer) Write(value []byte) (int, error) {
	if b.ctx != nil {
		if err := b.ctx.Err(); err != nil {
			return 0, err
		}
	}
	if len(value) > b.maximum-b.Len() {
		return 0, ErrLimit
	}
	return b.Buffer.Write(value)
}
func (b *boundedBuffer) WriteString(value string) (int, error) { return b.Write([]byte(value)) }

func safeHeader(value string) bool {
	if !utf8.ValidString(value) {
		return false
	}
	for _, r := range value {
		if r < 32 || r == 127 {
			return false
		}
	}
	return true
}
func address(value string) (*stdmail.Address, error) {
	if len(value) > 512 || !safeHeader(value) {
		return nil, ErrValidation
	}
	parsed, err := stdmail.ParseAddress(value)
	if err != nil || len(parsed.Address) > 254 || !strings.Contains(parsed.Address, "@") {
		return nil, ErrValidation
	}
	// SMTPUTF8 envelope addresses require a separate negotiated capability.
	// Unicode display names and UTF-8 MIME bodies are supported independently.
	for _, r := range parsed.Address {
		if r < 33 || r > 126 {
			return nil, ErrValidation
		}
	}
	// The initial SMTP transport supports ASCII dot-atom mailboxes. Quoted
	// local parts and SMTPUTF8 require a separate envelope capability contract.
	at := strings.LastIndexByte(parsed.Address, '@')
	local := parsed.Address[:at]
	if local == "" || strings.HasPrefix(local, ".") || strings.HasSuffix(local, ".") || strings.Contains(local, "..") {
		return nil, ErrValidation
	}
	for _, r := range local {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune(".!#$%&'*+-/=?^_`{|}~", r)) {
			return nil, ErrValidation
		}
	}
	return parsed, nil
}

func Prepare(ctx context.Context, message Message, limits Limits) (Prepared, error) {
	if ctx == nil {
		return Prepared{}, ErrValidation
	}
	if err := ctx.Err(); err != nil {
		return Prepared{}, err
	}
	limits, err := limits.defaults()
	if err != nil {
		return Prepared{}, err
	}
	if len(message.To)+len(message.Cc)+len(message.Bcc) == 0 || len(message.To)+len(message.Cc)+len(message.Bcc) > limits.MaxRecipients || len(message.ReplyTo) > limits.MaxRecipients || len(message.Attachments) > limits.MaxAttachments || len(message.Alternatives) > 20 || len(message.Headers) > 100 {
		return Prepared{}, ErrLimit
	}
	if !safeHeader(message.Subject) || len(message.Subject) > 512 {
		return Prepared{}, ErrValidation
	}
	sender, err := address(message.From)
	if err != nil {
		return Prepared{}, err
	}
	prepared := Prepared{sender: sender.Address, sensitive: message.Sensitive}
	headers := textproto.MIMEHeader{"From": {sender.String()}, "Subject": {mime.QEncoding.Encode("utf-8", message.Subject)}, "MIME-Version": {"1.0"}}
	seen := map[string]bool{}
	for _, group := range []struct {
		name     string
		values   []string
		envelope bool
	}{{"To", message.To, true}, {"Cc", message.Cc, true}, {"Bcc", message.Bcc, true}, {"Reply-To", message.ReplyTo, false}} {
		formatted := []string{}
		for _, raw := range group.values {
			parsed, err := address(raw)
			if err != nil {
				return Prepared{}, err
			}
			formatted = append(formatted, parsed.String())
			if group.envelope && !seen[parsed.Address] {
				prepared.recipients = append(prepared.recipients, parsed.Address)
				seen[parsed.Address] = true
			}
		}
		if group.name != "Bcc" && len(formatted) > 0 {
			headers[group.name] = []string{strings.Join(formatted, ",\r\n ")}
		}
	}
	date := message.Date
	if date.IsZero() {
		date = time.Now()
	}
	if date.Year() < 1900 || date.Year() > 9999 {
		return Prepared{}, ErrValidation
	}
	headers.Set("Date", date.Format(time.RFC1123Z))
	id := message.MessageID
	if id == "" {
		var random [16]byte
		if _, err := rand.Read(random[:]); err != nil {
			return Prepared{}, errors.New("mail: message identity unavailable")
		}
		id = hex.EncodeToString(random[:]) + sender.Address[strings.LastIndexByte(sender.Address, '@'):]
	}
	parsedID, err := address(id)
	if err != nil || parsedID.Name != "" || parsedID.Address != id || strings.ContainsAny(id, "<> ") {
		return Prepared{}, ErrValidation
	}
	prepared.messageID = id
	headers.Set("Message-ID", "<"+id+">")
	reserved := map[string]bool{"from": true, "to": true, "cc": true, "bcc": true, "reply-to": true, "subject": true, "date": true, "message-id": true, "mime-version": true, "return-path": true, "received": true, "sender": true}
	for name, value := range message.Headers {
		lower := strings.ToLower(name)
		if reserved[lower] || strings.HasPrefix(lower, "content-") || strings.HasPrefix(lower, "resent-") || len(name) > 78 || len(value) > 512 || !safeHeader(value) {
			return Prepared{}, ErrValidation
		}
		for _, r := range name {
			if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-') {
				return Prepared{}, ErrValidation
			}
		}
		if name == "" {
			return Prepared{}, ErrValidation
		}
		canonical := textproto.CanonicalMIMEHeaderKey(name)
		if _, duplicate := headers[canonical]; duplicate {
			return Prepared{}, ErrValidation
		}
		headers[canonical] = []string{mime.QEncoding.Encode("utf-8", value)}
	}
	parts := []Alternative{}
	if message.Text != "" || message.HTML == "" && len(message.Alternatives) == 0 {
		parts = append(parts, Alternative{"text/plain", message.Text})
	}
	if message.HTML != "" {
		parts = append(parts, Alternative{"text/html", message.HTML})
	}
	parts = append(parts, message.Alternatives...)
	for i, part := range parts {
		kind, params, err := mime.ParseMediaType(part.ContentType)
		if err != nil || !strings.HasPrefix(kind, "text/") || !safeHeader(part.ContentType) || len(part.ContentType) > 512 || !utf8.ValidString(part.Content) || len(part.Content) > limits.MaxMessageBytes {
			return Prepared{}, ErrValidation
		}
		params["charset"] = "utf-8"
		parts[i].ContentType = mime.FormatMediaType(kind, params)
		if len(parts[i].ContentType)+len("Content-Type: ") > 998 {
			return Prepared{}, ErrLimit
		}
	}
	attachments := slices.Clone(message.Attachments)
	for i, attachment := range attachments {
		if err := ctx.Err(); err != nil {
			return Prepared{}, err
		}
		if attachment.Filename == "" || len(attachment.Filename) > 255 || !safeHeader(attachment.Filename) || strings.ContainsAny(attachment.Filename, "/\\") {
			return Prepared{}, ErrValidation
		}
		if len(attachment.Data) > limits.MaxAttachmentBytes {
			return Prepared{}, ErrLimit
		}
		if attachment.ContentType != "" {
			kind, params, err := mime.ParseMediaType(attachment.ContentType)
			if err != nil || len(attachment.ContentType) > 512 || !safeHeader(attachment.ContentType) {
				return Prepared{}, ErrValidation
			}
			attachments[i].ContentType = mime.FormatMediaType(kind, params)
			if len(attachments[i].ContentType)+len("Content-Type: ") > 998 {
				return Prepared{}, ErrLimit
			}
		}
	}
	body := &boundedBuffer{maximum: limits.MaxMessageBytes, ctx: ctx}
	bodyHeaders, err := renderBody(body, parts, attachments)
	if err != nil {
		return Prepared{}, err
	}
	for key, values := range bodyHeaders {
		headers[key] = values
	}
	out := &boundedBuffer{maximum: limits.MaxHeaderBytes, ctx: ctx}
	names := make([]string, 0, len(headers))
	for name := range headers {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		for _, value := range headers[name] {
			line := name + ": " + value + "\r\n"
			for _, segment := range strings.Split(line, "\r\n") {
				if len(segment) > 998 {
					return Prepared{}, ErrLimit
				}
			}
			if _, err := out.WriteString(line); err != nil {
				return Prepared{}, err
			}
		}
	}
	if out.Len()+2+body.Len() > limits.MaxMessageBytes {
		return Prepared{}, ErrLimit
	}
	prepared.data = make([]byte, 0, out.Len()+2+body.Len())
	prepared.data = append(prepared.data, out.Bytes()...)
	prepared.data = append(prepared.data, '\r', '\n')
	prepared.data = append(prepared.data, body.Bytes()...)
	if err := ctx.Err(); err != nil {
		return Prepared{}, err
	}
	return prepared, nil
}

func renderBody(out *boundedBuffer, parts []Alternative, attachments []Attachment) (textproto.MIMEHeader, error) {
	if len(attachments) == 0 {
		if len(parts) == 1 {
			if err := writeText(out, parts[0].Content); err != nil {
				return nil, err
			}
			return textproto.MIMEHeader{"Content-Type": {parts[0].ContentType}, "Content-Transfer-Encoding": {"quoted-printable"}}, nil
		}
		writer := multipart.NewWriter(out)
		if err := writeAlternatives(writer, parts); err != nil {
			return nil, err
		}
		if err := writer.Close(); err != nil {
			return nil, err
		}
		return textproto.MIMEHeader{"Content-Type": {mime.FormatMediaType("multipart/alternative", map[string]string{"boundary": writer.Boundary()})}}, nil
	}
	writer := multipart.NewWriter(out)
	if len(parts) == 1 {
		part, err := writer.CreatePart(textproto.MIMEHeader{"Content-Type": {parts[0].ContentType}, "Content-Transfer-Encoding": {"quoted-printable"}})
		if err != nil {
			return nil, err
		}
		if err := writeText(part, parts[0].Content); err != nil {
			return nil, err
		}
	} else {
		// The nested writer chooses its boundary before the containing header.
		nested := multipart.NewWriter(out)
		part, err := writer.CreatePart(textproto.MIMEHeader{"Content-Type": {mime.FormatMediaType("multipart/alternative", map[string]string{"boundary": nested.Boundary()})}})
		if err != nil {
			return nil, err
		}
		inside := multipart.NewWriter(part)
		if err := inside.SetBoundary(nested.Boundary()); err != nil {
			return nil, err
		}
		if err := writeAlternatives(inside, parts); err != nil {
			return nil, err
		}
		if err := inside.Close(); err != nil {
			return nil, err
		}
	}
	for _, attachment := range attachments {
		kind := attachment.ContentType
		if kind == "" {
			kind = "application/octet-stream"
		}
		part, err := writer.CreatePart(textproto.MIMEHeader{"Content-Type": {kind}, "Content-Disposition": {mime.FormatMediaType("attachment", map[string]string{"filename": attachment.Filename})}, "Content-Transfer-Encoding": {"base64"}})
		if err != nil {
			return nil, err
		}
		for offset := 0; offset < len(attachment.Data); offset += 57 {
			end := min(len(attachment.Data), offset+57)
			line := base64.StdEncoding.EncodeToString(attachment.Data[offset:end]) + "\r\n"
			if _, err := fmt.Fprint(part, line); err != nil {
				return nil, err
			}
		}
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}
	return textproto.MIMEHeader{"Content-Type": {mime.FormatMediaType("multipart/mixed", map[string]string{"boundary": writer.Boundary()})}}, nil
}

func writeAlternatives(writer *multipart.Writer, parts []Alternative) error {
	for _, body := range parts {
		part, err := writer.CreatePart(textproto.MIMEHeader{"Content-Type": {body.ContentType}, "Content-Transfer-Encoding": {"quoted-printable"}})
		if err != nil {
			return err
		}
		if err := writeText(part, body.Content); err != nil {
			return err
		}
	}
	return nil
}
func writeText(out interface{ Write([]byte) (int, error) }, body string) error {
	writer := quotedprintable.NewWriter(out)
	if _, err := writer.Write([]byte(body)); err != nil {
		return err
	}
	return writer.Close()
}
