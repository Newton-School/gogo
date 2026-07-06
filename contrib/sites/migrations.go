package sites

import sitemigrations "github.com/Newton-School/gogo/contrib/sites/migrations"

func Migration() sitemigrations.MigrationInfo {
	return sitemigrations.Initial()
}
