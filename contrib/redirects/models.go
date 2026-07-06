package redirects

import (
	"context"
	"net/http"

	"github.com/Newton-School/gogo/contrib/sites"
	"github.com/Newton-School/gogo/models"
)

type Redirect struct {
	ID        int
	SiteID    int
	OldPath   string
	NewPath   string
	Permanent bool
}

type Store interface {
	Find(siteID int, oldPath string) (Redirect, bool)
}

type MemoryStore struct {
	redirects []Redirect
}

func NewMemoryStore(redirects []Redirect) *MemoryStore {
	return &MemoryStore{redirects: append([]Redirect(nil), redirects...)}
}

func (s *MemoryStore) Find(siteID int, oldPath string) (Redirect, bool) {
	for _, redirect := range s.redirects {
		if redirect.SiteID == siteID && redirect.OldPath == oldPath {
			return redirect, true
		}
	}
	return Redirect{}, false
}

func CurrentRedirect(ctx context.Context, r *http.Request, store Store, siteStore sites.Store) (Redirect, bool) {
	if store == nil || r == nil {
		return Redirect{}, false
	}
	site, err := sites.CurrentSite(ctx, r, siteStore, sites.Settings{})
	if err != nil {
		return Redirect{}, false
	}
	return store.Find(site.ID, r.URL.Path)
}

func Metadata() models.Metadata {
	return models.Metadata{AppLabel: "redirects", ModelName: "Redirect", TableName: "redirects_redirect", Fields: []models.FieldMeta{{Name: "site"}, {Name: "old_path"}, {Name: "new_path"}}}
}
