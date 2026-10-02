package config

import (
	"example.com/gogo-services/apps/catalog"
	"github.com/Newton-School/gogo/core/app"
)

func InstalledApps() []app.Config { return []app.Config{catalog.App()} }
