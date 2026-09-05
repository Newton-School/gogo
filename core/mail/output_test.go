package mail

import (
	"bytes"
	"context"
	"errors"
	"mime"
	stdmail "net/mail"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConsoleDummyAndServiceRecipientIsolation(t *testing.T) {
	var buffer bytes.Buffer
	console, err := NewConsole(&buffer, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	m := testMessage()
	m.Sensitive = true
	m.Text = "sensitive-token"
	if _, err := console.Send(context.Background(), m); !errors.Is(err, ErrSensitiveOutput) || buffer.Len() != 0 {
		t.Fatal("sensitive console output")
	}
	m.Sensitive = false
	r, err := console.Send(context.Background(), m)
	if err != nil || !r.Simulated || r.AllAccepted() {
		t.Fatal("console receipt")
	}
	dummy, _ := NewDummy(Limits{})
	r, err = dummy.Send(context.Background(), testMessage())
	if err != nil || !r.Simulated || r.AllAccepted() {
		t.Fatal("dummy receipt")
	}
	outbox, _ := NewMemory(4, 1<<20, Limits{})
	config := ServiceConfig{Backend: outbox, From: "service@example.test", SubjectPrefix: "[Notice] ", Admins: []string{"admin@example.test"}, Managers: []string{"manager@example.test"}}
	service, err := NewService(config)
	if err != nil {
		t.Fatal(err)
	}
	config.Admins[0] = "changed@example.test"
	m = testMessage()
	m.Cc = []string{"outsider@example.test"}
	m.Bcc = []string{"hidden-outsider@example.test"}
	if _, err := service.MailAdmins(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	if _, err := service.MailManagers(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	for i, want := range []string{"admin@example.test", "manager@example.test"} {
		p := outbox.Outbox()[i]
		parsed, err := stdmail.ReadMessage(bytes.NewReader(p.Bytes()))
		if err != nil {
			t.Fatal(err)
		}
		subject, err := new(mime.WordDecoder).DecodeHeader(parsed.Header.Get("Subject"))
		if err != nil {
			t.Fatal(err)
		}
		if p.Sender() != "service@example.test" || len(p.Recipients()) != 1 || p.Recipients()[0] != want || subject != "[Notice] "+m.Subject {
			t.Fatal("configured audience escaped")
		}
	}
}

func TestPrivateFileOutboxPublishesCompleteMessagesAndRejectsSensitive(t *testing.T) {
	directory := t.TempDir()
	if err := os.Chmod(directory, 0700); err != nil {
		t.Fatal(err)
	}
	backend, err := NewFile(FileConfig{Directory: directory, MaxMessages: 2})
	if err != nil {
		t.Fatal(err)
	}
	defer backend.Close()
	m := testMessage()
	m.Sensitive = true
	m.Text = "sensitive-token"
	if _, err := backend.Send(context.Background(), m); !errors.Is(err, ErrSensitiveOutput) {
		t.Fatal("sensitive file output")
	}
	entries, _ := os.ReadDir(directory)
	if len(entries) != 0 {
		t.Fatal("sensitive file created")
	}
	for range 2 {
		r, err := backend.Send(context.Background(), testMessage())
		if err != nil || !r.Simulated || r.AllAccepted() {
			t.Fatal("file receipt")
		}
	}
	if _, err := backend.Send(context.Background(), testMessage()); !errors.Is(err, ErrLimit) {
		t.Fatal("file quota exceeded")
	}
	entries, _ = os.ReadDir(directory)
	if len(entries) != 2 {
		t.Fatal("temporary file not cleaned")
	}
	for _, entry := range entries {
		info, _ := entry.Info()
		if !strings.HasSuffix(entry.Name(), ".eml") || info.Mode().Perm() != 0600 {
			t.Fatal("private complete output required")
		}
		data, err := os.ReadFile(filepath.Join(directory, entry.Name()))
		if err != nil || !bytes.Contains(data, []byte("MIME-Version: 1.0")) {
			t.Fatal("incomplete MIME")
		}
	}
	_ = backend.Close()
	if _, err := backend.Send(context.Background(), testMessage()); !errors.Is(err, ErrClosed) {
		t.Fatal("closed file outbox reused")
	}
}

func TestFileOutboxNeverTraversesEntriesOrDisclosesPaths(t *testing.T) {
	private := t.TempDir()
	if err := os.Chmod(private, 0700); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside.eml")
	if err := os.WriteFile(outside, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(private, "link.eml")); err != nil {
		t.Skip("symlinks unavailable")
	}
	if _, err := NewFile(FileConfig{Directory: private}); !errors.Is(err, ErrValidation) {
		t.Fatal("symlink entry accepted")
	}
	data, _ := os.ReadFile(outside)
	if string(data) != "keep" {
		t.Fatal("external file changed")
	}
	if _, err := NewFile(FileConfig{Directory: filepath.Join(private, "missing")}); err == nil || strings.Contains(err.Error(), private) {
		t.Fatal("path leaked")
	}
}

type failingConsoleWriter struct{}

func (failingConsoleWriter) Write([]byte) (int, error) {
	return 0, errors.New("private writer error includes synthetic-token")
}
func TestDevelopmentOutputFailureCannotClaimAcceptanceOrLeakWriterError(t *testing.T) {
	console, _ := NewConsole(failingConsoleWriter{}, Limits{})
	receipt, err := console.Send(context.Background(), testMessage())
	if !errors.Is(err, ErrTransport) || strings.Contains(err.Error(), "synthetic-token") || receipt.Simulated || receipt.AllAccepted() {
		t.Fatal("unsafe output error")
	}
	directory := t.TempDir()
	if err := os.Chmod(directory, 0700); err != nil {
		t.Fatal(err)
	}
	outbox, err := NewFile(FileConfig{Directory: directory, MaxBytes: 10})
	if err != nil {
		t.Fatal(err)
	}
	defer outbox.Close()
	if _, err := outbox.Send(context.Background(), testMessage()); !errors.Is(err, ErrLimit) {
		t.Fatal("file byte bound ignored")
	}
	entries, _ := os.ReadDir(directory)
	if len(entries) != 0 {
		t.Fatal("quota failure left an output file")
	}
}
