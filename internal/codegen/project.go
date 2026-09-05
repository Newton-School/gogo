// Package codegen contains private source-generation machinery.
package codegen

import (
	"bytes"
	"errors"
	"fmt"
	"github.com/Newton-School/gogo/core/conf"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

const moduleRoot = "github.com/Newton-School/gogo"

type ProjectOptions struct{ Module, Version string }

var modulePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._~/-]*$`)

func validModule(module string) bool {
	if !modulePattern.MatchString(module) || strings.HasSuffix(module, "/") {
		return false
	}
	for _, part := range strings.Split(module, "/") {
		if part == "" || part == "." || part == ".." {
			return false
		}
	}
	return true
}
func validPackage(name string) bool {
	return token.IsIdentifier(name) && token.Lookup(name) == token.IDENT && strings.ToLower(name) == name && name != "main" && name != "init"
}
func safeTarget(target string) error {
	if target == "" {
		return errors.New("explicit generation target required")
	}
	for _, part := range strings.Split(filepath.ToSlash(target), "/") {
		if part == ".." {
			return errors.New("generation path traversal rejected")
		}
	}
	if info, e := os.Lstat(target); e == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return errors.New("generation target must be a directory")
		}
		entries, e := os.ReadDir(target)
		if e != nil {
			return e
		}
		if len(entries) > 0 {
			return errors.New("generation target is not empty")
		}
	} else if !errors.Is(e, os.ErrNotExist) {
		return e
	}
	return nil
}

// writeTree creates only new files. On a write failure it removes only files it
// created; caller-owned destinations and pre-existing contents are preserved.
func writeTree(target string, files map[string]string) error {
	prepared, err := prepareFiles(files)
	if err != nil {
		return err
	}
	if err := safeTarget(target); err != nil {
		return err
	}
	if err := os.MkdirAll(target, 0755); err != nil {
		return err
	}
	root, err := os.OpenRoot(target)
	if err != nil {
		return err
	}
	defer root.Close()
	return writePreparedTree(root, prepared)
}

// Validate every file before creating any output, including files that sort
// after otherwise valid templates.
func prepareFiles(files map[string]string) (map[string][]byte, error) {
	prepared := make(map[string][]byte, len(files))
	for name, source := range files {
		if !fs.ValidPath(name) {
			return nil, errors.New("invalid generated path")
		}
		data := []byte(source)
		if strings.HasSuffix(name, ".go") {
			var err error
			data, err = format.Source(data)
			if err != nil {
				return nil, fmt.Errorf("invalid generated Go file %s: %w", name, err)
			}
		}
		prepared[name] = data
	}
	return prepared, nil
}

func writePreparedTree(root *os.Root, files map[string][]byte) (err error) {
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	slices.Sort(names)
	var created []string
	defer func() {
		if err != nil {
			for _, owned := range created {
				err = errors.Join(err, root.Remove(owned))
			}
		}
	}()
	for _, name := range names {
		if err = root.MkdirAll(filepath.Dir(name), 0755); err != nil {
			return err
		}
		mode := fs.FileMode(0644)
		if name == ".env" {
			mode = 0600
		}
		file, e := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
		if e != nil {
			err = e
		} else {
			created = append(created, name)
			_, err = file.Write(files[name])
			err = errors.Join(err, file.Close())
		}
		if err != nil {
			return err
		}
	}
	return nil
}
func StartProject(target string, options ProjectOptions) error {
	if !validModule(options.Module) {
		return errors.New("valid Go module path required")
	}
	if options.Version == "" {
		options.Version = "v0.0.0"
	}
	if !regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+(?:[-+][A-Za-z0-9.-]+)?$`).MatchString(options.Version) {
		return errors.New("valid framework module version required")
	}
	module := options.Module
	env := conf.CoreSchema().EnvTemplate()
	files := map[string]string{
		"go.mod":    fmt.Sprintf("module %s\n\ngo 1.26.0\n\nrequire (\n %s %s\n %s/connectors/postgres %s\n)\n", module, moduleRoot, options.Version, moduleRoot, options.Version),
		"manage.go": fmt.Sprintf("package main\nimport (\"%s\";\"%s/config\")\nfunc main(){gogo.Main(config.Project())}\n", moduleRoot, module),
		"config/apps.go": `package config
import "github.com/Newton-School/gogo/core/app"
func InstalledApps() []app.Config { return []app.Config{} }
`,
		"config/settings.go": `package config
import("github.com/Newton-School/gogo/core/conf";"github.com/Newton-School/gogo/core/management";"github.com/Newton-School/gogo/core/app";"github.com/Newton-School/gogo/core/security";"github.com/Newton-School/gogo/core/urls";"net/http")
func Settings() conf.Schema {return conf.CoreSchema()}
func Project() management.Project {
 connections:=&Connections{}
 return management.Project{Name:` + strconv.Quote(filepath.Base(target)) + `,Schema:Settings(),Apps:InstalledApps(),RuntimeResources:[]string{"database"},ResourceFactory:connections.Resources,Commands:management.MigrationCommands(func()dbBackend{return connections.Database},func()dbEditor{return connections.Database.SchemaEditor()}),Handler:func(registry *app.Registry,settings conf.Values)(http.Handler,error){
  router,err:=urls.New(Routes(registry)...);if err!=nil{return nil,err}
  headers,err:=security.Headers(security.HeadersConfig{AllowedHosts:settings.List("GOGO_ALLOWED_HOSTS")});if err!=nil{return nil,err};return headers(router),nil
 }}
}
`,
		"config/connections.go": `package config
import("context";"slices";"github.com/Newton-School/gogo/core/app";"github.com/Newton-School/gogo/core/conf";"github.com/Newton-School/gogo/core/db";"github.com/Newton-School/gogo/connectors/postgres")
type dbBackend = db.Backend
type dbEditor = db.SchemaEditor
type Connections struct { Database *postgres.Backend }
func(c *Connections) Resources(settings conf.Values,required []string)([]app.Resource,error){
 if !slices.Contains(required,"database"){return nil,nil}
 return []app.Resource{{Name:"database",Open:func(ctx context.Context)(func(context.Context)error,error){
  backend,err:=postgres.Open(ctx,postgres.Config{DSN:settings.Secret("GOGO_DATABASE_URL").Reveal(),Production:settings.String("GOGO_ENV")=="production",MaxOpen:int(settings.Int("GOGO_DB_MAX_OPEN")),MaxIdle:int(settings.Int("GOGO_DB_MAX_IDLE")),MaxLifetime:settings.Duration("GOGO_DB_MAX_LIFETIME")});if err!=nil{return nil,err};c.Database=backend;return func(context.Context)error{return backend.Close()},nil
 } }},nil
}
`,
		"config/urls.go": `package config
import("net/http";"github.com/Newton-School/gogo/core/app";ghttp "github.com/Newton-School/gogo/core/http";"github.com/Newton-School/gogo/core/urls")
func Routes(registry *app.Registry)[]urls.Route{
 routes:=[]urls.Route{urls.Path("",ghttp.Adapt(func(*http.Request)(ghttp.Response,error){return ghttp.JSON(200,map[string]string{"framework":"gogo"})}),"index","GET")}
 for _,name:=range registry.Names("urls"){value,_:=registry.Get("urls",name);routes=append(routes,value.([]urls.Route)...)}
 return routes
}
`,
		".env": env, ".env.example": env,
		".gitignore":    "# Local configuration and credentials\n.env\n.env.*\n!.env.example\n\n# Build and test output\n/bin/\n/dist/\n/coverage/\n*.test\ncoverage.out\n\n# Editors and operating system\n.DS_Store\n.idea/\n.vscode/\n",
		"apps/.gitkeep": "", "templates/.gitkeep": "", "static/.gitkeep": "", "tests/integration/.gitkeep": "",
	}
	return writeTree(target, files)
}

func projectModule(root *os.Root) (string, error) {
	content, err := root.ReadFile("go.mod")
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(content), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[0] == "module" && validModule(fields[1]) {
			return fields[1], nil
		}
	}
	return "", errors.New("project module path missing")
}
func StartApp(projectDir, label string) error {
	if !validPackage(label) {
		return errors.New("app label must be a lowercase Go package name")
	}
	root, err := os.OpenRoot(projectDir)
	if err != nil {
		return err
	}
	defer root.Close()
	// Source registration is intentionally restricted to a regular file. The
	// confined root also prevents symlink traversal outside the client project.
	info, err := root.Lstat("config/apps.go")
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("config/apps.go must be a regular file")
	}
	module, err := projectModule(root)
	if err != nil {
		return err
	}
	original, err := root.ReadFile("config/apps.go")
	if err != nil {
		return err
	}
	updated, err := registerApp(original, module+"/apps/"+label, label)
	if err != nil {
		return err
	}
	dir := "apps/" + label
	files := map[string]string{
		"app.go": fmt.Sprintf(`package %s
import("github.com/Newton-School/gogo/core/app"; appmigrations %q)
func App()app.Config{return app.Config{Name:%q,Label:%q,Register:func(registry *app.Registry)error{
 if err:=registerModels(registry);err!=nil{return err};if err:=registry.Register("migrations",%q,appmigrations.All);err!=nil{return err};return registry.Register("urls",%q,Routes())
}}}
`, label, module+"/apps/"+label+"/migrations", module+"/apps/"+label, label, label, label),
		"models.go":                        "package " + label + "\n\n// Declare Go models with models.Base and a Schema method here.\n",
		"views.go":                         "package " + label + "\n\n// Define context-aware HTTP views here.\n",
		"serializers.go":                   "package " + label + "\n\n// Declare explicit API input/output fields here.\n",
		"forms.go":                         "package " + label + "\n\n// Declare HTML forms and ModelForm field allowlists here.\n",
		"services.go":                      "package " + label + "\n\n// Keep transactional business operations in this package.\n",
		"urls.go":                          "package " + label + "\nimport \"github.com/Newton-School/gogo/core/urls\"\nfunc Routes()[]urls.Route{return []urls.Route{}}\n",
		"zz_gogo.gen.go":                   "// Code generated by gogo generate. DO NOT EDIT.\npackage " + label + "\nimport \"github.com/Newton-School/gogo/core/app\"\nfunc registerModels(registry *app.Registry)error{return nil}\n",
		"migrations/registry.go":           "// Code generated by gogo makemigrations; DO NOT EDIT.\npackage migrations\nimport \"github.com/Newton-School/gogo/core/migrations\"\nvar All=[]migrations.Migration{}\n",
		"templates/" + label + "/.gitkeep": "", "static/" + label + "/.gitkeep": "",
	}
	prepared, err := prepareFiles(files)
	if err != nil {
		return err
	}
	if _, err := root.Lstat(dir); err == nil {
		return errors.New("app destination already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err = root.MkdirAll(dir, 0755); err != nil {
		return err
	}
	appRoot, err := root.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer appRoot.Close()
	if err = writePreparedTree(appRoot, prepared); err != nil {
		return err
	}
	// Refuse an unexpected concurrent edit; never replace a changed app list.
	current, err := root.ReadFile("config/apps.go")
	if err != nil || !bytes.Equal(current, original) {
		return errors.New("app created; config/apps.go changed concurrently, register App manually")
	}
	currentInfo, err := root.Lstat("config/apps.go")
	if err != nil || !currentInfo.Mode().IsRegular() || !os.SameFile(info, currentInfo) {
		return errors.New("app created; config/apps.go changed concurrently, register App manually")
	}
	return root.WriteFile("config/apps.go", updated, 0644)
}
func registerApp(source []byte, importPath, label string) ([]byte, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "apps.go", source, parser.ParseComments)
	if err != nil {
		return nil, err
	}
	for _, imp := range file.Imports {
		path, _ := strconv.Unquote(imp.Path.Value)
		if path == importPath {
			return nil, errors.New("app already registered")
		}
		if imp.Name != nil && imp.Name.Name == label || imp.Name == nil && filepath.Base(path) == label {
			return nil, errors.New("app import alias conflicts")
		}
	}
	var list *ast.CompositeLit
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "InstalledApps" || fn.Body == nil {
			continue
		}
		if len(fn.Body.List) != 1 {
			return nil, errors.New("custom app configuration: explicit manual registration required")
		}
		ret, ok := fn.Body.List[0].(*ast.ReturnStmt)
		if !ok || len(ret.Results) != 1 {
			return nil, errors.New("custom app configuration: explicit manual registration required")
		}
		list, _ = ret.Results[0].(*ast.CompositeLit)
	}
	if list == nil {
		return nil, errors.New("InstalledApps must return an explicit app.Config slice")
	}
	list.Elts = append(list.Elts, &ast.CallExpr{Fun: &ast.SelectorExpr{X: ast.NewIdent(label), Sel: ast.NewIdent("App")}})
	spec := &ast.ImportSpec{Name: ast.NewIdent(label), Path: &ast.BasicLit{Kind: token.STRING, Value: strconv.Quote(importPath)}}
	file.Decls = append([]ast.Decl{&ast.GenDecl{Tok: token.IMPORT, Specs: []ast.Spec{spec}}}, file.Decls...)
	var out bytes.Buffer
	if err = format.Node(&out, fset, file); err != nil {
		return nil, err
	}
	return format.Source(out.Bytes())
}
