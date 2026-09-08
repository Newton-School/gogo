package redirects

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"strings"

	"github.com/Newton-School/gogo/core/migrations"
	"github.com/Newton-School/gogo/core/models"
)

const SitePathConstraint = "gogo_redirects_site_old_path"

// Redirect is application-managed content, not an authorization rule. An empty
// NewPath means gone; Permanent applies only to nonempty destinations.
type Redirect struct {
	models.Base
	ID, SiteID, OldPath, NewPath string
	Permanent                    bool
}

func (*Redirect) Schema() models.Schema {
	return models.Schema{AppLabel: "gogo_redirects", Name: "Redirect", Table: "gogo_redirects", Fields: []models.Field{
		models.UUIDField("id", models.WithStructField("ID"), models.Primary, models.ReadOnly),
		models.ForeignKeyField("site_id", models.Relation{Target: "gogo_sites.Site", OnDelete: models.Cascade, RelatedName: "redirects"}, models.WithStructField("SiteID")),
		models.CharField("old_path", models.WithStructField("OldPath"), models.WithMaxLength(MaxURIBytes)),
		models.CharField("new_path", models.WithStructField("NewPath"), models.WithMaxLength(MaxURIBytes), models.Optional, models.WithDefault("")),
		models.BooleanField("permanent", models.WithStructField("Permanent"), models.WithDefault(true)),
	}, Constraints: []models.Constraint{{Name: SitePathConstraint, Kind: "unique", Fields: []string{"site_id", "old_path"}}}, Ordering: []string{"old_path"}}
}

// NewRedirect allocates a UUID and validates content without accessing storage.
// An explicit later Permanent=false is preserved by the ORM default machinery.
func NewRedirect(siteID, oldPath, newPath string) (*Redirect, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return nil, ErrUnavailable
	}
	raw[6], raw[8] = raw[6]&0x0f|0x40, raw[8]&0x3f|0x80
	value := hex.EncodeToString(raw[:])
	r := &Redirect{ID: value[:8] + "-" + value[8:12] + "-" + value[12:16] + "-" + value[16:20] + "-" + value[20:], SiteID: siteID, OldPath: oldPath, NewPath: newPath, Permanent: true}
	r.ModelState().Provided = map[string]bool{"permanent": true}
	if err := r.Clean(context.Background()); err != nil {
		return nil, err
	}
	return r, nil
}

// Clean is explicit FullClean/ModelForm validation. Ordinary Save does not call
// Clean automatically; use Prepare at the final application save boundary.
func (r *Redirect) Clean(ctx context.Context) error {
	if r == nil {
		return ErrConfiguration
	}
	if err := contextError(ctx); err != nil {
		return err
	}
	id, err := redirectID(r.ID)
	if err != nil {
		return models.Invalid("invalid", "Enter a valid redirect UUID.")
	}
	siteID, err := redirectID(r.SiteID)
	if err != nil {
		return models.Invalid("invalid", "Enter a valid site UUID.")
	}
	oldPath, err := parseOldPath(r.OldPath)
	if err != nil {
		return models.Invalid("invalid", "Enter a valid redirect source path.")
	}
	newPath, err := validateStoredTarget(r.NewPath)
	if err != nil {
		return models.Invalid("invalid", "Enter a valid redirect destination.")
	}
	r.ID, r.SiteID, r.OldPath, r.NewPath = id, siteID, oldPath, newPath
	return nil
}

// Prepare validates an application-authorized save; it grants no permission
// and adds no transaction. Privileged bulk/raw writers preserve these rules.
func Prepare(ctx context.Context, record models.Record) error {
	model, ok := models.Underlying(record)
	if !ok {
		return ErrConfiguration
	}
	r, ok := model.(*Redirect)
	if !ok {
		return ErrConfiguration
	}
	return r.Clean(ctx)
}

// Migrations must be explicitly combined with sites.Migrations and applied.
// Neither construction nor lookup creates or adopts database objects.
func Migrations() []migrations.Migration {
	return []migrations.Migration{{App: "gogo_redirects", Name: "0001_initial", Dependencies: []string{"gogo_sites.0001_initial"}, Operations: []migrations.Operation{migrations.CreateModel((&Redirect{}).Schema())}}}
}

func redirectID(value string) (string, error) {
	if len(value) != 36 || value[8] != '-' || value[13] != '-' || value[18] != '-' || value[23] != '-' {
		return "", ErrInvalid
	}
	nonzero := false
	for i := 0; i < len(value); i++ {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			continue
		}
		c := value[i]
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F') {
			return "", ErrInvalid
		}
		nonzero = nonzero || c != '0'
	}
	if !nonzero {
		return "", ErrInvalid
	}
	return strings.ToLower(value), nil
}
