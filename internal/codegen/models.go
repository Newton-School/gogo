package codegen

import (
	"bytes"
	"errors"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

type generatedModel struct {
	name   string
	fields []generatedField
}
type generatedField struct{ name, goName, typeName string }

// Generate builds typed query references and explicit model registrations from
// Go declarations. It never opens a database or evaluates arbitrary schema code.
func Generate(projectDir string) error {
	root, err := os.OpenRoot(projectDir)
	if err != nil {
		return err
	}
	defer root.Close()
	apps, err := root.Open("apps")
	if err != nil {
		return err
	}
	entries, err := apps.ReadDir(-1)
	_ = apps.Close()
	if err != nil {
		return err
	}
	slices.SortFunc(entries, func(a, b os.DirEntry) int { return strings.Compare(a.Name(), b.Name()) })
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if !validPackage(entry.Name()) {
			return errors.New("invalid app package directory")
		}
		if err = generateApp(root, entry.Name()); err != nil {
			return fmt.Errorf("generate app %s: %w", entry.Name(), err)
		}
	}
	return nil
}
func generateApp(root *os.Root, label string) error {
	dir, err := root.Open("apps/" + label)
	if err != nil {
		return err
	}
	entries, err := dir.ReadDir(-1)
	_ = dir.Close()
	if err != nil {
		return err
	}
	fset := token.NewFileSet()
	var files []*ast.File
	structs := map[string]*ast.StructType{}
	imports := map[string]string{}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") || strings.HasSuffix(entry.Name(), ".gen.go") {
			continue
		}
		data, err := root.ReadFile("apps/" + label + "/" + entry.Name())
		if err != nil {
			return err
		}
		file, err := parser.ParseFile(fset, entry.Name(), data, 0)
		if err != nil {
			return err
		}
		if file.Name.Name != label {
			return errors.New("app package name mismatch")
		}
		files = append(files, file)
		for _, spec := range file.Imports {
			value, _ := strconv.Unquote(spec.Path.Value)
			alias := filepath.Base(value)
			if spec.Name != nil {
				alias = spec.Name.Name
			}
			if previous, ok := imports[alias]; ok && previous != value {
				return errors.New("ambiguous app import alias")
			}
			imports[alias] = value
		}
		for _, decl := range file.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok {
				continue
			}
			for _, spec := range gen.Specs {
				typed, ok := spec.(*ast.TypeSpec)
				if !ok {
					continue
				}
				structure, ok := typed.Type.(*ast.StructType)
				if ok {
					structs[typed.Name.Name] = structure
				}
			}
		}
	}
	var models []generatedModel
	usedImports := map[string]string{}
	for _, file := range files {
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Name.Name != "Schema" || fn.Recv == nil || len(fn.Recv.List) != 1 || fn.Body == nil {
				continue
			}
			receiver := fn.Recv.List[0].Type
			if star, ok := receiver.(*ast.StarExpr); ok {
				receiver = star.X
			}
			identifier, ok := receiver.(*ast.Ident)
			if !ok {
				continue
			}
			structure := structs[identifier.Name]
			if structure == nil {
				continue
			}
			model := generatedModel{name: identifier.Name}
			goFields := map[string]ast.Expr{}
			for _, field := range structure.Fields.List {
				for _, name := range field.Names {
					goFields[name.Name] = field.Type
				}
			}
			var fieldList *ast.CompositeLit
			ast.Inspect(fn.Body, func(node ast.Node) bool {
				kv, ok := node.(*ast.KeyValueExpr)
				if !ok {
					return true
				}
				key, ok := kv.Key.(*ast.Ident)
				if ok && key.Name == "Fields" {
					fieldList, _ = kv.Value.(*ast.CompositeLit)
					return false
				}
				return true
			})
			if fieldList == nil {
				return fmt.Errorf("%s.Schema must expose a literal Fields descriptor list for source generation", model.name)
			}
			for _, element := range fieldList.Elts {
				call, ok := element.(*ast.CallExpr)
				if !ok || len(call.Args) == 0 {
					return errors.New("model field constructor required")
				}
				name, ok := literalString(call.Args[0])
				if !ok {
					return errors.New("model field name must be a string literal")
				}
				goName := name
				for _, arg := range call.Args[1:] {
					option, ok := arg.(*ast.CallExpr)
					if !ok || len(option.Args) != 1 {
						continue
					}
					selector, ok := option.Fun.(*ast.SelectorExpr)
					if ok && selector.Sel.Name == "WithStructField" {
						goName, ok = literalString(option.Args[0])
						if !ok {
							return errors.New("Go field mapping must be a literal")
						}
					}
				}
				fieldType, ok := goFields[goName]
				if !ok {
					return fmt.Errorf("%s field %s requires a direct Go field mapping", model.name, name)
				}
				var printed bytes.Buffer
				if err = format.Node(&printed, fset, fieldType); err != nil {
					return err
				}
				ast.Inspect(fieldType, func(node ast.Node) bool {
					selector, ok := node.(*ast.SelectorExpr)
					if ok {
						if alias, ok := selector.X.(*ast.Ident); ok {
							usedImports[alias.Name] = imports[alias.Name]
						}
					}
					return true
				})
				model.fields = append(model.fields, generatedField{name, goName, printed.String()})
			}
			models = append(models, model)
		}
	}
	slices.SortFunc(models, func(a, b generatedModel) int { return strings.Compare(a.name, b.name) })
	var output strings.Builder
	output.WriteString("// Code generated by gogo generate. DO NOT EDIT.\npackage " + label + "\nimport(\n\"" + moduleRoot + "/core/app\"\n")
	if len(models) > 0 {
		output.WriteString("\"" + moduleRoot + "/core/orm\"\n")
	}
	aliases := make([]string, 0, len(usedImports))
	for alias := range usedImports {
		aliases = append(aliases, alias)
	}
	slices.Sort(aliases)
	for _, alias := range aliases {
		if usedImports[alias] == "" {
			return errors.New("cannot resolve generated field import")
		}
		fmt.Fprintf(&output, "%s %q\n", alias, usedImports[alias])
	}
	output.WriteString(")\n")
	for _, model := range models {
		fmt.Fprintf(&output, "var %sFields = struct{\n", model.name)
		for _, field := range model.fields {
			fmt.Fprintf(&output, "%s orm.Ref[%s]\n", field.goName, field.typeName)
		}
		output.WriteString("}{\n")
		for _, field := range model.fields {
			fmt.Fprintf(&output, "%s: orm.Field[%s](%q),\n", field.goName, field.typeName, field.name)
		}
		output.WriteString("}\n")
	}
	output.WriteString("func registerModels(registry *app.Registry)error{\n")
	for _, model := range models {
		fmt.Fprintf(&output, "{schema:=(&%s{}).Schema();if err:=schema.Validate();err!=nil{return err};if err:=registry.Register(\"models\",schema.Key(),schema);err!=nil{return err}}\n", model.name)
	}
	output.WriteString("return nil\n}\n")
	generated, err := format.Source([]byte(output.String()))
	if err != nil {
		return err
	}
	target := "apps/" + label + "/zz_gogo.gen.go"
	existing, err := root.ReadFile(target)
	if err == nil && !bytes.HasPrefix(existing, []byte("// Code generated by gogo generate.")) {
		return errors.New("refusing to overwrite non-generated model registration")
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return root.WriteFile(target, generated, 0644)
}
func literalString(expr ast.Expr) (string, bool) {
	value, ok := expr.(*ast.BasicLit)
	if !ok || value.Kind != token.STRING {
		return "", false
	}
	result, err := strconv.Unquote(value.Value)
	return result, err == nil
}
