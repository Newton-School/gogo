package flatpages

import "github.com/Newton-School/gogo/app"

func AppConfig() app.Config {
	return app.BaseConfig{AppName: "gogo.contrib.flatpages", AppLabel: "flatpages", AppPath: "contrib/flatpages", AppVerboseName: "Flat pages"}
}
