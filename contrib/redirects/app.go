package redirects

import "github.com/Newton-School/gogo/app"

func AppConfig() app.Config {
	return app.BaseConfig{AppName: "gogo.contrib.redirects", AppLabel: "redirects", AppPath: "contrib/redirects", AppVerboseName: "Redirects"}
}
