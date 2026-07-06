package sites

import "github.com/Newton-School/gogo/app"

func AppConfig() app.Config {
	return app.BaseConfig{
		AppName:        "gogo.contrib.sites",
		AppLabel:       "sites",
		AppPath:        "contrib/sites",
		AppVerboseName: "Sites",
	}
}

type Settings struct {
	SiteID int
}
