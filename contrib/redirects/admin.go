package redirects

import "github.com/Newton-School/gogo/admin"

func Admin() admin.ModelAdmin {
	return admin.ModelAdmin{
		Model:          Metadata(),
		AllowUnmanaged: true,
		ListDisplay:    []string{"site", "old_path", "new_path", "permanent"},
		ListFilter:     []string{"site", "permanent"},
		SearchFields:   []string{"old_path", "new_path"},
	}
}
