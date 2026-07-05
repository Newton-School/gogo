package admin

import (
	"fmt"
	"html/template"
	"net/http"
	"strconv"
	"strings"
	"unicode"

	"github.com/cybersaksham/gogo/auth"
	gogohttp "github.com/cybersaksham/gogo/http"
)

type adminBreadcrumb struct {
	URL   string
	Label string
}

type adminSubmitButton struct {
	Name  string
	Value string
	Label string
	Class string
}

type adminListFilter struct {
	Name  string
	Label string
	Title string
}

type adminPageData struct {
	Site              *Site
	Title             string
	Header            string
	IndexTitle        string
	ContentTitle      string
	BodyClass         string
	UserName          string
	SiteURL           string
	LogoutURL         string
	PasswordChangeURL string
	ContentClass      string
	OmitContentClass  bool
	ShowNavSidebar    bool
	StaticCSSURL      string
	StaticCSSURLs     []string
	StaticHeadJSURLs  []string
	StaticDeferJSURLs []string
	StaticJSURL       string
	StaticJSURLs      []string
	CSRFToken         string
	Breadcrumbs       []adminBreadcrumb
	Apps              []IndexApp

	AppLabel               string
	ModelName              string
	ModelVerboseName       string
	ModelVerboseNamePlural string
	AddURL                 string
	ChangeListURL          string
	DeleteURL              string
	HistoryURL             string
	SearchQuery            string
	SearchHelpText         string
	ListFilters            []adminListFilter
	Actions                []Action
	ChangeList             ChangeList
	Form                   adminFormData
	Deletion               DeletionSummary
	History                HistoryPage

	Next  string
	Error string

	csrfCookie *http.Cookie
}

type adminFormData struct {
	ID          string
	Fieldsets   []adminFieldsetData
	SaveButtons []adminSubmitButton
	SaveOnTop   bool
	CanDelete   bool
	DeleteURL   string
	HistoryURL  string
	Inlines     []adminInlineFormsetData
}

type adminFieldsetData struct {
	Name   string
	Fields []adminFormFieldData
}

type adminFormFieldData struct {
	Name       string
	Label      string
	LabelClass string
	LabelFor   bool
	FieldID    string
	FieldCSS   string
	HelpID     string
	Readonly   bool
	Checkbox   bool
	Fieldset   bool
	Required   bool
	HelpText   string
	Errors     string
	WidgetHTML template.HTML
}

type adminHiddenInput struct {
	Name  string
	Value string
}

type adminInlineFormsetData struct {
	ModelName         string
	Prefix            string
	Kind              InlineKind
	Stacked           bool
	Tabular           bool
	VerboseNamePlural string
	FormsetData       string
	Management        []adminHiddenInput
	Forms             []adminInlineFormData
	Headers           []adminInlineHeaderData
	Rows              []adminInlineRowData
	CanDelete         bool
	Errors            []string
}

type adminInlineFormData struct {
	Index         int
	ID            string
	Fields        []adminFormFieldData
	HiddenInputs  []adminHiddenInput
	DeleteName    string
	DeleteID      string
	DeleteChecked bool
	NonFieldError string
}

type adminInlineHeaderData struct {
	Class string
	Label string
}

type adminInlineRowData struct {
	ID            string
	Original      bool
	OriginalLabel string
	Cells         []adminInlineCellData
	HiddenInputs  []adminHiddenInput
	DeleteName    string
	DeleteID      string
	DeleteChecked bool
	NonFieldError string
}

type adminInlineCellData struct {
	Field adminFormFieldData
}

func renderAdminTemplate(name string, data adminPageData) gogohttp.Response {
	site := adminSiteOrDefault(data.Site)
	data.Site = site
	rendered, err := RenderTemplate(name, data, site.TemplateDirs)
	if err != nil {
		return gogohttp.InternalServerError(err)
	}
	response := gogohttp.HTML(http.StatusOK, rendered)
	if data.csrfCookie != nil {
		response.Header().Add("Set-Cookie", data.csrfCookie.String())
	}
	return response
}

func baseAdminPageData(site *Site, request *http.Request, title, contentTitle, bodyClass string) adminPageData {
	site = adminSiteOrDefault(site)
	data := adminPageData{
		Site:              site,
		Title:             title + " | " + site.Title,
		Header:            site.Header,
		IndexTitle:        site.IndexTitle,
		ContentTitle:      contentTitle,
		BodyClass:         strings.TrimSpace(bodyClass),
		SiteURL:           site.URLPrefix + "/",
		LogoutURL:         site.URLPrefix + "/logout/",
		PasswordChangeURL: site.URLPrefix + "/password_change/",
		ContentClass:      "colM",
		ShowNavSidebar:    true,
		StaticCSSURL:      site.URLPrefix + "/static/admin.css",
		StaticCSSURLs:     adminCSSURLs(site.URLPrefix, bodyClass),
		StaticHeadJSURLs:  []string{site.URLPrefix + "/static/admin/js/theme.js"},
		StaticDeferJSURLs: adminDeferJSURLs(site.URLPrefix, bodyClass),
		StaticJSURL:       site.URLPrefix + "/static/admin.js",
		StaticJSURLs:      adminJSURLs(site.URLPrefix, bodyClass),
		Breadcrumbs:       []adminBreadcrumb{{URL: site.URLPrefix + "/", Label: "Home"}},
	}
	data.UserName = adminUserDisplayName(site, request)
	data.CSRFToken, data.csrfCookie = adminCSRFPageToken(request)
	return data
}

func modelAdminPageData(site *Site, request *http.Request, modelAdmin ModelAdmin, title, contentTitle, actionClass string) adminPageData {
	site = adminSiteOrDefault(site)
	appLabel := strings.ToLower(modelAdmin.Model.AppLabel)
	modelName := strings.ToLower(modelAdmin.Model.ModelName)
	modelURL := site.URLPrefix + "/" + appLabel + "/" + modelName + "/"
	verboseName := modelVerboseName(modelAdmin)
	verbosePlural := modelVerboseNamePlural(modelAdmin)
	data := baseAdminPageData(site, request, title, contentTitle, strings.Join([]string{
		"app-" + adminClassName(appLabel),
		"model-" + adminClassName(modelName),
		actionClass,
	}, " "))
	data.AppLabel = appLabel
	data.ModelName = modelAdmin.Model.ModelName
	data.ModelVerboseName = verboseName
	data.ModelVerboseNamePlural = verbosePlural
	data.AddURL = modelURL + "add/"
	data.ChangeListURL = modelURL
	data.SearchQuery = request.URL.Query().Get("q")
	data.SearchHelpText = modelAdmin.SearchHelpText
	data.ListFilters = listFilters(modelAdmin)
	data.Actions = append([]Action{DeleteSelectedAction()}, modelAdmin.ActionDefinitions...)
	data.Breadcrumbs = []adminBreadcrumb{
		{URL: site.URLPrefix + "/", Label: "Home"},
		{URL: site.URLPrefix + "/" + appLabel + "/", Label: appLabel},
		{URL: modelURL, Label: modelAdmin.Model.ModelName},
	}
	return data
}

func adminCSSURLs(prefix, bodyClass string) []string {
	bodyClass = " " + strings.TrimSpace(bodyClass) + " "
	urls := []string{
		prefix + "/static/admin/css/base.css",
		prefix + "/static/admin/css/dark_mode.css",
	}
	if !strings.Contains(bodyClass, " login ") {
		urls = append(urls, prefix+"/static/admin/css/nav_sidebar.css")
	}
	switch {
	case strings.Contains(bodyClass, " change-list "):
		urls = append(urls, prefix+"/static/admin/css/changelists.css")
	case strings.Contains(bodyClass, " change-form "):
		urls = append(urls, prefix+"/static/admin/css/forms.css")
	case strings.Contains(bodyClass, " login "):
		urls = append(urls, prefix+"/static/admin/css/login.css")
	case strings.Contains(bodyClass, " dashboard "):
		urls = append(urls, prefix+"/static/admin/css/dashboard.css")
	}
	urls = append(urls, prefix+"/static/admin/css/responsive.css")
	return urls
}

func adminDeferJSURLs(prefix, bodyClass string) []string {
	bodyClass = " " + strings.TrimSpace(bodyClass) + " "
	urls := []string{}
	if !strings.Contains(bodyClass, " login ") {
		urls = append(urls, prefix+"/static/admin/js/nav_sidebar.js")
	}
	if strings.Contains(bodyClass, " change-list ") {
		urls = append(urls, prefix+"/static/admin/js/filters.js")
	}
	return urls
}

func adminJSURLs(prefix, bodyClass string) []string {
	bodyClass = " " + strings.TrimSpace(bodyClass) + " "
	switch {
	case strings.Contains(bodyClass, " change-form "):
		return adminStaticJS(prefix, []string{
			"jsi18n/",
			"static/admin/js/vendor/jquery/jquery.js",
			"static/admin/js/calendar.js",
			"static/admin/js/jquery.init.js",
			"static/admin/js/admin/DateTimeShortcuts.js",
			"static/admin/js/core.js",
			"static/admin/js/admin/RelatedObjectLookups.js",
			"static/admin/js/SelectBox.js",
			"static/admin/js/actions.js",
			"static/admin/js/SelectFilter2.js",
			"static/admin/js/urlify.js",
			"static/admin/js/prepopulate.js",
			"static/admin/js/vendor/xregexp/xregexp.js",
		})
	case strings.Contains(bodyClass, " change-list "):
		return adminStaticJS(prefix, []string{
			"jsi18n/",
			"static/admin/js/vendor/jquery/jquery.js",
			"static/admin/js/jquery.init.js",
			"static/admin/js/core.js",
			"static/admin/js/admin/RelatedObjectLookups.js",
			"static/admin/js/actions.js",
			"static/admin/js/urlify.js",
			"static/admin/js/prepopulate.js",
			"static/admin/js/vendor/xregexp/xregexp.js",
		})
	default:
		return nil
	}
}

func adminStaticJS(prefix string, paths []string) []string {
	urls := make([]string, 0, len(paths))
	for _, value := range paths {
		if strings.HasPrefix(value, "jsi18n/") {
			urls = append(urls, prefix+"/"+value)
			continue
		}
		urls = append(urls, prefix+"/"+value)
	}
	return urls
}

func changeFormViewData(site *Site, modelAdmin ModelAdmin, context ChangeFormContext) adminFormData {
	form := adminFormData{
		ID:          strings.ToLower(modelAdmin.Model.ModelName) + "_form",
		SaveButtons: submitButtons(context.SaveButtons),
		SaveOnTop:   context.SaveOnTop,
		CanDelete:   context.CanDelete,
		DeleteURL:   context.DeleteURL,
	}
	if context.ObjectID != "" {
		form.HistoryURL = context.ObjectID + "/history/"
	}
	for _, fieldset := range context.Fieldsets {
		fieldsetData := adminFieldsetData{Name: fieldset.Name}
		for _, fieldName := range fieldset.Fields {
			field, ok := context.Fields[fieldName]
			if !ok {
				field = ChangeFormField{Name: fieldName, Widget: WidgetText}
			}
			field.Relation = adminRelationTargetForRequest(site, modelAdmin, fieldName, context.Request, context.User)
			fieldData := adminFormFieldData{
				Name:       fieldName,
				Label:      adminFieldLabel(modelAdmin, field),
				FieldID:    "id_" + fieldName,
				FieldCSS:   "form-row field-" + fieldName,
				LabelFor:   field.Widget != WidgetPasswordHash && !field.Readonly,
				Readonly:   field.Readonly,
				Checkbox:   field.Widget == WidgetCheckbox,
				Fieldset:   field.Widget == WidgetFilteredSelectMultiple || field.Widget == WidgetDateTime,
				Required:   adminFieldRequired(modelAdmin, field),
				HelpText:   adminFieldHelpText(modelAdmin, field),
				WidgetHTML: template.HTML(renderAdminFormWidget(site, modelAdmin, field)),
			}
			if fieldData.Required {
				fieldData.LabelClass = "required"
			}
			if fieldData.HelpText != "" {
				fieldData.HelpID = fieldData.FieldID + "_helptext"
			}
			fieldsetData.Fields = append(fieldsetData.Fields, adminFormFieldData{
				Name:       fieldData.Name,
				Label:      fieldData.Label,
				LabelClass: fieldData.LabelClass,
				LabelFor:   fieldData.LabelFor,
				FieldID:    fieldData.FieldID,
				FieldCSS:   fieldData.FieldCSS,
				HelpID:     fieldData.HelpID,
				Readonly:   fieldData.Readonly,
				Checkbox:   fieldData.Checkbox,
				Fieldset:   fieldData.Fieldset,
				Required:   fieldData.Required,
				HelpText:   fieldData.HelpText,
				WidgetHTML: fieldData.WidgetHTML,
			})
		}
		form.Fieldsets = append(form.Fieldsets, fieldsetData)
	}
	for _, inline := range context.Inlines {
		form.Inlines = append(form.Inlines, inlineFormsetViewData(site, modelAdmin, inline))
	}
	return form
}

func inlineFormsetViewData(site *Site, modelAdmin ModelAdmin, formset InlineFormset) adminInlineFormsetData {
	data := adminInlineFormsetData{
		ModelName:         formset.Model,
		Prefix:            formset.Prefix,
		Kind:              formset.Kind,
		Stacked:           formset.Kind != InlineTabular,
		Tabular:           formset.Kind == InlineTabular,
		VerboseNamePlural: inlineVerboseNamePlural(formset),
		FormsetData:       inlineFormsetDataAttribute(formset),
		CanDelete:         formset.CanDelete,
		Errors:            append([]string(nil), formset.Errors...),
	}
	forms := append([]InlineForm(nil), formset.Forms...)
	if !formset.Submitted {
		nextIndex := len(forms)
		for i := 0; i < formset.ExtraForms; i++ {
			forms = append(forms, InlineForm{Index: nextIndex + i, Values: map[string]any{}})
		}
	}
	data.Management = []adminHiddenInput{
		{Name: formset.Prefix + "-TOTAL_FORMS", Value: strconv.Itoa(len(forms))},
		{Name: formset.Prefix + "-INITIAL_FORMS", Value: strconv.Itoa(formset.InitialForms)},
		{Name: formset.Prefix + "-MIN_NUM_FORMS", Value: strconv.Itoa(formset.MinNum)},
		{Name: formset.Prefix + "-MAX_NUM_FORMS", Value: inlineMaxNumValue(formset.MaxNum)},
	}
	for _, field := range formset.Fields {
		data.Headers = append(data.Headers, adminInlineHeaderData{
			Class: "column-" + field,
			Label: inlineFieldLabel(formset, field),
		})
	}
	if formset.CanDelete {
		data.Headers = append(data.Headers, adminInlineHeaderData{Class: "delete", Label: "Delete?"})
	}
	for _, form := range forms {
		formData := inlineFormViewData(site, modelAdmin, formset, form)
		data.Forms = append(data.Forms, formData)
		row := adminInlineRowData{
			ID:            formData.ID,
			Original:      inlineFormIsOriginal(formset, form),
			OriginalLabel: inlineFormOriginalLabel(formset, form),
			HiddenInputs:  formData.HiddenInputs,
			DeleteName:    formData.DeleteName,
			DeleteID:      formData.DeleteID,
			DeleteChecked: formData.DeleteChecked,
			NonFieldError: formData.NonFieldError,
		}
		for _, field := range formData.Fields {
			row.Cells = append(row.Cells, adminInlineCellData{Field: field})
		}
		data.Rows = append(data.Rows, row)
	}
	return data
}

func inlineFormIsOriginal(formset InlineFormset, form InlineForm) bool {
	if form.Index >= formset.InitialForms {
		return false
	}
	pkName := primaryKeyName(formset.Meta)
	return pkName != "" && firstFormValue(form.Values[pkName]) != ""
}

func inlineFormOriginalLabel(formset InlineFormset, form InlineForm) string {
	pkName := primaryKeyName(formset.Meta)
	objectID := ""
	if pkName != "" {
		objectID = firstFormValue(form.Values[pkName])
	}
	return rowDisplay(form.Values, objectID)
}

func inlineFormViewData(site *Site, modelAdmin ModelAdmin, formset InlineFormset, form InlineForm) adminInlineFormData {
	index := form.Index
	if index < 0 {
		index = 0
	}
	data := adminInlineFormData{
		Index:         index,
		ID:            formset.Prefix + "-" + strconv.Itoa(index),
		DeleteName:    inlineFieldKey(formset.Prefix, index, "DELETE"),
		DeleteID:      "id_" + inlineFieldKey(formset.Prefix, index, "DELETE"),
		DeleteChecked: form.Delete,
		NonFieldError: form.Errors["__all__"],
	}
	if pkName := primaryKeyName(formset.Meta); pkName != "" {
		data.HiddenInputs = append(data.HiddenInputs, adminHiddenInput{
			Name:  inlineFieldKey(formset.Prefix, index, pkName),
			Value: firstFormValue(form.Values[pkName]),
		})
	}
	for _, fieldName := range formset.Fields {
		data.Fields = append(data.Fields, inlineFieldViewData(site, modelAdmin, formset, form, fieldName))
	}
	return data
}

func inlineFieldViewData(site *Site, modelAdmin ModelAdmin, formset InlineFormset, form InlineForm, fieldName string) adminFormFieldData {
	metaField := adminFieldMetaMap(formset.Meta)[fieldName]
	value := form.Values[fieldName]
	htmlName := inlineFieldKey(formset.Prefix, form.Index, fieldName)
	fieldID := "id_" + htmlName
	widget := widgetForField(formset.Meta.Label(), metaField, fieldName, value, map[string]struct{}{}, map[string]struct{}{}, map[string]struct{}{}, map[string]struct{}{}, map[string]struct{}{})
	field := ChangeFormField{
		Name:     htmlName,
		Widget:   widget,
		Value:    value,
		Label:    adminMetadataLabel(metaField),
		HelpText: metaField.HelpText,
		Required: adminMetadataRequired(metaField),
		Choices:  adminWidgetChoices(metaField.Choices),
		Meta:     metaField,
	}
	fieldData := adminFormFieldData{
		Name:       htmlName,
		Label:      inlineFieldLabel(formset, fieldName),
		FieldID:    fieldID,
		FieldCSS:   "form-row field-" + fieldName,
		LabelFor:   true,
		Checkbox:   widget == WidgetCheckbox,
		Fieldset:   widget == WidgetFilteredSelectMultiple || widget == WidgetDateTime,
		Required:   field.Required,
		HelpText:   metaField.HelpText,
		Errors:     form.Errors[fieldName],
		WidgetHTML: template.HTML(renderAdminInlineWidget(site, modelAdmin, field, fieldName, fieldID)),
	}
	if fieldData.Required {
		fieldData.LabelClass = "required"
	}
	if fieldData.HelpText != "" {
		fieldData.HelpID = fieldData.FieldID + "_helptext"
	}
	return fieldData
}

func renderAdminInlineWidget(site *Site, modelAdmin ModelAdmin, field ChangeFormField, originalName, fieldID string) string {
	value := field.Value
	if value == nil {
		value = ""
	}
	config := WidgetConfig{
		Name:    field.Name,
		Value:   value,
		Choices: append([]WidgetChoice(nil), field.Choices...),
		Attrs: map[string]string{
			"id":    fieldID,
			"class": "vTextField",
		},
		RelationURL: adminRelationAutocompleteURL(site, modelAdmin, originalName),
	}
	switch field.Widget {
	case WidgetCheckbox:
		config.Attrs = map[string]string{"id": fieldID}
		return Checkbox(config)
	case WidgetTextarea:
		return Textarea(config)
	case WidgetNumber:
		config.Attrs["class"] = "vIntegerField"
		return NumberInput(config)
	case WidgetSelect:
		return Select(config)
	case WidgetDate:
		return DateInput(config)
	case WidgetTime:
		return TimeInput(config)
	case WidgetDateTime:
		return DateTimeInput(config)
	case WidgetEmail:
		return EmailInput(config)
	case WidgetFile:
		config.Attrs = map[string]string{"id": fieldID}
		return ClearableFileInput(config)
	case WidgetAutocomplete:
		config.Attrs["class"] = "admin-autocomplete"
		return AutocompleteWidget(config)
	case WidgetRadio:
		return Select(config)
	default:
		return TextInput(config)
	}
}

func inlineVerboseNamePlural(formset InlineFormset) string {
	if formset.Meta.VerboseNamePlural != "" {
		return formset.Meta.VerboseNamePlural
	}
	if formset.Meta.ModelName == "" {
		return formset.Model
	}
	return adminLabel(formset.Meta.ModelName) + "s"
}

func inlineFieldLabel(formset InlineFormset, fieldName string) string {
	if metaField, ok := adminFieldMetaMap(formset.Meta)[fieldName]; ok {
		if label := adminMetadataLabel(metaField); label != "" {
			return label
		}
	}
	return adminLabel(fieldName)
}

func inlineFormsetDataAttribute(formset InlineFormset) string {
	return fmt.Sprintf(`{"name":"#%s-group","options":{"prefix":"%s","addText":"Add another","deleteText":"Remove"}}`, formset.Prefix, formset.Prefix)
}

func inlineMaxNumValue(maxNum int) string {
	if maxNum > 0 {
		return strconv.Itoa(maxNum)
	}
	return "1000"
}

func renderAdminFormWidget(site *Site, modelAdmin ModelAdmin, field ChangeFormField) string {
	value := field.Value
	if value == nil {
		value = ""
		if field.Readonly {
			value = "-"
		}
	}
	config := WidgetConfig{
		Name:    field.Name,
		Value:   value,
		Choices: append([]WidgetChoice(nil), field.Choices...),
		Attrs: map[string]string{
			"id":    "id_" + field.Name,
			"class": "vTextField",
		},
	}
	relation := field.Relation
	if relation.ModelName == "" && relation.AutocompleteURL == "" {
		relation = adminRelationTarget(site, modelAdmin, field.Name)
	}
	config.RelationURL = relation.AutocompleteURL
	switch field.Widget {
	case WidgetReadonly:
		return ReadonlyDisplay(config)
	case WidgetPasswordHash:
		return PasswordHashDisplay(config)
	case WidgetCheckbox:
		config.Attrs = map[string]string{"id": "id_" + field.Name}
		return Checkbox(config)
	case WidgetTextarea:
		return Textarea(config)
	case WidgetNumber:
		config.Attrs["class"] = "vIntegerField"
		return NumberInput(config)
	case WidgetSelect:
		return Select(config)
	case WidgetDate:
		return DateInput(config)
	case WidgetTime:
		return TimeInput(config)
	case WidgetDateTime:
		return DateTimeInput(config)
	case WidgetEmail:
		return EmailInput(config)
	case WidgetFile:
		config.Attrs = map[string]string{"id": "id_" + field.Name}
		return ClearableFileInput(config)
	case WidgetRawID:
		config.Attrs["class"] = "vForeignKeyRawIdAdminField"
		return RawIDRelationWidget(config)
	case WidgetAutocomplete:
		config.Attrs["class"] = "admin-autocomplete"
		return AutocompleteWidget(config)
	case WidgetRadio:
		return Select(config)
	case WidgetFilteredSelectMultiple:
		config.Attrs = map[string]string{
			"id":              "id_" + field.Name,
			"class":           "selectfilter",
			"data-context":    "available-source",
			"data-field-name": strings.ReplaceAll(field.Name, "_", " "),
			"data-is-stacked": "0",
		}
		widget := FilteredSelectMultiple(config)
		related := WidgetConfig{
			Name:                     field.Name,
			RelatedModelName:         relation.ModelName,
			RelatedModelLabel:        relation.ModelLabel,
			AddRelatedURL:            relation.AddURL,
			ChangeRelatedTemplateURL: relation.ChangeTemplateURL,
			DeleteRelatedTemplateURL: relation.DeleteTemplateURL,
			ViewRelatedTemplateURL:   relation.ViewTemplateURL,
			URLParams:                "_to_field=id&_popup=1",
			ViewURLParams:            "_to_field=id",
			CanAddRelated:            relation.AddURL != "",
			CanChangeRelated:         relation.ChangeTemplateURL != "",
			CanDeleteRelated:         relation.DeleteTemplateURL != "",
			CanViewRelated:           relation.ViewTemplateURL != "",
		}
		return RelatedWidgetWrapper(related, widget)
	default:
		return TextInput(config)
	}
}

func adminFieldLabel(modelAdmin ModelAdmin, field ChangeFormField) string {
	if field.Label != "" {
		return field.Label
	}
	return adminLabel(field.Name)
}

func adminFieldRequired(modelAdmin ModelAdmin, field ChangeFormField) bool {
	if field.Readonly || field.Widget == WidgetPasswordHash {
		return false
	}
	if field.Required {
		return true
	}
	return false
}

func adminFieldHelpText(modelAdmin ModelAdmin, field ChangeFormField) string {
	if field.HelpText != "" {
		return field.HelpText
	}
	return ""
}

type adminRelation struct {
	ModelName         string
	ModelLabel        string
	AddURL            string
	ChangeTemplateURL string
	DeleteTemplateURL string
	ViewTemplateURL   string
	AutocompleteURL   string
}

func adminRelationAutocompleteURL(site *Site, modelAdmin ModelAdmin, fieldName string) string {
	relation := adminRelationTarget(site, modelAdmin, fieldName)
	if relation.AutocompleteURL != "" {
		return relation.AutocompleteURL
	}
	return "autocomplete/"
}

func adminRelationTarget(site *Site, modelAdmin ModelAdmin, fieldName string) adminRelation {
	return adminRelationTargetForRequest(site, modelAdmin, fieldName, nil, auth.User{})
}

func adminRelationTargetForRequest(site *Site, modelAdmin ModelAdmin, fieldName string, request *http.Request, user auth.User) adminRelation {
	site = adminSiteOrDefault(site)
	target := adminRelationTargetLabel(modelAdmin, fieldName)
	appLabel, modelName, ok := splitAdminModelLabel(target)
	if !ok {
		return adminRelation{}
	}
	relatedAdmin, registered := site.ModelRegistry.GetAdmin(target)
	base := site.URLPrefix + "/" + strings.ToLower(appLabel) + "/" + strings.ToLower(modelName) + "/"
	label := strings.ToLower(modelName)
	relation := adminRelation{
		ModelName:       label,
		ModelLabel:      label,
		AutocompleteURL: base + "autocomplete/",
	}
	if registered {
		relation.ModelLabel = modelVerboseName(relatedAdmin)
		if request == nil {
			request, _ = http.NewRequest(http.MethodGet, "/", nil)
		}
		if relatedAdmin.HasAddPermission(request, user) {
			relation.AddURL = base + "add/"
		}
		if relatedAdmin.HasChangePermission(request, user) {
			relation.ChangeTemplateURL = base + "__fk__/change/"
		}
		if relatedAdmin.HasDeletePermission(request, user) {
			relation.DeleteTemplateURL = base + "__fk__/delete/"
		}
		if relatedAdmin.HasViewPermission(request, user) || relatedAdmin.HasChangePermission(request, user) {
			relation.ViewTemplateURL = base + "__fk__/change/"
		}
	}
	return relation
}

func adminRelationTargetLabel(modelAdmin ModelAdmin, fieldName string) string {
	for _, field := range modelAdmin.Model.Fields {
		if field.Name == fieldName {
			return field.RelationTarget
		}
	}
	return ""
}

func splitAdminModelLabel(label string) (string, string, bool) {
	parts := strings.Split(label, ".")
	if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" || strings.TrimSpace(parts[1]) == "" {
		return "", "", false
	}
	return strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1]), true
}

func submitButtons(buttons []SaveButton) []adminSubmitButton {
	result := make([]adminSubmitButton, 0, len(buttons))
	for _, button := range buttons {
		switch button {
		case SaveButtonSave:
			result = append(result, adminSubmitButton{Name: "_save", Value: "Save", Label: "Save", Class: "default"})
		case SaveButtonSaveAndContinue:
			result = append(result, adminSubmitButton{Name: "_continue", Value: "Save and continue editing", Label: "Save and continue editing"})
		case SaveButtonSaveAndAddAnother:
			result = append(result, adminSubmitButton{Name: "_addanother", Value: "Save and add another", Label: "Save and add another"})
		case SaveButtonSaveAsNew:
			result = append(result, adminSubmitButton{Name: "_saveasnew", Value: "Save as new", Label: "Save as new"})
		}
	}
	return result
}

func listFilters(modelAdmin ModelAdmin) []adminListFilter {
	filters := make([]adminListFilter, 0, len(modelAdmin.ListFilter))
	for _, name := range modelAdmin.ListFilter {
		filters = append(filters, adminListFilter{Name: name, Label: adminLabel(name), Title: adminFilterTitle(name)})
	}
	return filters
}

func adminFilterTitle(name string) string {
	switch name {
	case "is_staff":
		return "staff status"
	case "is_superuser":
		return "superuser status"
	case "is_active":
		return "active"
	case "groups":
		return "group"
	default:
		return strings.ToLower(adminLabel(name))
	}
}

func modelVerboseName(modelAdmin ModelAdmin) string {
	if modelAdmin.Model.VerboseName != "" {
		return modelAdmin.Model.VerboseName
	}
	return strings.ToLower(modelAdmin.Model.ModelName)
}

func modelVerboseNamePlural(modelAdmin ModelAdmin) string {
	if modelAdmin.Model.VerboseNamePlural != "" {
		return modelAdmin.Model.VerboseNamePlural
	}
	return modelVerboseName(modelAdmin) + "s"
}

func rowsFromModelAdmin(modelAdmin ModelAdmin, request *http.Request) []map[string]any {
	if modelAdmin.Hooks.GetQuerySet == nil {
		return nil
	}
	switch rows := modelAdmin.Hooks.GetQuerySet(request).(type) {
	case []map[string]any:
		return cloneRows(rows)
	default:
		return nil
	}
}

func adminRequestUser(site *Site, request *http.Request) (auth.User, bool) {
	if user, ok := auth.UserFromContext(request.Context()); ok && user.IsAuthenticated() && !user.IsAnonymous() {
		return user, true
	}
	if site != nil {
		if provider, ok := site.PermissionPolicy.(interface {
			UserForRequest(*http.Request) (auth.User, bool)
		}); ok {
			return provider.UserForRequest(request)
		}
	}
	return auth.User{}, false
}

func adminUserDisplayName(site *Site, request *http.Request) string {
	user, ok := adminRequestUser(site, request)
	if !ok {
		return ""
	}
	if user.Username != "" {
		return user.Username
	}
	if user.Email != "" {
		return user.Email
	}
	if user.ID != 0 {
		return fmt.Sprint(user.ID)
	}
	return ""
}

func adminSiteOrDefault(site *Site) *Site {
	if site != nil {
		return site
	}
	return DefaultSite()
}

func adminLabel(name string) string {
	name = strings.TrimSpace(strings.ReplaceAll(name, "_", " "))
	if name == "" {
		return ""
	}
	words := strings.Fields(name)
	for i, word := range words {
		runes := []rune(strings.ToLower(word))
		if len(runes) == 0 {
			continue
		}
		runes[0] = unicode.ToUpper(runes[0])
		words[i] = string(runes)
	}
	return strings.Join(words, " ")
}

func adminClassName(value string) string {
	value = strings.ToLower(value)
	var builder strings.Builder
	previousDash := false
	for _, char := range value {
		if (char >= 'a' && char <= 'z') || (char >= '0' && char <= '9') {
			builder.WriteRune(char)
			previousDash = false
			continue
		}
		if !previousDash {
			builder.WriteByte('-')
			previousDash = true
		}
	}
	return strings.Trim(builder.String(), "-")
}
