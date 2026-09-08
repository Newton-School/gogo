package postgres

import (
	"strings"

	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/models"
)

func catalogMappingIssue(column db.CatalogColumn) string {
	f := column.Field
	if f.Kind == models.Generated && f.Element != nil {
		f = *f.Element
	}
	switch f.Kind {
	case models.Custom:
		return "native type requires an explicitly registered custom codec"
	case models.Array:
		return "native array values require an explicitly registered array codec for generated models"
	case models.Duration:
		return "native interval months and days cannot be represented by a Go time.Duration"
	case models.GenericIPAddress:
		return "native inet network prefixes require an explicit address codec"
	case models.Decimal:
		if f.MaxDigits < 1 || f.DecimalPlaces < 0 || f.DecimalPlaces > f.MaxDigits {
			return "native numeric precision or scale is not representable by DecimalField"
		}
	case models.Time:
		return "native time permits values and precision not represented by the default Go calendar-time model"
	case models.DateTime:
		if column.TypeName == "timestamp" {
			return "timestamp without time zone needs an explicit wall-time codec"
		}
		if strings.Contains(column.DatabaseType, "(") {
			return "native temporal precision needs an explicit precision-preserving codec"
		}
	}
	if column.Generated == "v" {
		return "virtual generated columns are not supported by the portable generated-field descriptor"
	}
	return ""
}
