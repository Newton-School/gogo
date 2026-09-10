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
				p.Declarations = append(p.Declarations, declaration{name, kind, out.String(), comment, filepath.ToSlash(pos.Filename), pos.Line})
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
