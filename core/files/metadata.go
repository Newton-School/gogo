package files

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"mime"
	"strings"
	"time"

	"github.com/Newton-School/gogo/core/migrations"
	"github.com/Newton-School/gogo/core/models"
)

type State string

const (
	Staged                 State = "staged"
	Ready                  State = "ready"
	Quarantined            State = "quarantined"
	Deleting               State = "deleting"
	Deleted                State = "deleted"
	StorageKeyConstraint         = "gogo_files_storage_object_key"
	MaxOwnerReferenceBytes       = 4096
)

// File is privileged metadata. Ordinary ORM writes do not authorize an owner,
// publish a blob, or preserve its link; use Service for supported mutations.
type File struct {
	models.Base
	ID, StorageAlias, ObjectKey, OwnerRef string
	State                                 State
	ContentType                           string
	Bytes                                 int64
	Checksum                              string
	CreatedAt                             time.Time
	FinalizedAt                           *time.Time
}

func (*File) Schema() models.Schema {
	return models.Schema{AppLabel: "gogo_files", Name: "File", Table: "gogo_files", Fields: []models.Field{
		models.UUIDField("id", models.WithStructField("ID"), models.Primary, models.ReadOnly),
		models.CharField("storage_alias", models.WithStructField("StorageAlias"), models.WithMaxLength(128)),
		models.CharField("object_key", models.WithStructField("ObjectKey"), models.WithMaxLength(32)),
		models.TextField("owner_ref", models.WithStructField("OwnerRef")),
		models.CharField("state", models.WithStructField("State"), models.WithMaxLength(16), models.WithChoices(
			models.Choice{Value: string(Staged), Label: "Staged"}, models.Choice{Value: string(Ready), Label: "Ready"}, models.Choice{Value: string(Quarantined), Label: "Quarantined"}, models.Choice{Value: string(Deleting), Label: "Deleting"}, models.Choice{Value: string(Deleted), Label: "Deleted"})),
		models.CharField("content_type", models.WithStructField("ContentType"), models.WithMaxLength(255)),
		models.BigIntegerField("bytes", models.WithStructField("Bytes")),
		models.CharField("checksum", models.WithStructField("Checksum"), models.WithMaxLength(64)),
		models.DateTimeField("created_at", models.WithStructField("CreatedAt")),
		models.DateTimeField("finalized_at", models.WithStructField("FinalizedAt"), models.Nullable),
	}, Constraints: []models.Constraint{
		{Name: StorageKeyConstraint, Kind: "unique", Fields: []string{"storage_alias", "object_key"}},
		{Name: "gogo_files_nonnegative_bytes", Kind: "check", Expression: `"bytes" >= 0`},
		{Name: "gogo_files_known_state", Kind: "check", Expression: `"state" IN ('staged','ready','quarantined','deleting','deleted')`},
	}, Ordering: []string{"id"}}
}

// Migrations is explicitly registered/applied by the application. There is no
// generic foreign key for owner_ref and no implicit migration or schema adoption.
func Migrations() []migrations.Migration {
	return []migrations.Migration{{App: "gogo_files", Name: "0001_initial", Operations: []migrations.Operation{migrations.CreateModel((&File{}).Schema())}}}
}

// UploadIdentity must be retained before starting an effectful operation. It
// remains usable for privileged reconciliation if the database outcome is unknown.
type UploadIdentity struct{ ID, Key string }

func NewUploadIdentity() (UploadIdentity, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return UploadIdentity{}, ErrUnavailable
	}
	raw[6], raw[8] = raw[6]&0x0f|0x40, raw[8]&0x3f|0x80
	v := hex.EncodeToString(raw[:])
	key, err := NewKey()
	if err != nil {
		return UploadIdentity{}, err
	}
	return UploadIdentity{ID: v[:8] + "-" + v[8:12] + "-" + v[12:16] + "-" + v[16:20] + "-" + v[20:], Key: key}, nil
}

func metadataID(value string) (string, error) {
	if len(value) != 36 || value[8] != '-' || value[13] != '-' || value[18] != '-' || value[23] != '-' {
		return "", ErrInvalidOwner
	}
	nonzero := false
	for i, c := range []byte(value) {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			continue
		}
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return "", ErrInvalidOwner
		}
		nonzero = nonzero || c != '0'
	}
	if !nonzero {
		return "", ErrInvalidOwner
	}
	return strings.ToLower(value), nil
}

func canonicalContentType(value string) (string, error) {
	if len(value) == 0 || len(value) > 255 || strings.ContainsAny(value, "\x00\r\n") {
		return "", ErrInvalidOwner
	}
	media, parameters, err := mime.ParseMediaType(value)
	if err != nil || !strings.Contains(media, "/") {
		return "", ErrInvalidOwner
	}
	canonical := mime.FormatMediaType(media, parameters)
	if canonical == "" || len(canonical) > 255 || canonical != value {
		return "", ErrInvalidOwner
	}
	return canonical, nil
}

func (f *File) Clean(ctx context.Context) error {
	if f == nil {
		return ErrConfiguration
	}
	if err := storageContext(ctx); err != nil {
		return err
	}
	id, err := metadataID(f.ID)
	if err != nil || id != f.ID || !bindingName(f.StorageAlias) || !validKey(f.ObjectKey) || len(f.OwnerRef) == 0 || len(f.OwnerRef) > MaxOwnerReferenceBytes || f.Bytes < 0 || f.CreatedAt.IsZero() {
		return ErrInvalidOwner
	}
	if _, err := canonicalContentType(f.ContentType); err != nil {
		return err
	}
	if len(f.Checksum) != 64 || !validKey(f.Checksum[:32]) || !validKey(f.Checksum[32:]) {
		return ErrInvalidOwner
	}
	switch f.State {
	case Staged, Ready, Quarantined, Deleting, Deleted:
	default:
		return ErrInvalidOwner
	}
	if f.FinalizedAt != nil && (f.FinalizedAt.IsZero() || f.FinalizedAt.Before(f.CreatedAt)) {
		return ErrInvalidOwner
	}
	if f.State == Ready && f.FinalizedAt == nil {
		return ErrInvalidOwner
	}
	return nil
}
