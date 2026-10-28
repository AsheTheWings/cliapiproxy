//go:build conformance

package inputs

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// processInputReads are the calls that read process inputs. Only the entry
// point and this package may make them.
var processInputReads = map[string]map[string]bool{
	"os": {"Getenv": true, "LookupEnv": true, "Environ": true, "ExpandEnv": true, "Args": true,
		"UserHomeDir": true, "UserConfigDir": true, "UserCacheDir": true},
	"syscall": {"Getenv": true, "Environ": true},
	"flag": {"Parse": true, "CommandLine": true, "Args": true, "Arg": true, "NArg": true,
		"Bool": true, "BoolVar": true, "String": true, "StringVar": true, "Int": true, "IntVar": true,
		"Int64": true, "Int64Var": true, "Uint": true, "UintVar": true, "Duration": true, "DurationVar": true,
		"Float64": true, "Float64Var": true, "Func": true, "Var": true},
	"github.com/joho/godotenv": nil,
}

var inputReaders = []string{"cmd/server/", "internal/inputs/"}

func TestOnlyTheEntryPointReadsProcessInputs(t *testing.T) {
	root := moduleRoot(t)
	errWalk := filepath.WalkDir(root, func(path string, d fs.DirEntry, errEntry error) error {
		if errEntry != nil {
			return errEntry
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if rel != "." && (strings.HasPrefix(d.Name(), ".") || d.Name() == "testdata" || d.Name() == "testsupport") {
				return filepath.SkipDir
			}
			if _, errMod := os.Stat(filepath.Join(path, "go.mod")); rel != "." && errMod == nil {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(rel, ".go") || strings.HasSuffix(rel, "_test.go") {
			return nil
		}
		for _, allowed := range inputReaders {
			if strings.HasPrefix(rel, allowed) {
				return nil
			}
		}
		checkFile(t, path, rel)
		return nil
	})
	if errWalk != nil {
		t.Fatal(errWalk)
	}
}

func checkFile(t *testing.T, path, rel string) {
	t.Helper()
	fset := token.NewFileSet()
	file, errParse := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
	if errParse != nil {
		t.Fatalf("%s: %v", rel, errParse)
	}
	names := map[string]string{}
	for _, imp := range file.Imports {
		importPath, _ := strconv.Unquote(imp.Path.Value)
		reads, watched := processInputReads[importPath]
		if !watched {
			continue
		}
		if reads == nil {
			t.Errorf("%s imports %s", rel, importPath)
			continue
		}
		name := filepath.Base(importPath)
		if imp.Name != nil {
			name = imp.Name.Name
		}
		names[name] = importPath
	}
	ast.Inspect(file, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		pkg, ok := sel.X.(*ast.Ident)
		if !ok {
			return true
		}
		if importPath, imported := names[pkg.Name]; imported && processInputReads[importPath][sel.Sel.Name] {
			t.Errorf("%s: %s.%s reads a process input outside the entry point", fset.Position(sel.Pos()), pkg.Name, sel.Sel.Name)
		}
		return true
	})
}
