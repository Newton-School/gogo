package flatpages

import flatpagemigrations "github.com/Newton-School/gogo/contrib/flatpages/migrations"

func Migration() flatpagemigrations.MigrationInfo {
	return flatpagemigrations.Initial()
}
