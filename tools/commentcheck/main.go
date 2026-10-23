package main

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

var generatedPattern = regexp.MustCompile(`(?m)^// Code generated .* DO NOT EDIT\.$`)

func main() {
	os.Exit(run())
}

func run() int {
	files, err := trackedGoFiles()
	if err != nil {
		fmt.Fprintln(os.Stderr, "commentcheck: "+err.Error())
		return 1
	}
	failed := false
	for _, f := range files {
		src, err := os.ReadFile(filepath.Clean(f))
		if err != nil {
			fmt.Fprintln(os.Stderr, "commentcheck: "+err.Error())
			failed = true
			continue
		}
		found, err := violationsInFile(f, src)
		if err != nil {
			fmt.Fprintln(os.Stderr, "commentcheck: "+f+": "+err.Error())
			failed = true
			continue
		}
		for _, v := range found {
			fmt.Println(v)
			failed = true
		}
	}
	if failed {
		return 1
	}
	return 0
}

func trackedGoFiles() ([]string, error) {
	cmd := exec.Command("git", "ls-files", "--", "*.go")
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return nil, err
	}
	trimmed := strings.TrimRight(out.String(), "\n")
	if trimmed == "" {
		return nil, nil
	}
	return strings.Split(trimmed, "\n"), nil
}

func isGenerated(src []byte) bool {
	return generatedPattern.Match(src)
}

func isDirective(text string) bool {
	if !strings.HasPrefix(text, "//") {
		return false
	}
	rest := text[2:]
	if strings.HasPrefix(rest, "go:") {
		return true
	}
	if rest == "line" || strings.HasPrefix(rest, "line ") || strings.HasPrefix(rest, "line\t") || strings.HasPrefix(rest, "line:") {
		return true
	}
	trimmed := strings.TrimLeft(rest, " \t")
	if strings.HasPrefix(trimmed, "+build ") || strings.HasPrefix(trimmed, "+build\t") || trimmed == "+build" {
		return true
	}
	return false
}

func fieldExported(f *ast.Field) bool {
	for _, n := range f.Names {
		if ast.IsExported(n.Name) {
			return true
		}
	}
	if len(f.Names) != 0 {
		return false
	}
	switch t := f.Type.(type) {
	case *ast.Ident:
		return ast.IsExported(t.Name)
	case *ast.StarExpr:
		switch x := t.X.(type) {
		case *ast.Ident:
			return ast.IsExported(x.Name)
		case *ast.SelectorExpr:
			return ast.IsExported(x.Sel.Name)
		}
	case *ast.SelectorExpr:
		return ast.IsExported(t.Sel.Name)
	}
	return false
}

func genDeclExports(d *ast.GenDecl) bool {
	if d.Tok == token.IMPORT {
		return false
	}
	for _, spec := range d.Specs {
		switch s := spec.(type) {
		case *ast.TypeSpec:
			if ast.IsExported(s.Name.Name) {
				return true
			}
		case *ast.ValueSpec:
			for _, n := range s.Names {
				if ast.IsExported(n.Name) {
					return true
				}
			}
		}
	}
	return false
}

func collectAllowed(f *ast.File) map[*ast.CommentGroup]struct{} {
	allowed := map[*ast.CommentGroup]struct{}{}
	if f.Doc != nil {
		allowed[f.Doc] = struct{}{}
	}
	for _, decl := range f.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			if d.Doc != nil && ast.IsExported(d.Name.Name) {
				allowed[d.Doc] = struct{}{}
			}
		case *ast.GenDecl:
			if d.Doc != nil && genDeclExports(d) {
				allowed[d.Doc] = struct{}{}
			}
			for _, spec := range d.Specs {
				switch s := spec.(type) {
				case *ast.TypeSpec:
					if s.Doc != nil && ast.IsExported(s.Name.Name) {
						allowed[s.Doc] = struct{}{}
					}
					switch t := s.Type.(type) {
					case *ast.StructType:
						for _, fld := range t.Fields.List {
							if fld.Doc != nil && fieldExported(fld) {
								allowed[fld.Doc] = struct{}{}
							}
						}
					case *ast.InterfaceType:
						for _, fld := range t.Methods.List {
							if fld.Doc != nil && fieldExported(fld) {
								allowed[fld.Doc] = struct{}{}
							}
						}
					}
				case *ast.ValueSpec:
					if s.Doc != nil {
						for _, n := range s.Names {
							if ast.IsExported(n.Name) {
								allowed[s.Doc] = struct{}{}
								break
							}
						}
					}
				}
			}
		}
	}
	return allowed
}

func violationsInFile(filename string, src []byte) ([]string, error) {
	if isGenerated(src) {
		return nil, nil
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, filename, src, parser.ParseComments)
	if err != nil {
		return nil, err
	}
	allowed := collectAllowed(f)
	var out []string
	for _, g := range f.Comments {
		if _, ok := allowed[g]; ok {
			continue
		}
		exempt := true
		for _, c := range g.List {
			if !isDirective(c.Text) {
				exempt = false
				break
			}
		}
		if exempt {
			continue
		}
		pos := fset.Position(g.Pos())
		out = append(out, filename+":"+strconv.Itoa(pos.Line))
	}
	return out, nil
}
