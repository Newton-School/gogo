// Package sites resolves explicitly configured sites. A site is a content
// selector, not a tenant grant or a substitute for application authorization.
package sites

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/Newton-School/gogo/core/migrations"
	"github.com/Newton-School/gogo/core/models"
)

var ErrConfiguration = errors.New("sites: invalid configuration")
var ErrInvalidHost = errors.New("sites: invalid or disallowed host")
var ErrSiteNotConfigured = errors.New("sites: site is not configured")
var ErrUnavailable = errors.New("sites: lookup unavailable")

const DomainConstraint = "gogo_sites_domain"

type Site struct {
	models.Base
	ID, Domain, DisplayName string
	Active                  bool
}

func (*Site) Schema() models.Schema {
	return models.Schema{AppLabel: "gogo_sites", Name: "Site", Table: "gogo_sites", Fields: []models.Field{
		models.UUIDField("id", models.WithStructField("ID"), models.Primary, models.ReadOnly),
		models.CharField("domain", models.WithStructField("Domain"), models.WithMaxLength(253)),
		models.CharField("display_name", models.WithStructField("DisplayName"), models.WithMaxLength(255)),
		models.BooleanField("active", models.WithStructField("Active"), models.WithDefault(true)),
	}, Constraints: []models.Constraint{{Name: DomainConstraint, Kind: "unique", Fields: []string{"domain"}}}, Ordering: []string{"domain"}}
}

// NewSite allocates a UUID and canonicalizes domain without database access.
// Persistence and mutation authority remain explicitly application-owned.
func NewSite(domain, displayName string) (*Site, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return nil, ErrUnavailable
	}
	raw[6], raw[8] = raw[6]&0x0f|0x40, raw[8]&0x3f|0x80
	encoded := hex.EncodeToString(raw[:])
	site := &Site{ID: encoded[:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:], Domain: domain, DisplayName: displayName, Active: true}
	site.ModelState().Provided = map[string]bool{"active": true}
	if err := site.Clean(context.Background()); err != nil {
		return nil, err
	}
	return site, nil
}

// Clean participates in explicit FullClean/ModelForm validation. Like Django,
// ordinary ORM Save does not call Clean; use Prepare for a final save boundary.
func (s *Site) Clean(ctx context.Context) error {
	if ctx == nil || s == nil {
		return ErrConfiguration
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	id, err := canonicalID(s.ID)
	if err != nil {
		return models.Invalid("invalid", "Enter a valid site UUID.")
	}
	domain, err := NormalizeDomain(s.Domain)
	if err != nil {
		return models.Invalid("invalid", "Enter a valid ASCII site hostname.")
	}
	if !validName(s.DisplayName) {
		return models.Invalid("invalid", "Enter a site name of at most 255 characters.")
	}
	s.ID, s.Domain = id, domain
	return nil
}

// Prepare is a normalization/validation hook for orm.SaveOptions.Prepare.
// It adds no authorization or transaction. Bulk/raw application writes remain
// privileged and must preserve the canonical domain invariant themselves.
func Prepare(ctx context.Context, record models.Record) error {
	model, ok := models.Underlying(record)
	if !ok {
		return ErrConfiguration
	}
	site, ok := model.(*Site)
	if !ok {
		return ErrConfiguration
	}
	return site.Clean(ctx)
}

func Migrations() []migrations.Migration {
	return []migrations.Migration{{App: "gogo_sites", Name: "0001_initial", Operations: []migrations.Operation{migrations.CreateModel((&Site{}).Schema())}}}
}

func canonicalID(value string) (string, error) {
	if len(value) != 36 || value[8] != '-' || value[13] != '-' || value[18] != '-' || value[23] != '-' {
		return "", ErrSiteNotConfigured
	}
	var nonzero bool
	for i, c := range []byte(value) {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			continue
		}
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F') {
			return "", ErrSiteNotConfigured
		}
		nonzero = nonzero || c != '0'
	}
	if !nonzero {
		return "", ErrSiteNotConfigured
	}
	return strings.ToLower(value), nil
}

func validName(value string) bool {
	if len(value) < 1 || len(value) > 4*255 || !utf8.ValidString(value) || utf8.RuneCountInString(value) > 255 {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}
