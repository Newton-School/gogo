package sites

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/Newton-School/gogo/checks"
	"github.com/Newton-School/gogo/models"
)

var ErrSiteNotFound = errors.New("site not found")

type Site struct {
	ID     int
	Domain string
	Name   string
}

type Store interface {
	ByID(int) (Site, bool)
	ByDomain(string) (Site, bool)
	All() []Site
}

type MemoryStore struct {
	sites []Site
}

func NewMemoryStore(sites []Site) *MemoryStore {
	return &MemoryStore{sites: append([]Site(nil), sites...)}
}

func (s *MemoryStore) ByID(id int) (Site, bool) {
	for _, site := range s.sites {
		if site.ID == id {
			return site, true
		}
	}
	return Site{}, false
}

func (s *MemoryStore) ByDomain(domain string) (Site, bool) {
	domain = strings.ToLower(stripHostPort(domain))
	for _, site := range s.sites {
		if strings.EqualFold(site.Domain, domain) {
			return site, true
		}
	}
	return Site{}, false
}

func (s *MemoryStore) All() []Site {
	return append([]Site(nil), s.sites...)
}

func CurrentSite(_ context.Context, r *http.Request, store Store, settings Settings) (Site, error) {
	if store == nil {
		return Site{}, ErrSiteNotFound
	}
	if settings.SiteID != 0 {
		if site, ok := store.ByID(settings.SiteID); ok {
			return site, nil
		}
		return Site{}, ErrSiteNotFound
	}
	if r != nil {
		if site, ok := store.ByDomain(r.Host); ok {
			return site, nil
		}
	}
	return Site{}, ErrSiteNotFound
}

func Metadata() models.Metadata {
	return models.Metadata{AppLabel: "sites", ModelName: "Site", TableName: "sites_site", Fields: []models.FieldMeta{{Name: "domain"}, {Name: "name"}}}
}

func Checks(store Store, settings Settings) []checks.Result {
	var results []checks.Result
	seen := map[string]bool{}
	if store != nil {
		for _, site := range store.All() {
			key := strings.ToLower(site.Domain)
			if seen[key] {
				results = append(results, checks.Result{ID: "sites.E001", Tags: []string{"sites"}, Severity: checks.SeverityError, Message: "duplicate site domain", Object: site.Domain})
			}
			seen[key] = true
		}
	}
	if settings.SiteID != 0 {
		if store == nil {
			results = append(results, checks.Result{ID: "sites.E002", Tags: []string{"sites"}, Severity: checks.SeverityError, Message: "configured SITE_ID does not exist"})
		} else if _, ok := store.ByID(settings.SiteID); !ok {
			results = append(results, checks.Result{ID: "sites.E002", Tags: []string{"sites"}, Severity: checks.SeverityError, Message: "configured SITE_ID does not exist"})
		}
	}
	return results
}

func stripHostPort(host string) string {
	if index := strings.Index(host, ":"); index >= 0 {
		return host[:index]
	}
	return host
}
