package integration_test

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"example.com/gogo-integration/internal/testservice"
	"github.com/Newton-School/gogo/connectors/postgres"
	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/files"
	"github.com/Newton-School/gogo/core/migrations"
	"github.com/Newton-School/gogo/core/models"
	"github.com/Newton-School/gogo/core/orm"
)

type nativeFileStorage struct {
	files.Storage
	saves, opens int
	saveHook     func()
}

func (s *nativeFileStorage) Save(ctx context.Context, key string, r io.Reader) (files.SaveResult, error) {
	s.saves++
	result, err := s.Storage.Save(ctx, key, r)
	if s.saveHook != nil {
		s.saveHook()
	}
	return result, err
}
func (s *nativeFileStorage) Open(ctx context.Context, key string) (files.Reader, error) {
	s.opens++
	return s.Storage.Open(ctx, key)
}

type nativeFileFixture struct {
	backend  *postgres.Backend
	registry *models.Registry
	storage  *nativeFileStorage
	config   files.ServiceConfig
	grants   func(context.Context, files.OwnerAction, files.OwnerSnapshot) error
}

func newNativeFileFixture(t *testing.T) *nativeFileFixture {
	t.Helper()
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("local storage provider supports Linux and macOS")
	}
	ctx := context.Background()
	b := testservice.Postgres(t)
	owner := models.Schema{AppLabel: "files_owner", Name: "Document", Table: "files_document", PrimaryKey: []string{"tenant", "id"}, Fields: []models.Field{
		models.CharField("tenant", models.WithMaxLength(32), models.WithColumn("tenant_key")), models.BigIntegerField("id", models.WithColumn("document_number")),
		models.FileField("attachment", models.Optional, models.WithMaxLength(32), models.WithColumn("private_asset")), models.BooleanField("allowed"), models.TextField("title"),
	}}
	if err := b.SchemaEditor().CreateModel(ctx, b, owner); err != nil {
		t.Fatal(err)
	}
	runner := &migrations.Executor{Backend: b, Editor: b.SchemaEditor(), Migrations: files.Migrations()}
	if err := runner.Apply(ctx, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Exec(ctx, `INSERT INTO files_document (tenant_key,document_number,private_asset,allowed,title) VALUES ('one',1,'',true,'Keep this title'),('two',1,'',true,'Hidden title')`); err != nil {
		t.Fatal(err)
	}
	registry := &models.Registry{}
	for _, schema := range []models.Schema{owner, (&files.File{}).Schema()} {
		if err := registry.Register(schema); err != nil {
			t.Fatal(err)
		}
	}
	if err := registry.Freeze(); err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	if err := os.Chmod(directory, 0700); err != nil {
		t.Fatal(err)
	}
	local, err := files.NewLocal(files.LocalConfig{Directory: directory})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := local.Close(); err != nil {
			t.Error(err)
		}
	})
	f := &nativeFileFixture{backend: b, registry: registry, storage: &nativeFileStorage{Storage: local}}
	f.config = files.ServiceConfig{Backend: b, Registry: registry, StorageAlias: "private", Storage: f.storage, Bindings: []files.OwnerBinding{{Name: "document.attachment", Model: owner.Key(), Field: "attachment", PolicyFields: []string{"allowed"}, Scope: func(ctx context.Context, _ models.Schema) (db.Predicate, error) {
		if !db.InTransaction(ctx, b.Alias()) {
			t.Fatal("scope escaped owned transaction")
		}
		return orm.Q("tenant", "one"), nil
	}, Authorize: func(ctx context.Context, action files.OwnerAction, owner files.OwnerSnapshot) error {
		if owner.Key["tenant"] != "one" || owner.Key["id"] != int64(1) || len(owner.Fields) != 1 {
			return files.ErrForbidden
		}
		if owner.Fields["allowed"] != true {
			return files.ErrForbidden
		}
		if f.grants != nil {
			return f.grants(ctx, action, owner)
		}
		return nil
	}}}}
	return f
}
func (f *nativeFileFixture) service(t *testing.T) *files.Service {
	t.Helper()
	s, err := files.NewService(f.config)
	if err != nil {
		t.Fatal(err)
	}
	return s
}
func nativeFileInput(t *testing.T) files.StoreInput {
	t.Helper()
	id, err := files.NewUploadIdentity()
	if err != nil {
		t.Fatal(err)
	}
	return files.StoreInput{Identity: id, Owner: files.OwnerInput{Binding: "document.attachment", Key: map[string]any{"tenant": "one", "id": int64(1)}}, ContentType: "text/plain; charset=utf-8"}
}
func nativeFileCount(t *testing.T, b db.Executor, id string) int {
	t.Helper()
	var count int
	if err := db.QueryRow(context.Background(), b, `SELECT count(*) FROM gogo_files WHERE id=$1`, []any{id}, &count); err != nil {
		t.Fatal(err)
	}
	return count
}
func nativeFileLink(t *testing.T, b db.Executor) string {
	t.Helper()
	var value string
	if err := db.QueryRow(context.Background(), b, `SELECT private_asset FROM files_document WHERE tenant_key='one' AND document_number=1`, nil, &value); err != nil {
		t.Fatal(err)
	}
	return value
}

func TestFilesServiceNativeMetadataOwnerBindingAndReplacement(t *testing.T) {
	f := newNativeFileFixture(t)
	s := f.service(t)
	input := nativeFileInput(t)
	ctx := context.Background()
	var actions []files.OwnerAction
	f.grants = func(ctx context.Context, action files.OwnerAction, owner files.OwnerSnapshot) error {
		actions = append(actions, action)
		var readOnly, isolation string
		if err := db.QueryRow(ctx, db.ExecutorFor(ctx, f.backend), `SELECT current_setting('transaction_read_only'),current_setting('transaction_isolation')`, nil, &readOnly, &isolation); err != nil {
			t.Fatal(err)
		}
		if len(actions) == 1 || action == files.ReadFile {
			if readOnly != "on" || isolation != "repeatable read" {
				t.Fatal(readOnly, isolation)
			}
		}
		owner.Key["id"] = int64(9)
		owner.Fields["allowed"] = false
		return nil
	}
	info, err := s.StoreValidated(ctx, input, strings.NewReader("private original"))
	if err != nil || info.ID != input.Identity.ID || info.ObjectKey != input.Identity.Key || info.State != files.Ready || info.Bytes != 16 || info.Checksum != fmt.Sprintf("%x", sha256.Sum256([]byte("private original"))) {
		t.Fatal(info, err)
	}
	if nativeFileLink(t, f.backend) != input.Identity.Key || nativeFileCount(t, f.backend, info.ID) != 1 {
		t.Fatal("metadata and opaque owner key diverged")
	}
	var title, reference string
	if err := db.QueryRow(ctx, f.backend, `SELECT title FROM files_document WHERE tenant_key='one' AND document_number=1`, nil, &title); err != nil || title != "Keep this title" {
		t.Fatal(title, err)
	}
	if err := db.QueryRow(ctx, f.backend, `SELECT owner_ref FROM gogo_files WHERE id=$1`, []any{info.ID}, &reference); err != nil || !strings.Contains(reference, `"model":"files_owner.Document"`) || !strings.Contains(reference, `"field":"attachment"`) {
		t.Fatal(reference, err)
	}
	_, started, err := s.Open(ctx, info.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer started.Close()
	nextInput := nativeFileInput(t)
	next, err := s.StoreValidated(ctx, nextInput, strings.NewReader("replacement"))
	if err != nil || nativeFileLink(t, f.backend) != next.ObjectKey {
		t.Fatal(next, err)
	}
	var state string
	if err := db.QueryRow(ctx, f.backend, `SELECT state FROM gogo_files WHERE id=$1`, []any{info.ID}, &state); err != nil || state != string(files.Deleting) {
		t.Fatal(state, err)
	}
	if present, err := f.storage.Storage.Exists(ctx, info.ObjectKey); err != nil || !present {
		t.Fatal("replacement deleted old blob", err)
	}
	before := f.storage.opens
	if _, r, err := s.Open(ctx, info.ID); err != files.ErrNotFound || r != nil || f.storage.opens != before {
		t.Fatal("retired metadata touched storage", err)
	}
	body, err := io.ReadAll(started)
	if err != nil || string(body) != "private original" {
		t.Fatal("replacement retroactively revoked an existing reader", string(body), err)
	}
	if _, err := f.backend.Exec(ctx, `UPDATE files_document SET private_asset=$1 WHERE tenant_key='one' AND document_number=1`, info.ObjectKey); err != nil {
		t.Fatal(err)
	}
	before = f.storage.opens
	if _, r, err := s.Open(ctx, next.ID); err != files.ErrNotFound || r != nil || f.storage.opens != before {
		t.Fatal("stale ready metadata bypassed current owner link", err)
	}
	if !reflect.DeepEqual(actions[:2], []files.OwnerAction{files.StoreFile, files.StoreFile}) {
		t.Fatal(actions)
	}
}

type nativeFileReadTripwire struct{ calls int }

func (r *nativeFileReadTripwire) Read([]byte) (int, error) { r.calls++; return 0, io.EOF }
func TestFilesServiceNativeDenialDriftAndScopedAbsence(t *testing.T) {
	for _, mode := range []string{"initial-denial", "hidden-owner", "source-revocation", "policy-write", "postwrite-trigger", "unmanaged", "replace-denial"} {
		t.Run(mode, func(t *testing.T) {
			f := newNativeFileFixture(t)
			s := f.service(t)
			input := nativeFileInput(t)
			ctx := context.Background()
			expected := files.ErrForbidden
			if mode == "replace-denial" {
				if _, err := s.StoreValidated(ctx, nativeFileInput(t), strings.NewReader("old")); err != nil {
					t.Fatal(err)
				}
			}
			original := nativeFileLink(t, f.backend)
			calls := 0
			f.grants = func(ctx context.Context, action files.OwnerAction, _ files.OwnerSnapshot) error {
				calls++
				if mode == "initial-denial" || mode == "replace-denial" && action == files.ReplaceFile {
					return files.ErrForbidden
				}
				if mode == "policy-write" && calls == 2 {
					_, err := db.ExecutorFor(ctx, f.backend).Exec(ctx, `UPDATE files_document SET allowed=false WHERE tenant_key='one' AND document_number=1`)
					return err
				}
				return nil
			}
			switch mode {
			case "hidden-owner":
				input.Owner.Key["tenant"] = "two"
				expected = files.ErrNotFound
			case "source-revocation":
				f.storage.saveHook = func() {
					if _, err := f.backend.Exec(ctx, `UPDATE files_document SET allowed=false WHERE tenant_key='one' AND document_number=1`); err != nil {
						t.Fatal(err)
					}
				}
			case "unmanaged":
				expected = files.ErrMetadataConflict
				original = strings.Repeat("b", 32)
				if _, err := f.backend.Exec(ctx, `UPDATE files_document SET private_asset=$1 WHERE tenant_key='one' AND document_number=1`, original); err != nil {
					t.Fatal(err)
				}
			case "policy-write":
				expected = files.ErrMetadataConflict
			case "postwrite-trigger":
				expected = files.ErrMetadataConflict
				if _, err := f.backend.Exec(ctx, `CREATE FUNCTION change_file_metadata() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN NEW.content_type := 'text/html'; RETURN NEW; END $$`); err != nil {
					t.Fatal(err)
				}
				if _, err := f.backend.Exec(ctx, `CREATE TRIGGER change_file_metadata BEFORE INSERT ON gogo_files FOR EACH ROW EXECUTE FUNCTION change_file_metadata()`); err != nil {
					t.Fatal(err)
				}
			}
			reader := &nativeFileReadTripwire{}
			info, err := s.StoreValidated(ctx, input, reader)
			if !errors.Is(err, expected) || info != (files.Info{}) || nativeFileCount(t, f.backend, input.Identity.ID) != 0 || nativeFileLink(t, f.backend) != original {
				t.Fatal(info, err, expected)
			}
			var failure *files.StoreFailure
			if !errors.As(err, &failure) {
				t.Fatal(err)
			}
			if mode == "initial-denial" || mode == "hidden-owner" {
				if reader.calls != 0 || failure.Published {
					t.Fatal("denied request read source")
				}
			} else {
				if !failure.Published {
					t.Fatal("published blob hidden by DB failure")
				}
				if present, e := f.storage.Storage.Exists(ctx, input.Identity.Key); e != nil || !present {
					t.Fatal("unsafe failed-bind cleanup", e)
				}
			}
		})
	}
}

type nativeFileOutcomeBackend struct {
	db.Backend
	outcome string
	cancel  context.CancelFunc
}

func (b *nativeFileOutcomeBackend) BeginTx(ctx context.Context, options db.TxOptions) (db.Transaction, error) {
	tx, err := b.Backend.BeginTx(ctx, options)
	if err != nil || options.ReadOnly {
		return tx, err
	}
	return &nativeFileOutcomeTx{Transaction: tx, backend: b}, nil
}

type nativeFileOutcomeTx struct {
	db.Transaction
	backend *nativeFileOutcomeBackend
}

func (tx *nativeFileOutcomeTx) Commit() error {
	if err := tx.Transaction.Commit(); err != nil {
		return err
	}
	switch tx.backend.outcome {
	case "unknown-after-real-commit":
		return &db.Error{Code: db.UnknownCommit}
	case "late-cancel":
		tx.backend.cancel()
	}
	return nil
}

func TestFilesServiceNativeObservedCommitOutcomes(t *testing.T) {
	for _, mode := range []string{"unknown-after-real-commit", "late-cancel", "callback-error", "callback-panic"} {
		t.Run(mode, func(t *testing.T) {
			f := newNativeFileFixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			wrapper := &nativeFileOutcomeBackend{Backend: f.backend, outcome: mode, cancel: cancel}
			f.config.Backend = wrapper
			f.grants = func(txctx context.Context, action files.OwnerAction, _ files.OwnerSnapshot) error {
				var readOnly string
				if err := db.QueryRow(txctx, db.ExecutorFor(txctx, f.backend), `SHOW transaction_read_only`, nil, &readOnly); err != nil {
					return err
				}
				if action == files.StoreFile && readOnly == "off" && (mode == "callback-error" || mode == "callback-panic") {
					return db.OnCommit(txctx, f.backend.Alias(), func(context.Context) error {
						if mode == "callback-panic" {
							panic("private after-commit")
						}
						return errors.New("private after-commit")
					}, false)
				}
				return nil
			}
			input := nativeFileInput(t)
			info, err := f.service(t).StoreValidated(ctx, input, strings.NewReader("committed bytes"))
			if nativeFileCount(t, f.backend, input.Identity.ID) != 1 || nativeFileLink(t, f.backend) != input.Identity.Key {
				t.Fatal("actual commit was not retained", info, err)
			}
			if mode == "late-cancel" {
				if err != nil || info.ID != input.Identity.ID {
					t.Fatal(info, err)
				}
				return
			}
			var failure *files.StoreFailure
			if !errors.As(err, &failure) || !failure.Published {
				t.Fatal(info, err)
			}
			if mode == "unknown-after-real-commit" {
				if !errors.Is(err, files.ErrOutcomeUnknown) || failure.Committed || info != (files.Info{}) {
					t.Fatal(info, err)
				}
			} else if !errors.Is(err, files.ErrCommittedCallback) || !failure.Committed || info.ID != input.Identity.ID {
				t.Fatal(info, err)
			}
		})
	}
}

func TestFilesServiceNativeConcurrentReplacementAndReadOnlyScope(t *testing.T) {
	f := newNativeFileFixture(t)
	s := f.service(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	first, err := s.StoreValidated(ctx, nativeFileInput(t), strings.NewReader("initial"))
	if err != nil {
		t.Fatal(err)
	}
	inputs := []files.StoreInput{nativeFileInput(t), nativeFileInput(t)}
	type result struct {
		info files.Info
		err  error
	}
	results := make(chan result, 2)
	// Each call preallocates a distinct identity. Root row locking serializes
	// replacements; both may succeed, but only one ready current link remains.
	// Separate counting wrappers avoid making the test's observation counters
	// a concurrent mutable provider state.
	for _, input := range inputs {
		go func(input files.StoreInput) {
			config := f.config
			config.Storage = f.storage.Storage
			service, e := files.NewService(config)
			if e != nil {
				results <- result{err: e}
				return
			}
			info, e := service.StoreValidated(ctx, input, strings.NewReader("next"))
			results <- result{info, e}
		}(input)
	}
	for range inputs {
		got := <-results
		if got.err != nil {
			t.Fatal(got.err)
		}
	}
	var ready, deleting int
	if err := db.QueryRow(ctx, f.backend, `SELECT count(*) FILTER (WHERE state='ready'),count(*) FILTER (WHERE state='deleting') FROM gogo_files`, nil, &ready, &deleting); err != nil || ready != 1 || deleting != 2 {
		t.Fatal(ready, deleting, err)
	}
	if present, err := f.storage.Storage.Exists(ctx, first.ObjectKey); err != nil || !present {
		t.Fatal("concurrent replace deleted old blob", err)
	}
	before := f.storage.opens
	f.grants = func(txctx context.Context, _ files.OwnerAction, _ files.OwnerSnapshot) error {
		_, err := db.ExecutorFor(txctx, f.backend).Exec(txctx, `UPDATE files_document SET title='forbidden policy write'`)
		return err
	}
	var currentID string
	if err := db.QueryRow(ctx, f.backend, `SELECT id FROM gogo_files WHERE state='ready'`, nil, &currentID); err != nil {
		t.Fatal(err)
	}
	if _, r, err := s.Open(ctx, currentID); err != files.ErrUnavailable || r != nil || f.storage.opens != before {
		t.Fatal("read-only authorization wrote or touched storage", err)
	}
}
