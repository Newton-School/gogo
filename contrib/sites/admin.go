package sites

import "github.com/Newton-School/gogo/admin"

func Admin() admin.ModelAdmin {
	return admin.ModelAdmin{
		Model:          Metadata(),
		AllowUnmanaged: true,
		ListDisplay:    []string{"domain", "name"},
		SearchFields:   []string{"domain", "name"},
		Ordering:       []string{"domain"},
	}
}
