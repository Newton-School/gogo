package redirects

import redirectmigrations "github.com/Newton-School/gogo/contrib/redirects/migrations"

func Migration() redirectmigrations.MigrationInfo {
	return redirectmigrations.Initial()
}
