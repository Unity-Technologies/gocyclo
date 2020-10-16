// Copyright 2013 Frederik Zipp. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Gocyclo calculates the cyclomatic complexities of functions and
// methods in Go source code.
//
// Usage:
//      gocyclo [<flag> ...] <Go file or directory> ...
//
// Flags:
//      -over N   show functions with complexity > N only and
//                return exit code 1 if the output is non-empty
//      -top N    show the top N most complex functions only
//      -avg      show the average complexity
//      -total    show the total complexity
//
// The output fields for each line are:
// <complexity> <package> <function> <file:row:column>
package main

import (
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const usageDoc = `Calculate cyclomatic complexities of Go functions.
Usage:
        gocyclo [flags] <Go file or directory> ...

Flags:
        -over N   show functions with complexity > N only and
                  return exit code 1 if the set is non-empty
        -top N    show the top N most complex functions only
        -avg      show the average complexity over all functions,
                  not depending on whether -over or -top are set
        -total    show the total complexity for all functions

The output fields for each line are:
<complexity> <package> <function> <file:row:column>
`

func usage() {
	fmt.Fprintf(os.Stderr, usageDoc)
	os.Exit(2)
}

var (
	over  = flag.Int("over", 0, "show functions with complexity > N only")
	top   = flag.Int("top", -1, "show the top N most complex functions only")
	avg   = flag.Bool("avg", false, "show the average complexity")
	total = flag.Bool("total", false, "show the total complexity")
)

func main() {
	log.SetFlags(0)
	log.SetPrefix("gocyclo: ")
	flag.Usage = usage
	flag.Parse()
	args := flag.Args()
	if len(args) == 0 {
		usage()
	}

	stats := analyze(args)
	sort.Sort(byComplexityDesc(stats))
	written := writeStats(os.Stdout, stats)

	if *avg {
		showAverage(stats)
	}

	if *total {
		showTotal(stats)
	}

	if *over > 0 && written > 0 {
		os.Exit(1)
	}
}

func analyze(paths []string) []stat {
	var stats []stat
	for _, path := range paths {
		if isDir(path) {
			stats = analyzeDir(path, stats)
		} else {
			stats = analyzeFile(path, stats)
		}
	}
	return stats
}

func isDir(filename string) bool {
	fi, err := os.Stat(filename)
	return err == nil && fi.IsDir()
}

func analyzeFile(fname string, stats []stat) []stat {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, fname, nil, parser.ParseComments)
	if err != nil {
		log.Fatal(err)
	}
	return buildStats(f, fset, stats)
}

func analyzeDir(dirname string, stats []stat) []stat {
	filepath.Walk(dirname, func(path string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() && strings.HasSuffix(path, ".go") {
			stats = analyzeFile(path, stats)
		}
		return err
	})
	return stats
}

func writeStats(w io.Writer, sortedStats []stat) int {
	for i, stat := range sortedStats {
		if i == *top {
			return i
		}
		if stat.Complexity <= *over {
			return i
		}
		fmt.Fprintln(w, stat)
	}
	return len(sortedStats)
}

func showAverage(stats []stat) {
	fmt.Printf("Average: %.3g\n", average(stats))
}

func average(stats []stat) float64 {
	total := 0
	for _, s := range stats {
		total += s.Complexity
	}
	return float64(total) / float64(len(stats))
}

func showTotal(stats []stat) {
	fmt.Printf("Total: %d\n", sumtotal(stats))
}

func sumtotal(stats []stat) uint64 {
	total := uint64(0)
	for _, s := range stats {
		total += uint64(s.Complexity)
	}
	return total
}

type stat struct {
	PkgName    string
	FuncName   string
	Complexity int
	Pos        token.Position
}

func (s stat) String() string {
	return fmt.Sprintf("%d %s %s %s", s.Complexity, s.PkgName, s.FuncName, s.Pos)
}

type byComplexityDesc []stat

func (s byComplexityDesc) Len() int      { return len(s) }
func (s byComplexityDesc) Swap(i, j int) { s[i], s[j] = s[j], s[i] }
func (s byComplexityDesc) Less(i, j int) bool {
	return s[i].Complexity >= s[j].Complexity
}

func buildStats(f *ast.File, fset *token.FileSet, stats []stat) []stat {
	for _, declaration := range f.Decls {
		switch decl := declaration.(type) {
		case *ast.FuncDecl:
			stats = addStatIfNotIgnored(stats, decl, funcName(decl), decl.Doc, f, fset)
		case *ast.GenDecl:
			for _, spec := range decl.Specs {
				valueSpec, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}
				for _, value := range valueSpec.Values {
					funcLit, ok := value.(*ast.FuncLit)
					if !ok {
						continue
					}
					stats = addStatIfNotIgnored(stats, funcLit, valueSpec.Names[0].Name, decl.Doc, f, fset)
				}
			}
		}
	}
	return stats
}

func addStatIfNotIgnored(stats []stat, funcNode ast.Node, funcName string, doc *ast.CommentGroup, f *ast.File, fset *token.FileSet) []stat {
	if parseDirectives(doc).HasIgnore() {
		return stats
	}
	return append(stats, statForFunc(funcNode, funcName, f, fset))
}

func statForFunc(funcNode ast.Node, funcName string, f *ast.File, fset *token.FileSet) stat {
	return stat{
		PkgName:    f.Name.Name,
		FuncName:   funcName,
		Complexity: complexity(funcNode),
		Pos:        fset.Position(funcNode.Pos()),
	}
}

// funcName returns the name representation of a function or method:
// "(Type).Name" for methods or simply "Name" for functions.
func funcName(fn *ast.FuncDecl) string {
	if fn.Recv != nil {
		if fn.Recv.NumFields() > 0 {
			typ := fn.Recv.List[0].Type
			return fmt.Sprintf("(%s).%s", recvString(typ), fn.Name)
		}
	}
	return fn.Name.Name
}

// recvString returns a string representation of recv of the
// form "T", "*T", or "BADRECV" (if not a proper receiver type).
func recvString(recv ast.Expr) string {
	switch t := recv.(type) {
	case *ast.Ident:
		return t.Name
	case *ast.StarExpr:
		return "*" + recvString(t.X)
	}
	return "BADRECV"
}

// complexity calculates the cyclomatic complexity of a function.
func complexity(fn ast.Node) int {
	v := complexityVisitor{}
	ast.Walk(&v, fn)
	return v.Complexity
}

type complexityVisitor struct {
	// Complexity is the cyclomatic complexity
	Complexity int
}

// Visit implements the ast.Visitor interface.
func (v *complexityVisitor) Visit(n ast.Node) ast.Visitor {
	switch n := n.(type) {
	case *ast.FuncDecl, *ast.IfStmt, *ast.ForStmt, *ast.RangeStmt, *ast.CaseClause, *ast.CommClause:
		v.Complexity++
	case *ast.BinaryExpr:
		if n.Op == token.LAND || n.Op == token.LOR {
			v.Complexity++
		}
	}
	return v
}

type directives []string

func (ds directives) HasIgnore() bool {
	return ds.isPresent("ignore")
}

func (ds directives) isPresent(name string) bool {
	for _, d := range ds {
		if d == name {
			return true
		}
	}
	return false
}

func parseDirectives(doc *ast.CommentGroup) directives {
	if doc == nil {
		return directives{}
	}
	const prefix = "//gocyclo:"
	var ds directives
	for _, comment := range doc.List {
		if strings.HasPrefix(comment.Text, prefix) {
			ds = append(ds, strings.TrimPrefix(comment.Text, prefix))
		}
	}
	return ds
}
