package admin

import (
	"context"
	"net/http"
	"strings"

	"github.com/cybersaksham/gogo/auth"
	gogohttp "github.com/cybersaksham/gogo/http"
)

// IndexContext is the render-ready admin index data.
type IndexContext struct {
	Site *Site
	Apps []IndexApp
}

// IndexApp groups visible models by app label.
type IndexApp struct {
	AppLabel string
	Models   []IndexModel
}

// IndexModel describes one visible model entry.
type IndexModel struct {
	AppLabel  string
	Name      string
	AddURL    string
	ChangeURL string
}

// LowerName returns the Django admin CSS object name.
func (m IndexModel) LowerName() string {
	return strings.ToLower(m.Name)
}

// RowID returns the Django admin app-model row identifier used by ARIA links.
func (m IndexModel) RowID() string {
	return strings.ToLower(m.AppLabel) + "-" + strings.ToLower(m.Name)
}

// BuildIndex builds the permission-filtered admin index.
func BuildIndex(site *Site, router *gogohttp.Router, user auth.User) (IndexContext, error) {
	return buildIndex(site, router, adminIndexRequest(site, user), user, "")
}

// BuildAppList builds a permission-filtered app list for one app label.
func BuildAppList(site *Site, router *gogohttp.Router, user auth.User, appLabel string) (IndexContext, error) {
	return buildIndex(site, router, adminIndexRequest(site, user), user, strings.ToLower(appLabel))
}

func buildIndex(site *Site, router *gogohttp.Router, request *http.Request, user auth.User, onlyApp string) (IndexContext, error) {
	if site == nil {
		site = DefaultSite()
	}
	if router == nil {
		var err error
		router, err = site.URLs()
		if err != nil {
			return IndexContext{}, err
		}
	}
	context := IndexContext{Site: site}
	appPositions := map[string]int{}
	for _, label := range site.ModelRegistry.RegisteredModels() {
		admin, ok := site.ModelRegistry.GetAdmin(label)
		if !ok {
			continue
		}
		appLabel := strings.ToLower(admin.Model.AppLabel)
		modelName := strings.ToLower(admin.Model.ModelName)
		if onlyApp != "" && appLabel != onlyApp {
			continue
		}
		if !canSeeIndexModel(admin, request, user) {
			continue
		}
		changeURL, err := router.Reverse(routeName(appLabel, modelName, "changelist"), nil)
		if err != nil {
			return IndexContext{}, err
		}
		addURL := ""
		if canAddIndexModel(admin, request, user) {
			addURL, err = router.Reverse(routeName(appLabel, modelName, "add"), nil)
			if err != nil {
				return IndexContext{}, err
			}
		}
		position, ok := appPositions[appLabel]
		if !ok {
			context.Apps = append(context.Apps, IndexApp{AppLabel: appLabel})
			position = len(context.Apps) - 1
			appPositions[appLabel] = position
		}
		context.Apps[position].Models = append(context.Apps[position].Models, IndexModel{
			AppLabel:  appLabel,
			Name:      admin.Model.ModelName,
			AddURL:    addURL,
			ChangeURL: changeURL,
		})
	}
	return context, nil
}

func adminIndexRequest(site *Site, user auth.User) *http.Request {
	prefix := "/admin/"
	if site != nil && site.URLPrefix != "" {
		prefix = strings.TrimRight(site.URLPrefix, "/") + "/"
	}
	request, err := http.NewRequestWithContext(context.Background(), http.MethodGet, prefix, nil)
	if err != nil {
		return nil
	}
	return request.WithContext(auth.ContextWithUser(request.Context(), user))
}

func canSeeIndexModel(admin ModelAdmin, request *http.Request, user auth.User) bool {
	appLabel := strings.ToLower(admin.Model.AppLabel)
	modelName := strings.ToLower(admin.Model.ModelName)
	if !admin.HasModulePermission(request, user) && !auth.HasModulePerms(user, appLabel) {
		return false
	}
	return admin.HasViewPermission(request, user) || admin.HasChangePermission(request, user) || canViewOrChange(user, appLabel, modelName)
}

func canAddIndexModel(admin ModelAdmin, request *http.Request, user auth.User) bool {
	appLabel := strings.ToLower(admin.Model.AppLabel)
	modelName := strings.ToLower(admin.Model.ModelName)
	return admin.HasAddPermission(request, user) || auth.HasPerm(user, appLabel+".add_"+modelName)
}

func canViewOrChange(user auth.User, appLabel, modelName string) bool {
	return auth.HasPerm(user, appLabel+".view_"+modelName) || auth.HasPerm(user, appLabel+".change_"+modelName)
}

func routeName(appLabel, modelName, action string) string {
	return "admin:" + appLabel + "_" + modelName + "_" + action
}
