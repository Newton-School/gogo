package flatpages

import "github.com/Newton-School/gogo/models"

type FlatPage struct {
	ID                   int
	URL                  string
	Title                string
	Content              string
	EnableComments       bool
	TemplateName         string
	RegistrationRequired bool
	SiteIDs              []int
}

type Store interface {
	Find(path string, siteID int) (FlatPage, bool)
}

type MemoryStore struct {
	pages []FlatPage
}

func NewMemoryStore(pages []FlatPage) *MemoryStore {
	return &MemoryStore{pages: append([]FlatPage(nil), pages...)}
}

func (s *MemoryStore) Find(path string, siteID int) (FlatPage, bool) {
	for _, page := range s.pages {
		if page.URL != path || !pageOnSite(page, siteID) {
			continue
		}
		return page, true
	}
	return FlatPage{}, false
}

func Metadata() models.Metadata {
	return models.Metadata{AppLabel: "flatpages", ModelName: "FlatPage", TableName: "flatpages_flatpage", Fields: []models.FieldMeta{{Name: "url"}, {Name: "title"}, {Name: "content"}}}
}

func pageOnSite(page FlatPage, siteID int) bool {
	for _, id := range page.SiteIDs {
		if id == siteID {
			return true
		}
	}
	return false
}
