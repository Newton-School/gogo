package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

func TestDescribeFieldsPreservesEveryOptionAndComment(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "sample.go", `package example
type Config struct {
 // Bounds are inclusive.
 Min, Max int
 Enabled *bool // Nil inherits.
 private string
}
func Run(ctx string, options ...Config) (int, error) { return 0, nil }
`, parser.ParseComments)
	if err != nil {
		t.Fatal(err)
	}
	fields := file.Decls[0].(*ast.GenDecl).Specs[0].(*ast.TypeSpec).Type.(*ast.StructType).Fields
	got := describeFields(fset, fields, true)
	if len(got) != 3 || got[0].Name != "Min" || got[1].Name != "Max" || got[1].Doc != "Bounds are inclusive." || got[2].Type != "*bool" || got[2].Doc != "Nil inherits." {
		t.Fatalf("options: %#v", got)
	}
	function := file.Decls[1].(*ast.FuncDecl)
	parameters := describeFields(fset, function.Type.Params, false)
	results := describeFields(fset, function.Type.Results, false)
	if len(parameters) != 2 || parameters[1].Type != "...Config" || len(results) != 2 || results[1].Type != "error" {
		t.Fatalf("signature: %#v %#v", parameters, results)
	}
}
