package admin

import (
	"strings"

	"github.com/Newton-School/gogo/core/templates"
)

// Group only already-authorized navigation rows. The original flat navigation
// and models contexts remain available to application-owned templates.
func groupNavigation(rows []any) []templates.Context {
	groups := []templates.Context{}
	positions := map[string]int{}
	for _, value := range rows {
		row := value.(templates.Context)
		app := row["app"].(string)
		position, exists := positions[app]
		if !exists {
			position = len(groups)
			positions[app] = position
			groups = append(groups, templates.Context{
				"label": strings.ReplaceAll(app, "_", " "), "models": []any{},
			})
		}
		groups[position]["models"] = append(groups[position]["models"].([]any), row)
	}
	return groups
}
