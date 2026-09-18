// Command catalog extracts public API documentation without loading application
// configuration, connecting to services, or evaluating application callbacks.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/doc"
	"go/format"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Newton-School/gogo/core/conf"
	"github.com/Newton-School/gogo/core/templates"
)

const module = "github.com/Newton-School/gogo"

type declaration struct {
	Name, Kind, Signature, Doc, File string
	Line                             int
	Members, Parameters, Results     []member
}
type member struct {
	Name, Type, Doc string
}
type pkg struct {
	Path, Directory, Doc string
	Declarations         []declaration
}
type catalog struct {
	Packages  []pkg
	Settings  conf.Schema
	Templates templates.Description
}

func main() {
	result, err := collect(".")
	if err == nil {
		err = json.NewEncoder(os.Stdout).Encode(result)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func collect(root string) (catalog, error) {
	result := catalog{Settings: conf.CoreSchema()}
	description, err := templates.New(templates.Config{}).Describe(4096)
	if err != nil {
		return result, err
	}
	result.Templates = description
	dirs := []string{"."}
	for _, parent := range []string{"core", "admin", "async", "connectors"} {
		err := filepath.WalkDir(filepath.Join(root, parent), func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if !entry.IsDir() {
				return nil
			}
			if entry.Name() == "internal" || entry.Name() == "testdata" {
				return filepath.SkipDir
			}
			dirs = append(dirs, path)
			return nil
		})
		if err != nil {
			return result, err
		}
	}
	sort.Strings(dirs)
	for _, dir := range dirs {
		fset := token.NewFileSet()
		parsed, err := parser.ParseDir(fset, dir, func(info fs.FileInfo) bool {
			return strings.HasSuffix(info.Name(), ".go") && !strings.HasSuffix(info.Name(), "_test.go")
		}, parser.ParseComments)
		if err != nil {
			return result, err
		}
		for _, syntax := range parsed {
			if syntax.Name == "main" {
				continue
			}
			path := module
			if dir != "." {
				path += "/" + filepath.ToSlash(dir)
			}
			parsedDoc := doc.New(syntax, path, 0)
			p := pkg{Path: path, Directory: filepath.ToSlash(dir), Doc: parsedDoc.Doc}
			add := func(name, kind, comment string, node ast.Node) error {
				var out bytes.Buffer
				if err := format.Node(&out, fset, node); err != nil {
					return err
				}
				pos := fset.Position(node.Pos())
				d := declaration{Name: name, Kind: kind, Signature: out.String(), Doc: comment, File: filepath.ToSlash(pos.Filename), Line: pos.Line}
				if function, ok := node.(*ast.FuncDecl); ok {
					d.Parameters = describeFields(fset, function.Type.Params, false)
					d.Results = describeFields(fset, function.Type.Results, false)
				}
				if declaration, ok := node.(*ast.GenDecl); ok && kind == "type" {
					for _, specification := range declaration.Specs {
						typeSpec, ok := specification.(*ast.TypeSpec)
						if !ok || typeSpec.Name.Name != name {
							continue
						}
						switch t := typeSpec.Type.(type) {
						case *ast.StructType:
							d.Members = describeFields(fset, t.Fields, true)
						case *ast.InterfaceType:
							d.Members = describeFields(fset, t.Methods, true)
						case *ast.FuncType:
							d.Parameters = describeFields(fset, t.Params, false)
							d.Results = describeFields(fset, t.Results, false)
						}
					}
				}
				p.Declarations = append(p.Declarations, d)
				return nil
			}
			values := func(items []*doc.Value, kind string) error {
				for _, v := range items {
					if err := add(strings.Join(v.Names, ", "), kind, v.Doc, v.Decl); err != nil {
						return err
					}
				}
				return nil
			}
			funcs := func(items []*doc.Func, kind, prefix string) error {
				for _, f := range items {
					f.Decl.Body = nil
					if err := add(prefix+f.Name, kind, f.Doc, f.Decl); err != nil {
						return err
					}
				}
				return nil
			}
			if err := values(parsedDoc.Consts, "constant"); err != nil {
				return result, err
			}
			if err := values(parsedDoc.Vars, "variable"); err != nil {
				return result, err
			}
			if err := funcs(parsedDoc.Funcs, "function", ""); err != nil {
				return result, err
			}
			for _, t := range parsedDoc.Types {
				if err := add(t.Name, "type", t.Doc, t.Decl); err != nil {
					return result, err
				}
				if err := values(t.Consts, "constant"); err != nil {
					return result, err
				}
				if err := values(t.Vars, "variable"); err != nil {
					return result, err
				}
				if err := funcs(t.Funcs, "function", ""); err != nil {
					return result, err
				}
				if err := funcs(t.Methods, "method", t.Name+"."); err != nil {
					return result, err
				}
			}
			sort.Slice(p.Declarations, func(i, j int) bool { return p.Declarations[i].Name < p.Declarations[j].Name })
			result.Packages = append(result.Packages, p)
		}
	}
	sort.Slice(result.Packages, func(i, j int) bool { return result.Packages[i].Path < result.Packages[j].Path })
	return result, nil
}

// Split grouped names so every public option has its own documentation row.
// Do not infer constructor defaults from Go zero values.
func describeFields(fset *token.FileSet, fields *ast.FieldList, exportedOnly bool) []member {
	var result []member
	if fields == nil {
		return result
	}
	for _, field := range fields.List {
		var out bytes.Buffer
		if err := format.Node(&out, fset, field.Type); err != nil {
			continue
		}
		comment := strings.TrimSpace(field.Doc.Text() + "\n" + field.Comment.Text())
		if len(field.Names) == 0 {
			result = append(result, member{Name: "(embedded or positional)", Type: out.String(), Doc: comment})
		}
		for _, name := range field.Names {
			if exportedOnly && !name.IsExported() {
				continue
			}
			result = append(result, member{Name: name.Name, Type: out.String(), Doc: comment})
		}
	}
	return result
}
