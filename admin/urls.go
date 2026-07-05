package admin

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"

	"github.com/cybersaksham/gogo/auth"
	"github.com/cybersaksham/gogo/forms"
	gogohttp "github.com/cybersaksham/gogo/http"
	"github.com/cybersaksham/gogo/messages"
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
		if request.Method() == http.MethodPost {
			if response, handled := adminChangeListAction(ctx, site, modelAdmin, request.Raw(), user); handled {
				return response
			}
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
				response, err := AdminFormProcessor{Site: site, ModelAdmin: modelAdmin, Mode: ChangeFormAdd}.Process(ctx, AdminFormProcessInput{
					Request: request.Raw(),
					User:    user,
					Values:  values,
				})
				if err != nil {
					return adminFormProcessError(err)
				}
				return response
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
					response, err := AdminFormProcessor{Site: site, ModelAdmin: modelAdmin, Mode: ChangeFormEdit}.Process(ctx, AdminFormProcessInput{
						Request:  request.Raw(),
						User:     user,
						ObjectID: objectID,
						Existing: object,
						Values:   values,
					})
					if err != nil {
						return adminFormProcessError(err)
					}
					return response
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
		data.Form = changeFormViewData(site, modelAdmin, formContext)
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
		page, err := adminAutocompletePage(request.Raw())
		if err != nil {
			return gogohttp.BadRequest("Bad Request", ErrInvalidChangeListQuery)
		}
		pageSize := modelAdmin.Normalize().ListPerPage
		if pageSize <= 0 || pageSize > 100 {
			pageSize = 20
		}
		searchFields := autocompleteSearchFields(modelAdmin)
		if err := validateAdminSearchFields(modelAdmin.Model, searchFields); err != nil {
			return gogohttp.BadRequest("Bad Request", err)
		}
		filters, err := adminAutocompleteForwardedFilters(modelAdmin.Model, request.Raw().URL.Query())
		if err != nil {
			return gogohttp.BadRequest("Bad Request", err)
		}
		toField, err := adminAutocompleteToField(modelAdmin.Model, request.Raw().URL.Query())
		if err != nil {
			return gogohttp.BadRequest("Bad Request", err)
		}
		if queryStore, ok := adminSiteOrDefault(site).ModelStore.(models.ObjectQueryStore); ok {
			query := models.ObjectQuery{
				Search:       autocompleteTerm(request.Raw()),
				SearchFields: searchFields,
				Filters:      filters,
				Limit:        pageSize + 1,
				Offset:       (page - 1) * pageSize,
				IncludeTotal: true,
			}
			result, err := queryStore.Query(ctx, modelAdmin.Model, query)
			if err != nil {
				return gogohttp.BadRequest("Bad Request", err)
			}
			rows := cloneRows(result.Rows)
			more := false
			if len(rows) > pageSize {
				more = true
				rows = rows[:pageSize]
			} else if result.Total > query.Offset+len(rows) {
				more = true
			}
			return gogohttp.JSON(http.StatusOK, autocompleteResponse(rows, toField, more))
		}
		rows, err := rowsForAdminChangeList(ctx, site, modelAdmin, request.Raw())
		if err != nil {
			return gogohttp.InternalServerError(err)
		}
		config := AutocompleteConfig{
			SearchFields:         searchFields,
			PageSize:             pageSize,
			Rows:                 rows,
			ForwardedConstraints: autocompleteForwardedConstraints(filters),
		}
		matched := autocompleteRows(config, request.Raw())
		start := (page - 1) * pageSize
		end := start + pageSize
		more := end < len(matched)
		if start < len(matched) {
			if end > len(matched) {
				end = len(matched)
			}
			matched = matched[start:end]
		} else {
			matched = nil
		}
		return gogohttp.JSON(http.StatusOK, autocompleteResponse(matched, toField, more))
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

func adminFormProcessError(err error) gogohttp.Response {
	if errors.Is(err, ErrAdminPermissionDenied) {
		return gogohttp.Forbidden("Forbidden", err)
	}
	if errors.Is(err, forms.ErrValidation) {
		return gogohttp.BadRequest("Bad Request", err)
	}
	return gogohttp.BadRequest("Bad Request", err)
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

func adminChangeListAction(ctx context.Context, site *Site, modelAdmin ModelAdmin, request *http.Request, user auth.User) (gogohttp.Response, bool) {
	if err := request.ParseForm(); err != nil {
		return gogohttp.BadRequest("Bad Request", err), true
	}
	actionName := request.PostFormValue("action")
	if actionName == "" {
		return gogohttp.Response{}, false
	}
	action, ok := findAdminAction(adminActionsForRequest(modelAdmin, request, user), actionName)
	if !ok {
		return gogohttp.BadRequest("Bad Request", fmt.Errorf("%w: unknown action %s", ErrInvalidChangeListQuery, actionName)), true
	}
	selectedIDs := request.PostForm["_selected_action"]
	if len(selectedIDs) == 0 {
		return gogohttp.BadRequest("Bad Request", fmt.Errorf("%w: no selected objects", ErrInvalidChangeListQuery)), true
	}
	selectedRows, err := selectedAdminActionRows(ctx, site, modelAdmin, request, selectedIDs)
	if err != nil {
		return gogohttp.InternalServerError(err), true
	}
	result, err := ExecuteAction(action, ActionContext{
		User:      user,
		Selected:  selectedRows,
		Store:     modelObjectActionStore{ctx: ctx, store: adminSiteOrDefault(site).ModelStore, meta: modelAdmin.Model},
		Confirmed: request.PostFormValue("post") == "yes",
	})
	if err != nil {
		return gogohttp.Forbidden("Forbidden", err), true
	}
	if result.ConfirmationRequired {
		return renderDeleteSelectedConfirmation(site, modelAdmin, request, selectedRows), true
	}
	if result.Message != "" {
		messages.Add(request.Context(), messages.LevelSuccess, result.Message)
		if err := logSelectedAdminAction(site, user, modelAdmin.Model, selectedRows, result.Message); err != nil {
			return gogohttp.InternalServerError(err), true
		}
	}
	return gogohttp.TemporaryRedirect(adminModelURL(adminSiteOrDefault(site), modelAdmin)), true
}

func findAdminAction(actions []Action, name string) (Action, bool) {
	for _, action := range actions {
		if action.Name == name {
			return action, true
		}
	}
	return Action{}, false
}

func selectedAdminActionRows(ctx context.Context, site *Site, modelAdmin ModelAdmin, request *http.Request, selectedIDs []string) ([]map[string]any, error) {
	site = adminSiteOrDefault(site)
	rows := make([]map[string]any, 0, len(selectedIDs))
	if site.ModelStore != nil {
		for _, objectID := range selectedIDs {
			row, exists, err := site.ModelStore.Get(ctx, modelAdmin.Model, objectID)
			if err != nil {
				return nil, err
			}
			if exists {
				rows = append(rows, row)
			}
		}
		return rows, nil
	}
	allRows := rowsFromModelAdmin(modelAdmin, request)
	selected := setFromSlice(selectedIDs)
	for _, row := range allRows {
		if _, ok := selected[objectIDFromRow(row)]; ok {
			rows = append(rows, row)
		}
	}
	return rows, nil
}

func renderDeleteSelectedConfirmation(site *Site, modelAdmin ModelAdmin, request *http.Request, selectedRows []map[string]any) gogohttp.Response {
	data := modelAdminPageData(site, request, modelAdmin, "Are you sure?", "Are you sure?", "delete-confirmation")
	objects := make([]DeletionObject, 0, len(selectedRows))
	for _, row := range selectedRows {
		objectID := objectIDFromRow(row)
		objects = append(objects, DeletionObject{
			Label:    data.ModelVerboseName,
			ObjectID: objectID,
			Repr:     rowDisplay(row, objectID),
		})
	}
	data.Deletion = CollectDeletion(objects)
	return renderAdminTemplate("delete_selected_confirmation.html", data)
}

type modelObjectActionStore struct {
	ctx   context.Context
	store ModelObjectStore
	meta  models.Metadata
}

func (s modelObjectActionStore) DeleteObjects(rows []map[string]any) error {
	if s.store == nil {
		return nil
	}
	for _, row := range rows {
		objectID := objectIDFromRow(row)
		if objectID == "" {
			return fmt.Errorf("%w: selected object is missing primary key", ErrInvalidChangeListQuery)
		}
		if err := s.store.Delete(s.ctx, s.meta, objectID); err != nil {
			return err
		}
	}
	return nil
}

func logSelectedAdminAction(site *Site, user auth.User, meta models.Metadata, selectedRows []map[string]any, message string) error {
	for _, row := range selectedRows {
		objectID := objectIDFromRow(row)
		if err := logAdminRow(site, user, meta, objectID, rowDisplay(row, objectID), ActionFlagAction, message); err != nil {
			return err
		}
	}
	return nil
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

func adminAutocompletePage(request *http.Request) (int, error) {
	values := request.URL.Query()
	pageValue := values.Get("page")
	if pageValue == "" {
		pageValue = values.Get("p")
	}
	page, err := strconv.Atoi(valueOrDefault(pageValue, "1"))
	if err != nil || page < 1 {
		return 0, fmt.Errorf("%w: invalid page %q", ErrInvalidChangeListQuery, pageValue)
	}
	return page, nil
}

func adminAutocompleteForwardedFilters(meta models.Metadata, values url.Values) (map[string][]string, error) {
	filters := map[string][]string{}
	fields := adminFieldMetaMap(meta)
	for key, raw := range values {
		if !strings.HasPrefix(key, "forward_") {
			continue
		}
		lookup := strings.TrimSpace(strings.TrimPrefix(key, "forward_"))
		if lookup == "" {
			return nil, fmt.Errorf("%w: empty forwarded lookup", ErrInvalidChangeListQuery)
		}
		if _, ok := fields[adminLookupRoot(lookup)]; !ok {
			return nil, fmt.Errorf("%w: unknown forwarded lookup %s", ErrInvalidChangeListQuery, lookup)
		}
		filters[lookup] = append([]string(nil), raw...)
	}
	return filters, nil
}

func adminAutocompleteToField(meta models.Metadata, values url.Values) (string, error) {
	toField := strings.TrimSpace(values.Get("to_field"))
	if toField == "" {
		toField = strings.TrimSpace(values.Get("_to_field"))
	}
	if toField == "" {
		return "", nil
	}
	if _, ok := adminFieldMetaMap(meta)[toField]; ok {
		return toField, nil
	}
	return "", fmt.Errorf("%w: unknown to_field %s", ErrInvalidChangeListQuery, toField)
}

func validateAdminSearchFields(meta models.Metadata, searchFields []string) error {
	fields := adminFieldMetaMap(meta)
	for _, raw := range searchFields {
		field := adminLookupRoot(raw)
		if field == "" || field == "__str__" {
			continue
		}
		if _, ok := fields[field]; !ok {
			return fmt.Errorf("%w: unknown search field %s", ErrInvalidChangeListQuery, field)
		}
	}
	return nil
}

func autocompleteTerm(request *http.Request) string {
	term := strings.TrimSpace(request.URL.Query().Get("q"))
	if term == "" {
		term = strings.TrimSpace(request.URL.Query().Get("term"))
	}
	return term
}

func autocompleteForwardedConstraints(filters map[string][]string) map[string]string {
	if len(filters) == 0 {
		return nil
	}
	constraints := map[string]string{}
	for lookup, values := range filters {
		if len(values) == 0 {
			continue
		}
		field := strings.TrimSuffix(lookup, "__exact")
		constraints[field] = values[0]
	}
	return constraints
}

func autocompleteResponse(rows []map[string]any, toField string, more bool) map[string]any {
	results := make([]AutocompleteResult, 0, len(rows))
	for _, row := range rows {
		objectID := objectIDFromRow(row)
		if toField != "" {
			objectID = fmt.Sprint(row[toField])
		}
		results = append(results, AutocompleteResult{ID: objectID, Text: rowDisplay(row, objectID)})
	}
	return map[string]any{
		"results": results,
		"pagination": map[string]bool{
			"more": more,
		},
	}
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
