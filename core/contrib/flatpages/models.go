package flatpages

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"strings"

	"github.com/Newton-School/gogo/core/migrations"
	"github.com/Newton-School/gogo/core/models"
)

const (
	SiteURLConstraint  = "gogo_flatpage_sites_site_url"
	PageSiteConstraint = "gogo_flatpage_sites_page_site"
)

// FlatPage is shared content. Use SaveDomain to change it and its complete site
// set together; ordinary ORM saves cannot synchronize denormalized mapping URLs.
type FlatPage struct {
	models.Base
	ID, URL, Title, Content, TemplateName string
	RegistrationRequired                  bool
}

func (*FlatPage) Schema() models.Schema {
	return models.Schema{AppLabel: "gogo_flatpages", Name: "FlatPage", Table: "gogo_flatpages", Fields: []models.Field{
		models.UUIDField("id", models.WithStructField("ID"), models.Primary, models.ReadOnly),
		models.CharField("url", models.WithStructField("URL"), models.WithMaxLength(MaxURLBytes)),
		models.CharField("title", models.WithStructField("Title"), models.WithMaxLength(MaxTitleRunes)),
		models.TextField("content", models.WithStructField("Content"), models.Optional, models.WithDefault("")),
		models.CharField("template_name", models.WithStructField("TemplateName"), models.WithMaxLength(MaxTemplateBytes), models.Optional, models.WithDefault("")),
		models.BooleanField("registration_required", models.WithStructField("RegistrationRequired"), models.WithDefault(false)),
		models.ManyToManyField("sites", models.Relation{Target: "gogo_sites.Site", Through: "gogo_flatpages.FlatPageSite", ThroughFields: []string{"page_id", "site_id"}, RelatedName: "flatpages"}),
	}, Ordering: []string{"url", "id"}}
}

// FlatPageSite reserves one URL within a site. Scalar foreign keys enforce
// endpoints and unique constraints enforce keys. URL equality with FlatPage is
// a SaveDomain invariant, not a composite database foreign-key guarantee.
type FlatPageSite struct {
	models.Base
	ID, PageID, SiteID, URL string
}

func (*FlatPageSite) Schema() models.Schema {
	return models.Schema{AppLabel: "gogo_flatpages", Name: "FlatPageSite", Table: "gogo_flatpage_sites", Fields: []models.Field{
		models.UUIDField("id", models.WithStructField("ID"), models.Primary, models.ReadOnly),
		models.ForeignKeyField("page_id", models.Relation{Target: "gogo_flatpages.FlatPage", OnDelete: models.Cascade, RelatedName: "site_mappings"}, models.WithStructField("PageID")),
		models.ForeignKeyField("site_id", models.Relation{Target: "gogo_sites.Site", OnDelete: models.Cascade, RelatedName: "flatpage_mappings"}, models.WithStructField("SiteID")),
		models.CharField("url", models.WithStructField("URL"), models.WithMaxLength(MaxURLBytes), models.ReadOnly),
	}, Constraints: []models.Constraint{
		{Name: SiteURLConstraint, Kind: "unique", Fields: []string{"site_id", "url"}},
		{Name: PageSiteConstraint, Kind: "unique", Fields: []string{"page_id", "site_id"}},
	}, Ordering: []string{"site_id", "id"}}
}

// Clean validates local fields only. It is not an authorization grant and
// cannot maintain the cross-model URL invariant without SaveDomain.
func (p *FlatPage) Clean(ctx context.Context) error {
	if p == nil {
		return ErrConfiguration
	}
	if err := contextError(ctx); err != nil {
		return err
	}
	draft, err := validateDraft(Draft{ID: p.ID, URL: p.URL, Title: p.Title, Content: p.Content, TemplateName: p.TemplateName, RegistrationRequired: p.RegistrationRequired})
	if err != nil {
		return err
	}
	id, err := pageID(draft.ID)
	if err != nil {
		return err
	}
	p.ID, p.URL, p.Title, p.Content, p.TemplateName, p.RegistrationRequired = id, draft.URL, draft.Title, draft.Content, draft.TemplateName, draft.RegistrationRequired
	return nil
}
func (p *FlatPageSite) Clean(ctx context.Context) error {
	if p == nil {
		return ErrConfiguration
	}
	if err := contextError(ctx); err != nil {
		return err
	}
	id, err := pageID(p.ID)
	if err != nil {
		return err
	}
	page, err := pageID(p.PageID)
	if err != nil {
		return err
	}
	site, err := pageID(p.SiteID)
	if err != nil {
		return err
	}
	path, err := normalizePath(p.URL)
	if err != nil {
		return err
	}
	p.ID, p.PageID, p.SiteID, p.URL = id, page, site, path
	return nil
}

func Migrations() []migrations.Migration {
	return []migrations.Migration{{App: "gogo_flatpages", Name: "0001_initial", Dependencies: []string{"gogo_sites.0001_initial"}, Operations: []migrations.Operation{migrations.CreateModel((&FlatPage{}).Schema()), migrations.CreateModel((&FlatPageSite{}).Schema())}}}
}

func newPageID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", ErrUnavailable
	}
	raw[6], raw[8] = raw[6]&0x0f|0x40, raw[8]&0x3f|0x80
	v := hex.EncodeToString(raw[:])
	return v[:8] + "-" + v[8:12] + "-" + v[12:16] + "-" + v[16:20] + "-" + v[20:], nil
}
func pageID(value string) (string, error) {
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
func draftInfo(p Draft) Info {
	return Info{ID: p.ID, URL: p.URL, Title: p.Title, Content: p.Content, TemplateName: p.TemplateName, RegistrationRequired: p.RegistrationRequired}
}
func infoDraft(p Info) Draft {
	return Draft{ID: p.ID, URL: p.URL, Title: p.Title, Content: p.Content, TemplateName: p.TemplateName, RegistrationRequired: p.RegistrationRequired}
}
func infoModel(p Info) *FlatPage {
	return &FlatPage{ID: p.ID, URL: p.URL, Title: p.Title, Content: p.Content, TemplateName: p.TemplateName, RegistrationRequired: p.RegistrationRequired}
}
