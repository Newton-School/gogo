package admin

import (
	"bytes"
	"embed"
	"html/template"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

//go:embed templates static/*
var embeddedAssets embed.FS

var adminTemplatePartials = []string{
	"templates/actions.html",
	"templates/app_list.html",
	"templates/base_site.html",
	"templates/change_form_object_tools.html",
	"templates/change_list_object_tools.html",
	"templates/change_list_results.html",
	"templates/color_theme_toggle.html",
	"templates/date_hierarchy.html",
	"templates/filter.html",
	"templates/includes/fieldset.html",
	"templates/includes/object_delete_summary.html",
	"templates/nav_sidebar.html",
	"templates/pagination.html",
	"templates/prepopulated_fields_js.html",
	"templates/search_form.html",
	"templates/submit_line.html",
	"templates/widgets/clearable_file_input.html",
	"templates/widgets/date.html",
	"templates/widgets/foreign_key_raw_id.html",
	"templates/widgets/many_to_many_raw_id.html",
	"templates/widgets/radio.html",
	"templates/widgets/related_widget_wrapper.html",
	"templates/widgets/split_datetime.html",
	"templates/widgets/time.html",
	"templates/widgets/url.html",
	"templates/edit_inline/stacked.html",
	"templates/edit_inline/tabular.html",
}

var standaloneAdminTemplates = map[string]struct{}{
	"popup_response.html": {},
}

// AssetNames returns embedded admin asset paths.
func AssetNames() []string {
	var names []string
	_ = fs.WalkDir(embeddedAssets, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || path == "." {
			return err
		}
		names = append(names, path)
		return nil
	})
	sort.Strings(names)
	return names
}

// ReadAsset returns one embedded admin asset.
func ReadAsset(name string) ([]byte, bool) {
	body, err := embeddedAssets.ReadFile(name)
	return body, err == nil
}

// RenderTemplate renders an admin template, preferring project overrides.
func RenderTemplate(name string, data any, overrideDirs []string) (string, error) {
	if _, ok := standaloneAdminTemplates[name]; ok {
		if body, ok, err := readFirstTemplateOverride(templateOverrideNames(name, data), overrideDirs); ok || err != nil {
			if err != nil {
				return "", err
			}
			return renderStandaloneAdminTemplate(name, body, data)
		}
		body, err := embeddedAssets.ReadFile("templates/" + name)
		if err != nil {
			return "", err
		}
		return renderStandaloneAdminTemplate(name, body, data)
	}

	if body, ok, err := readFirstTemplateOverride(templateOverrideNames(name, data), overrideDirs); ok || err != nil {
		if err != nil {
			return "", err
		}
		if !templateOverrideUsesBase(body) {
			return renderStandaloneAdminTemplate(name, body, data)
		}
		return renderAdminTemplateSet(name, data, overrideDirs, body)
	}

	return renderAdminTemplateSet(name, data, overrideDirs, nil)
}

func renderStandaloneAdminTemplate(name string, body []byte, data any) (string, error) {
	tpl, err := template.New(name).Parse(string(body))
	if err != nil {
		return "", err
	}
	var buffer bytes.Buffer
	if err := tpl.Execute(&buffer, data); err != nil {
		return "", err
	}
	return buffer.String(), nil
}

func renderAdminTemplateSet(name string, data any, overrideDirs []string, targetOverride []byte) (string, error) {
	tpl := template.New("admin")
	for _, file := range templateFilesFor(name) {
		body, err := adminTemplateFileBody(file, name, data, overrideDirs, targetOverride)
		if err != nil {
			return "", err
		}
		if _, err := tpl.Parse(string(body)); err != nil {
			return "", err
		}
	}
	var buffer bytes.Buffer
	if err := tpl.ExecuteTemplate(&buffer, "base-site", data); err != nil {
		return "", err
	}
	return buffer.String(), nil
}

func adminTemplateFileBody(file, name string, data any, overrideDirs []string, targetOverride []byte) ([]byte, error) {
	target := "templates/" + name
	if file == target && targetOverride != nil {
		return targetOverride, nil
	}
	relative := strings.TrimPrefix(file, "templates/")
	if body, ok, err := readFirstTemplateOverride(templatePartialOverrideNames(relative, data), overrideDirs); ok || err != nil {
		return body, err
	}
	return embeddedAssets.ReadFile(file)
}

func templateFilesFor(name string) []string {
	target := "templates/" + name
	files := []string{"templates/base.html"}
	seen := map[string]struct{}{"templates/base.html": {}}
	for _, partial := range adminTemplatePartials {
		if _, ok := seen[partial]; ok {
			continue
		}
		files = append(files, partial)
		seen[partial] = struct{}{}
	}
	if _, ok := seen[target]; !ok {
		files = append(files, target)
	}
	return files
}

func templateOverrideNames(name string, data any) []string {
	name = strings.TrimPrefix(filepath.ToSlash(name), "templates/")
	scope := adminTemplateScope(data)
	var names []string
	if scope.appLabel != "" {
		if scope.modelName != "" {
			names = append(names, pathJoinSlash("admin", scope.appLabel, scope.modelName, name))
		}
		names = append(names, pathJoinSlash("admin", scope.appLabel, name))
	}
	names = append(names, pathJoinSlash("admin", name), name)
	return uniqueTemplateNames(names)
}

func templatePartialOverrideNames(name string, data any) []string {
	name = strings.TrimPrefix(filepath.ToSlash(name), "templates/")
	scope := adminTemplateScope(data)
	var names []string
	if scope.appLabel != "" {
		if scope.modelName != "" {
			names = append(names, pathJoinSlash("admin", scope.appLabel, scope.modelName, name))
		}
		names = append(names, pathJoinSlash("admin", scope.appLabel, name))
	}
	names = append(names, pathJoinSlash("admin", name))
	return uniqueTemplateNames(names)
}

type templateScope struct {
	appLabel  string
	modelName string
}

func adminTemplateScope(data any) templateScope {
	switch value := data.(type) {
	case adminPageData:
		return templateScope{
			appLabel:  strings.ToLower(strings.TrimSpace(value.AppLabel)),
			modelName: strings.ToLower(strings.TrimSpace(value.ModelName)),
		}
	case *adminPageData:
		if value == nil {
			return templateScope{}
		}
		return templateScope{
			appLabel:  strings.ToLower(strings.TrimSpace(value.AppLabel)),
			modelName: strings.ToLower(strings.TrimSpace(value.ModelName)),
		}
	default:
		return templateScope{}
	}
}

func uniqueTemplateNames(names []string) []string {
	seen := make(map[string]struct{}, len(names))
	unique := make([]string, 0, len(names))
	for _, name := range names {
		name = filepath.ToSlash(strings.TrimSpace(name))
		if name == "" {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		unique = append(unique, name)
	}
	return unique
}

func pathJoinSlash(parts ...string) string {
	return filepath.ToSlash(filepath.Join(parts...))
}

func templateOverrideUsesBase(body []byte) bool {
	text := string(body)
	return strings.Contains(text, "{{define") || strings.Contains(text, "{{ define") || strings.Contains(text, "{{template") || strings.Contains(text, "{{ template")
}

func readFirstTemplateOverride(names []string, overrideDirs []string) ([]byte, bool, error) {
	for _, name := range names {
		body, ok, err := readTemplateOverride(name, overrideDirs)
		if ok || err != nil {
			return body, ok, err
		}
	}
	return nil, false, nil
}

func readTemplateOverride(name string, overrideDirs []string) ([]byte, bool, error) {
	for _, dir := range overrideDirs {
		for _, candidate := range []string{
			filepath.Join(dir, name),
			filepath.Join(dir, "templates", name),
			filepath.Join(dir, "admin", "templates", name),
		} {
			body, err := os.ReadFile(candidate)
			if err == nil {
				return body, true, nil
			}
			if !os.IsNotExist(err) {
				return nil, false, err
			}
		}
	}
	return nil, false, nil
}
