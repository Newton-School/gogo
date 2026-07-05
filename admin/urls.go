package admin

import (
	"context"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"

	"github.com/cybersaksham/gogo/auth"
	gogohttp "github.com/cybersaksham/gogo/http"
	"github.com/cybersaksham/gogo/models"
)

// URLs builds the namespaced admin router for this site.
func (s *Site) URLs() (*gogohttp.Router, error) {
	router := gogohttp.NewRouter()
	routes := []struct {
		name    string
		pattern string
		view    gogohttp.View
		methods []string
	}{
		{"admin:index_slash_redirect", s.URLPrefix, adminSlashRedirectView(s.URLPrefix + "/"), []string{"GET"}},
		{"admin:index", s.URLPrefix + "/", protectedAdminView(s, adminIndexView(s)), []string{"GET", "POST"}},
		{"admin:login", s.URLPrefix + "/login/", gogohttp.FromHandler(s.LoginView), []string{"GET", "POST"}},
		{"admin:logout", s.URLPrefix + "/logout/", gogohttp.FromHandler(s.LogoutView), []string{"GET", "POST"}},
		{"admin:password_change", s.URLPrefix + "/password_change/", protectedAdminView(s, gogohttp.FromHandler(s.PasswordChangeView)), []string{"GET", "POST"}},
		{"admin:jsi18n", s.URLPrefix + "/jsi18n/", protectedAdminView(s, adminJSI18NView()), []string{"GET"}},
		{"admin:css", s.URLPrefix + "/static/admin.css", adminAssetView("static/admin.css", "text/css; charset=utf-8"), []string{"GET"}},
		{"admin:js", s.URLPrefix + "/static/admin.js", adminAssetView("static/admin.js", "application/javascript; charset=utf-8"), []string{"GET"}},
		{"admin:static", s.URLPrefix + "/static/<path:asset_path>", adminStaticAssetView(), []string{"GET"}},
		{"admin:app_list", s.URLPrefix + "/<str:app_label>/", protectedAdminView(s, adminAppListView(s)), []string{"GET", "POST"}},
	}
	for _, route := range routes {
		if err := router.Handle(route.name, route.pattern, route.view, route.methods...); err != nil {
			return nil, err
		}
	}

	for _, label := range s.ModelRegistry.RegisteredModels() {
		modelAdmin, ok := s.ModelRegistry.GetAdmin(label)
		if !ok {
			continue
		}
		if err := registerModelURLs(router, s, modelAdmin); err != nil {
			return nil, err
		}
	}
	return router, nil
}

func registerModelURLs(router *gogohttp.Router, site *Site, admin ModelAdmin) error {
	appLabel := strings.ToLower(admin.Model.AppLabel)
	modelName := strings.ToLower(admin.Model.ModelName)
	prefix := site.URLPrefix + "/" + appLabel + "/" + modelName
	namePrefix := "admin:" + appLabel + "_" + modelName
	routes := []struct {
		name    string
		pattern string
		view    gogohttp.View
	}{
		{namePrefix + "_changelist", prefix + "/", adminChangeListView(site, admin)},
		{namePrefix + "_add", prefix + "/add/", adminChangeFormView(site, admin, ChangeFormAdd)},
		{namePrefix + "_change", prefix + "/<path:object_id>/change/", adminChangeFormView(site, admin, ChangeFormEdit)},
		{namePrefix + "_delete", prefix + "/<path:object_id>/delete/", adminDeleteView(site, admin)},
		{namePrefix + "_history", prefix + "/<path:object_id>/history/", adminHistoryView(site, admin)},
		{namePrefix + "_autocomplete", prefix + "/autocomplete/", adminAutocompleteView(site, admin)},
		{namePrefix + "_jsi18n", prefix + "/jsi18n/", adminJSI18NView()},
	}
	for _, route := range routes {
		if err := router.Handle(route.name, route.pattern, protectedAdminView(site, route.view), "GET", "POST"); err != nil {
			return err
		}
	}
	for _, custom := range admin.GetURLs(nil) {
		pattern := prefix + "/" + strings.TrimLeft(custom.Path, "/")
		if !strings.HasSuffix(pattern, "/") {
			pattern += "/"
		}
		view := placeholderView(custom.Name)
		if custom.Handler != nil {
			view = gogohttp.FromHandler(custom.Handler)
		}
		if err := router.Handle(namePrefix+"_"+custom.Name, pattern, protectedAdminView(site, view), "GET", "POST"); err != nil {
			return err
		}
	}
	return nil
}

func adminSlashRedirectView(location string) gogohttp.View {
	return func(_ context.Context, request *gogohttp.Request) gogohttp.Response {
		target := location
		if query := request.Raw().URL.RawQuery; query != "" {
			target += "?" + query
		}
		return gogohttp.PermanentRedirect(target)
	}
}

func protectedAdminView(site *Site, view gogohttp.View) gogohttp.View {
	return func(ctx context.Context, request *gogohttp.Request) gogohttp.Response {
		if site == nil {
			site = DefaultSite()
		}
		if site.PermissionPolicy == nil || site.PermissionPolicy.HasAccess(request.Raw()) {
			if err := validateAdminCSRF(request.Raw()); err != nil {
				return gogohttp.Text(http.StatusForbidden, csrfFailureMessage)
			}
			return view(ctx, request)
		}
		return adminAccessDenied(site, request.Raw())
	}
}

func adminAccessDenied(site *Site, request *http.Request) gogohttp.Response {
	if user, ok := auth.UserFromContext(request.Context()); ok && user.IsAuthenticated() && !user.IsAnonymous() {
		return gogohttp.Forbidden("Forbidden", nil)
	}
	if provider, ok := site.PermissionPolicy.(interface {
		UserForRequest(*http.Request) (auth.User, bool)
	}); ok {
		if user, ok := provider.UserForRequest(request); ok && user.IsAuthenticated() && !user.IsAnonymous() {
			return gogohttp.Forbidden("Forbidden", nil)
		}
	}
	next := request.URL.RequestURI()
	if next == "" {
		next = site.URLPrefix + "/"
	}
	return gogohttp.TemporaryRedirect(site.URLPrefix + "/login/?next=" + url.QueryEscape(next))
}

func placeholderView(name string) gogohttp.View {
	return func(context.Context, *gogohttp.Request) gogohttp.Response {
		return gogohttp.Text(200, name)
	}
}

func adminAssetView(name, contentType string) gogohttp.View {
	return func(context.Context, *gogohttp.Request) gogohttp.Response {
		body, ok := ReadAsset(name)
		if !ok {
			return gogohttp.NotFound("Not Found", nil)
		}
		return gogohttp.Stream(contentType, func(writer io.Writer) error {
			_, err := writer.Write(body)
			return err
		})
	}
}

func adminStaticAssetView() gogohttp.View {
	return func(_ context.Context, request *gogohttp.Request) gogohttp.Response {
		assetPath := strings.TrimLeft(request.PathParam("asset_path"), "/")
		if assetPath == "" || strings.Contains(assetPath, "\x00") || hasTraversalSegment(assetPath) {
			return gogohttp.NotFound("Not Found", nil)
		}
		cleaned := path.Clean(assetPath)
		if cleaned == "." || strings.HasPrefix(cleaned, "../") {
			return gogohttp.NotFound("Not Found", nil)
		}
		body, ok := ReadAsset("static/" + cleaned)
		if !ok {
			return gogohttp.NotFound("Not Found", nil)
		}
		contentType := mime.TypeByExtension(path.Ext(cleaned))
		if contentType == "" {
			contentType = "application/octet-stream"
		}
		return gogohttp.Stream(contentType, func(writer io.Writer) error {
			_, err := writer.Write(body)
			return err
		})
	}
}

func hasTraversalSegment(assetPath string) bool {
	for _, segment := range strings.Split(assetPath, "/") {
		if segment == ".." {
			return true
		}
	}
	return false
}

func adminIndexView(site *Site) gogohttp.View {
	return func(_ context.Context, request *gogohttp.Request) gogohttp.Response {
		site = adminSiteOrDefault(site)
		user, ok := adminRequestUser(site, request.Raw())
		if !ok {
			return gogohttp.Forbidden("Forbidden", nil)
		}
		index, err := buildIndex(site, nil, request.Raw(), user, "")
		if err != nil {
			return gogohttp.InternalServerError(err)
		}
		data := baseAdminPageData(site, request.Raw(), site.IndexTitle, site.IndexTitle, "dashboard")
		data.Apps = index.Apps
		data.Breadcrumbs = nil
		data.ContentClass = "colMS"
		data.ShowNavSidebar = false
		return renderAdminTemplate("index.html", data)
	}
}

func adminAppListView(site *Site) gogohttp.View {
	return func(_ context.Context, request *gogohttp.Request) gogohttp.Response {
		site = adminSiteOrDefault(site)
		user, ok := adminRequestUser(site, request.Raw())
		if !ok {
			return gogohttp.Forbidden("Forbidden", nil)
		}
		appLabel := strings.ToLower(request.PathParam("app_label"))
		index, err := buildIndex(site, nil, request.Raw(), user, appLabel)
		if err != nil {
			return gogohttp.InternalServerError(err)
		}
		data := baseAdminPageData(site, request.Raw(), appLabel, appLabel, "dashboard app-"+adminClassName(appLabel))
		data.Apps = index.Apps
		data.ContentClass = "colMS"
		data.ShowNavSidebar = false
		data.Breadcrumbs = append(data.Breadcrumbs, adminBreadcrumb{URL: site.URLPrefix + "/" + appLabel + "/", Label: appLabel})
		return renderAdminTemplate("index.html", data)
	}
}

func adminChangeListView(site *Site, modelAdmin ModelAdmin) gogohttp.View {
	return func(ctx context.Context, request *gogohttp.Request) gogohttp.Response {
		user, ok := adminRequestUser(site, request.Raw())
		if !ok || !(modelAdmin.HasViewPermission(request.Raw(), user) || modelAdmin.HasChangePermission(request.Raw(), user)) {
			return gogohttp.Forbidden("Forbidden", nil)
		}
		changeList, err := changeListForAdmin(ctx, site, modelAdmin, request.Raw())
		if err != nil {
			return gogohttp.BadRequest("Bad Request", err)
		}
		verboseName := modelVerboseName(modelAdmin)
		data := modelAdminPageData(site, request.Raw(), modelAdmin, "Select "+verboseName+" to change", "Select "+verboseName+" to change", "change-list")
		data.OmitContentClass = true
		data.Actions = adminActionsForRequest(modelAdmin, request.Raw(), user)
		changeList.BulkSelection = len(data.Actions) > 0
		data.ChangeList = changeList
		return renderAdminTemplate("change_list.html", data)
	}
}

func adminChangeFormView(site *Site, modelAdmin ModelAdmin, mode ChangeFormMode) gogohttp.View {
	return func(ctx context.Context, request *gogohttp.Request) gogohttp.Response {
		user, ok := adminRequestUser(site, request.Raw())
		if !ok {
			return gogohttp.Forbidden("Forbidden", nil)
		}
		objectID := request.PathParam("object_id")
		values := formValues(request.Raw())
		if site = adminSiteOrDefault(site); site.ModelStore != nil {
			switch {
			case mode == ChangeFormAdd && request.Method() == http.MethodPost:
				if !modelAdmin.HasAddPermission(request.Raw(), user) {
					return gogohttp.Forbidden("Forbidden", nil)
				}
				created, err := site.ModelStore.Create(ctx, modelAdmin.Model, values)
				if err != nil {
					return gogohttp.BadRequest("Bad Request", err)
				}
				if err := logAdminObject(site, user, modelAdmin.Model, created, ActionFlagAddition, "Added"); err != nil {
					return gogohttp.InternalServerError(err)
				}
				return adminSaveRedirect(site, modelAdmin, created, request.Raw())
			case mode == ChangeFormEdit:
				object, exists, err := site.ModelStore.Get(ctx, modelAdmin.Model, objectID)
				if err != nil {
					return gogohttp.InternalServerError(err)
				}
				if !exists {
					return gogohttp.NotFound("Not Found", nil)
				}
				if request.Method() == http.MethodPost {
					if !modelAdmin.HasChangePermission(request.Raw(), user) {
						return gogohttp.Forbidden("Forbidden", nil)
					}
					updated, err := site.ModelStore.Update(ctx, modelAdmin.Model, objectID, values, true)
					if err != nil {
						return gogohttp.BadRequest("Bad Request", err)
					}
					if err := logAdminObject(site, user, modelAdmin.Model, updated, ActionFlagChange, "Changed"); err != nil {
						return gogohttp.InternalServerError(err)
					}
					return adminSaveRedirect(site, modelAdmin, updated, request.Raw())
				}
				values = object
			}
		}
		formContext, err := BuildChangeForm(modelAdmin, ChangeFormInput{
			Mode:     mode,
			ObjectID: objectID,
			User:     user,
			Request:  request.Raw(),
			Values:   values,
		})
		if err != nil {
			return gogohttp.Forbidden("Forbidden", err)
		}
		action := "Add"
		if mode == ChangeFormEdit {
			action = "Change"
		}
		verboseName := modelVerboseName(modelAdmin)
		data := modelAdminPageData(site, request.Raw(), modelAdmin, action+" "+verboseName, action+" "+verboseName, "change-form")
		data.Form = changeFormViewData(modelAdmin, formContext)
		if objectID != "" {
			data.DeleteURL = data.ChangeListURL + objectID + "/delete/"
			data.HistoryURL = data.ChangeListURL + objectID + "/history/"
			data.Form.DeleteURL = data.DeleteURL
			data.Form.HistoryURL = data.HistoryURL
		}
		return renderAdminTemplate("change_form.html", data)
	}
}

func adminDeleteView(site *Site, modelAdmin ModelAdmin) gogohttp.View {
	return func(ctx context.Context, request *gogohttp.Request) gogohttp.Response {
		user, ok := adminRequestUser(site, request.Raw())
		if !ok || !modelAdmin.HasDeletePermission(request.Raw(), user) {
			return gogohttp.Forbidden("Forbidden", nil)
		}
		objectID := request.PathParam("object_id")
		objectRepr := objectID
		if site = adminSiteOrDefault(site); site.ModelStore != nil {
			object, exists, err := site.ModelStore.Get(ctx, modelAdmin.Model, objectID)
			if err != nil {
				return gogohttp.InternalServerError(err)
			}
			if !exists {
				return gogohttp.NotFound("Not Found", nil)
			}
			objectRepr = rowDisplay(object, objectID)
		}
		data := modelAdminPageData(site, request.Raw(), modelAdmin, "Delete "+modelVerboseName(modelAdmin), "Are you sure?", "delete-confirmation")
		data.Deletion = CollectDeletion([]DeletionObject{{
			Label:    data.ModelVerboseName,
			ObjectID: objectID,
			Repr:     objectRepr,
		}})
		if request.Method() == http.MethodPost {
			if err := ConfirmDeletion(data.Deletion); err != nil {
				return gogohttp.Conflict("Conflict", err)
			}
			if site.ModelStore != nil {
				if err := site.ModelStore.Delete(ctx, modelAdmin.Model, objectID); err != nil {
					return gogohttp.InternalServerError(err)
				}
			}
			if err := logAdminRow(site, user, modelAdmin.Model, objectID, objectRepr, ActionFlagDeletion, "Deleted"); err != nil {
				return gogohttp.InternalServerError(err)
			}
			return gogohttp.TemporaryRedirect(data.ChangeListURL)
		}
		return renderAdminTemplate("delete_confirmation.html", data)
	}
}

func adminHistoryView(site *Site, modelAdmin ModelAdmin) gogohttp.View {
	return func(ctx context.Context, request *gogohttp.Request) gogohttp.Response {
		user, ok := adminRequestUser(site, request.Raw())
		if !ok || !modelAdmin.HasViewPermission(request.Raw(), user) {
			return gogohttp.Forbidden("Forbidden", nil)
		}
		if site = adminSiteOrDefault(site); site.ModelStore != nil {
			_, exists, err := site.ModelStore.Get(ctx, modelAdmin.Model, request.PathParam("object_id"))
			if err != nil {
				return gogohttp.InternalServerError(err)
			}
			if !exists {
				return gogohttp.NotFound("Not Found", nil)
			}
		}
		data := modelAdminPageData(site, request.Raw(), modelAdmin, "History "+modelVerboseName(modelAdmin), "Object history", "history")
		data.History = BuildHistoryPage(site.LogStore, modelAdmin.Model.Label(), request.PathParam("object_id"))
		return renderAdminTemplate("history.html", data)
	}
}

func adminAutocompleteView(site *Site, modelAdmin ModelAdmin) gogohttp.View {
	return func(ctx context.Context, request *gogohttp.Request) gogohttp.Response {
		user, ok := adminRequestUser(site, request.Raw())
		if !ok || !(modelAdmin.HasViewPermission(request.Raw(), user) || modelAdmin.HasChangePermission(request.Raw(), user)) {
			return gogohttp.Forbidden("Forbidden", nil)
		}
		rows, err := rowsForAdminChangeList(ctx, site, modelAdmin, request.Raw())
		if err != nil {
			return gogohttp.InternalServerError(err)
		}
		page, err := strconv.Atoi(valueOrDefault(request.Raw().URL.Query().Get("page"), "1"))
		if err != nil || page < 1 {
			return gogohttp.BadRequest("Bad Request", ErrInvalidChangeListQuery)
		}
		pageSize := modelAdmin.Normalize().ListPerPage
		if pageSize <= 0 || pageSize > 100 {
			pageSize = 20
		}
		config := AutocompleteConfig{
			SearchFields: autocompleteSearchFields(modelAdmin),
			PageSize:     pageSize,
			Rows:         rows,
		}
		matched := autocompleteRows(config, request.Raw())
		start := (page - 1) * pageSize
		end := start + pageSize
		results := []AutocompleteResult{}
		if start < len(matched) {
			if end > len(matched) {
				end = len(matched)
			}
			for _, row := range matched[start:end] {
				objectID := objectIDFromRow(row)
				results = append(results, AutocompleteResult{ID: objectID, Text: rowDisplay(row, objectID)})
			}
		}
		return gogohttp.JSON(http.StatusOK, map[string]any{
			"results": results,
			"pagination": map[string]bool{
				"more": end < len(matched),
			},
		})
	}
}

func adminJSI18NView() gogohttp.View {
	return func(context.Context, *gogohttp.Request) gogohttp.Response {
		catalog := JavaScriptCatalog(map[string]string{
			"Add":    "Add",
			"Change": "Change",
			"Delete": "Delete",
			"Save":   "Save",
		})
		return gogohttp.Stream(catalog.ContentType+"; charset=utf-8", func(writer io.Writer) error {
			_, err := io.WriteString(writer, catalog.Body)
			return err
		})
	}
}

func formValues(request *http.Request) map[string]any {
	values := map[string]any{}
	if request.Method != http.MethodPost {
		return values
	}
	if err := request.ParseForm(); err != nil {
		return values
	}
	for key := range request.PostForm {
		values[key] = request.PostFormValue(key)
	}
	return values
}

func rowsForAdminChangeList(ctx context.Context, site *Site, modelAdmin ModelAdmin, request *http.Request) ([]map[string]any, error) {
	site = adminSiteOrDefault(site)
	if site.ModelStore != nil {
		return site.ModelStore.List(ctx, modelAdmin.Model)
	}
	return rowsFromModelAdmin(modelAdmin, request), nil
}

func changeListForAdmin(ctx context.Context, site *Site, modelAdmin ModelAdmin, request *http.Request) (ChangeList, error) {
	site = adminSiteOrDefault(site)
	if site.ModelStore != nil {
		if queryStore, ok := site.ModelStore.(models.ObjectQueryStore); ok {
			query, err := adminObjectQuery(modelAdmin, request)
			if err != nil {
				return ChangeList{}, err
			}
			result, err := queryStore.Query(ctx, modelAdmin.Model, query)
			if err != nil {
				return ChangeList{}, err
			}
			return BuildChangeListFromQueryResult(modelAdmin, result, request.URL.Query())
		}
	}
	rows, err := rowsForAdminChangeList(ctx, site, modelAdmin, request)
	if err != nil {
		return ChangeList{}, err
	}
	return BuildChangeList(modelAdmin, rows, request.URL.Query())
}

func adminObjectQuery(modelAdmin ModelAdmin, request *http.Request) (models.ObjectQuery, error) {
	values := request.URL.Query()
	options := modelAdmin.Normalize()
	page, err := pageNumber(values.Get("p"))
	if err != nil {
		return models.ObjectQuery{}, err
	}
	ordering, err := adminObjectOrdering(options, request, values.Get("o"))
	if err != nil {
		return models.ObjectQuery{}, err
	}
	limit := options.ListPerPage
	offset := (page - 1) * limit
	if values.Get("all") == "1" {
		limit = 0
		offset = 0
	}
	return models.ObjectQuery{
		Search:       strings.TrimSpace(values.Get("q")),
		SearchFields: append([]string(nil), options.SearchFields...),
		Filters:      adminObjectFilters(values),
		Ordering:     ordering,
		Limit:        limit,
		Offset:       offset,
		IncludeTotal: true,
	}, nil
}

func adminObjectOrdering(modelAdmin ModelAdmin, request *http.Request, raw string) ([]string, error) {
	if raw != "" {
		if err := sortRows(nil, modelAdmin, raw); err != nil {
			return nil, err
		}
		return []string{raw}, nil
	}
	return modelAdmin.GetOrdering(request), nil
}

func adminObjectFilters(values url.Values) map[string][]string {
	filters := map[string][]string{}
	for key, raw := range values {
		switch key {
		case "", "q", "o", "p", "all", "_popup", "csrfmiddlewaretoken", "action", "index", "_selected_action", "select_across":
			continue
		}
		if strings.HasPrefix(key, "_") {
			continue
		}
		filters[key] = append([]string(nil), raw...)
	}
	return filters
}

func adminActionsForRequest(modelAdmin ModelAdmin, request *http.Request, user auth.User) []Action {
	actions := make([]Action, 0, 1+len(modelAdmin.ActionDefinitions))
	if modelAdmin.HasDeletePermission(request, user) {
		actions = append(actions, DeleteSelectedAction())
	}
	for _, action := range modelAdmin.ActionDefinitions {
		if actionAllowed(action, user) {
			actions = append(actions, action)
		}
	}
	return actions
}

func autocompleteSearchFields(modelAdmin ModelAdmin) []string {
	if len(modelAdmin.SearchFields) > 0 {
		return append([]string(nil), modelAdmin.SearchFields...)
	}
	for _, field := range modelAdmin.ListDisplay {
		if field != "__str__" {
			return []string{field}
		}
	}
	for _, field := range modelAdmin.Model.Fields {
		if field.Name == "title" || field.Name == "name" || field.Name == "slug" {
			return []string{field.Name}
		}
	}
	return []string{"id"}
}

func logAdminObject(site *Site, user auth.User, meta models.Metadata, object map[string]any, action ActionFlag, message string) error {
	objectID := fmt.Sprint(objectPrimaryKey(meta, object))
	return logAdminRow(site, user, meta, objectID, rowDisplay(object, objectID), action, message)
}

func logAdminRow(site *Site, user auth.User, meta models.Metadata, objectID, objectRepr string, action ActionFlag, message string) error {
	if site == nil || site.LogStore == nil {
		return nil
	}
	return site.LogStore.Log(AdminLogEntry{
		UserID:        user.ID,
		ContentType:   meta.Label(),
		ObjectID:      objectID,
		ObjectRepr:    objectRepr,
		ActionFlag:    action,
		ChangeMessage: message,
	})
}

func adminSaveRedirect(site *Site, modelAdmin ModelAdmin, object map[string]any, request *http.Request) gogohttp.Response {
	site = adminSiteOrDefault(site)
	base := adminModelURL(site, modelAdmin)
	objectID := fmt.Sprint(objectPrimaryKey(modelAdmin.Model, object))
	if err := request.ParseForm(); err != nil {
		return gogohttp.BadRequest("Bad Request", err)
	}
	switch ResolveSaveIntent(request.PostForm) {
	case SaveIntentContinue:
		return gogohttp.TemporaryRedirect(base + objectID + "/change/")
	case SaveIntentAddAnother:
		return gogohttp.TemporaryRedirect(base + "add/")
	default:
		return gogohttp.TemporaryRedirect(base)
	}
}

func adminModelURL(site *Site, modelAdmin ModelAdmin) string {
	return site.URLPrefix + "/" + strings.ToLower(modelAdmin.Model.AppLabel) + "/" + strings.ToLower(modelAdmin.Model.ModelName) + "/"
}

func objectPrimaryKey(meta models.Metadata, object map[string]any) any {
	for _, field := range meta.Fields {
		if field.PrimaryKey {
			return object[field.Name]
		}
	}
	return object["id"]
}

func rowDisplay(row map[string]any, fallback string) string {
	for _, key := range []string{"name", "title", "slug", "id"} {
		if value := row[key]; value != nil && fmt.Sprint(value) != "" {
			return fmt.Sprint(value)
		}
	}
	return fallback
}

func groupedAdminModels(site *Site, onlyApp string) []IndexApp {
	appPositions := map[string]int{}
	var apps []IndexApp
	for _, label := range site.ModelRegistry.RegisteredModels() {
		modelAdmin, ok := site.ModelRegistry.GetAdmin(label)
		if !ok {
			continue
		}
		appLabel := strings.ToLower(modelAdmin.Model.AppLabel)
		modelName := strings.ToLower(modelAdmin.Model.ModelName)
		if onlyApp != "" && appLabel != onlyApp {
			continue
		}
		position, ok := appPositions[appLabel]
		if !ok {
			apps = append(apps, IndexApp{AppLabel: appLabel})
			position = len(apps) - 1
			appPositions[appLabel] = position
		}
		modelPath := site.URLPrefix + "/" + appLabel + "/" + modelName + "/"
		apps[position].Models = append(apps[position].Models, IndexModel{
			AppLabel:  appLabel,
			Name:      modelAdmin.Model.ModelName,
			AddURL:    modelPath + "add/",
			ChangeURL: modelPath,
		})
	}
	return apps
}
