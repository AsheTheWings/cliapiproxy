// Package testclass classifies the repository's Go tests by the boundary they
// cross, so a conformance test can keep every test file's declared class at
// least as wide as what the file does.
//
// Each test file declares its class with a build constraint naming exactly
// one of unit, component, conformance, or integration:
//
//   - unit: pure code over in-memory values, with no filesystem, sockets,
//     processes, process configuration, or wall-clock waits;
//   - component: in-process code that may use run-owned temporary files,
//     loopback sockets, the process environment, and bounded waits;
//   - conformance: read-only checks of the source tree's invariants;
//   - integration: code that starts processes or needs real network
//     interfaces.
//
// The order unit, component, integration is a widening: a file may declare a
// wider class than its use requires. A file's use includes what the test
// files it references in the same directory do.
package testclass

import (
	"fmt"
	"go/ast"
	"go/build/constraint"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// Class is a test class a build constraint can name.
type Class string

// The test classes, from narrowest to widest boundary; Conformance stands apart.
const (
	Unit          Class = "unit"
	Component     Class = "component"
	Integration   Class = "integration"
	Qualification Class = "qualification"
	Conformance   Class = "conformance"
)

// Classes lists every class a build constraint may name.
var Classes = []Class{Unit, Component, Conformance, Integration, Qualification}

// rank orders the widening classes; Conformance has no rank.
var rank = map[Class]int{Unit: 0, Component: 1, Integration: 2, Qualification: 3}

// Covers reports whether a file declaring c may hold tests that require r.
func (c Class) Covers(r Class) bool {
	if c == Conformance || r == Conformance {
		return c == r
	}
	return rank[c] >= rank[r]
}

// Use names a boundary a test file crosses.
type Use string

// The boundaries the classifier detects.
const (
	UseFilesystem Use = "filesystem"
	UseSocket     Use = "socket"
	UseProcessEnv Use = "process configuration"
	UseWallClock  Use = "wall-clock wait"
	UseProcess    Use = "process"
	UseNetwork    Use = "network interfaces"
	UseSource     Use = "source tree"
)

// FixtureLatency marks a time.Sleep in a test fake that must outlast a real
// deadline, such as a reply that arrives after a read timeout.
const FixtureLatency = "// fixture latency:"

// File describes one _test.go file.
type File struct {
	Path     string // slash-separated, relative to the analyzed root
	Dir      string
	Tests    int // Test, Benchmark, Fuzz, and Example functions
	Declared []Class
	Own      map[Use][]string // uses, each with the positions or calls behind it
	Uses     map[Use]bool     // own uses plus those of referenced test files
	Sleeps   []string         // time.Sleep calls outside synctest bubbles and fixture latency
	decls    map[string]bool
	refs     map[string]bool
}

// Required is the narrowest class covering the file's uses.
func (f *File) Required() Class {
	switch {
	case f.Uses[UseProcess] || f.Uses[UseNetwork]:
		return Integration
	case f.Own[UseSource] != nil:
		return Conformance
	case f.Uses[UseFilesystem] || f.Uses[UseSocket] || f.Uses[UseProcessEnv] || f.Uses[UseWallClock]:
		return Component
	default:
		return Unit
	}
}

// Analyze parses every _test.go file below root, skipping .git, testdata, and
// vendor directories.
func Analyze(root string) ([]*File, error) {
	var files []*File
	errWalk := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "testdata", "vendor":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, errRel := filepath.Rel(root, path)
		if errRel != nil {
			return errRel
		}
		file, errParse := parseFile(path, filepath.ToSlash(rel))
		if errParse != nil {
			return errParse
		}
		files = append(files, file)
		return nil
	})
	if errWalk != nil {
		return nil, errWalk
	}
	propagate(files)
	return files, nil
}

// propagate adds to each file the uses of the test files it references in
// its directory, transitively.
func propagate(files []*File) {
	byDir := make(map[string][]*File)
	for _, f := range files {
		byDir[f.Dir] = append(byDir[f.Dir], f)
	}
	for _, group := range byDir {
		deps := make(map[*File][]*File)
		for _, f := range group {
			for _, g := range group {
				if f == g {
					continue
				}
				for name := range g.decls {
					if f.refs[name] {
						deps[f] = append(deps[f], g)
						break
					}
				}
			}
		}
		for _, f := range group {
			seen := map[*File]bool{f: true}
			queue := []*File{f}
			for len(queue) > 0 {
				g := queue[0]
				queue = queue[1:]
				for use := range g.Own {
					if use != UseSource {
						f.Uses[use] = true
					}
				}
				for _, h := range deps[g] {
					if !seen[h] {
						seen[h] = true
						queue = append(queue, h)
					}
				}
			}
		}
	}
}

var (
	filesystemCalls = set("os.Create", "os.CreateTemp", "os.Mkdir", "os.MkdirAll", "os.MkdirTemp", "os.Open",
		"os.OpenFile", "os.ReadDir", "os.ReadFile", "os.Remove", "os.RemoveAll", "os.Rename", "os.Stat", "os.Lstat",
		"os.WriteFile", "os.Chmod", "os.Chown", "os.Chtimes", "os.Symlink", "os.Readlink", "os.Truncate", "os.DirFS",
		"os.Link", "os.TempDir", "os.UserHomeDir", "os.UserConfigDir", "os.UserCacheDir", "path/filepath.Walk",
		"path/filepath.WalkDir", "path/filepath.Glob", "path/filepath.Abs", "path/filepath.EvalSymlinks")
	socketCalls = set("net.Listen", "net.ListenTCP", "net.ListenUDP", "net.ListenPacket", "net.Dial", "net.DialTCP",
		"net.DialUDP", "net.DialTimeout", "net.Dialer", "net.ListenConfig", "net.ResolveTCPAddr", "net.ResolveUDPAddr",
		"net.LookupHost", "net.LookupIP", "net.DefaultResolver", "net/http/httptest.NewServer",
		"net/http/httptest.NewTLSServer", "net/http/httptest.NewUnstartedServer", "net/http.Get", "net/http.Post",
		"net/http.Head", "net/http.PostForm", "net/http.ListenAndServe", "net/http.Serve", "net/http.DefaultClient",
		"net/http.DefaultTransport", "crypto/tls.Dial", "crypto/tls.Listen", "github.com/gorilla/websocket.DefaultDialer",
		"github.com/gorilla/websocket.Dialer")
	processEnvCalls = set("os.Getenv", "os.LookupEnv", "os.Setenv", "os.Unsetenv", "os.Environ", "os.Clearenv",
		"os.ExpandEnv", "os.Getwd", "os.Chdir", "os.Hostname", "os.Executable", "os.Getpid", "os.Getuid", "os.Args")
	processCalls   = set("os.StartProcess", "os.FindProcess", "syscall.Exec", "syscall.ForkExec")
	wallClockCalls = set("time.Tick", "time.NewTicker", "time.AfterFunc")
	// Method names on testing.T that reach the filesystem or process configuration.
	filesystemMethods = set("TempDir")
	processEnvMethods = set("Setenv", "Chdir")
)

const waitPackage = "github.com/AsheTheWings/cliapiproxy/internal/testsupport/wait"

func parseFile(path, rel string) (*File, error) {
	fset := token.NewFileSet()
	syntax, errParse := parser.ParseFile(fset, path, nil, parser.ParseComments)
	if errParse != nil {
		return nil, errParse
	}
	f := &File{
		Path:  rel,
		Dir:   filepath.ToSlash(filepath.Dir(rel)),
		Own:   make(map[Use][]string),
		Uses:  make(map[Use]bool),
		decls: make(map[string]bool),
		refs:  make(map[string]bool),
	}
	declared, errConstraint := declaredClasses(syntax)
	if errConstraint != nil {
		return nil, fmt.Errorf("%s: %w", rel, errConstraint)
	}
	f.Declared = declared

	imports := make(map[string]string)
	for _, spec := range syntax.Imports {
		importPath, _ := strconv.Unquote(spec.Path.Value)
		name := importPath[strings.LastIndex(importPath, "/")+1:]
		if spec.Name != nil {
			name = spec.Name.Name
		}
		imports[name] = importPath
		switch {
		case importPath == "os/exec":
			f.add(UseProcess, importPath)
		case strings.HasPrefix(importPath, "github.com/pion/"):
			f.add(UseNetwork, importPath)
		case importPath == "go/parser" || importPath == "go/build" || importPath == "golang.org/x/tools/go/packages":
			f.add(UseSource, importPath)
		case importPath == "io/ioutil":
			f.add(UseFilesystem, importPath)
		case importPath == waitPackage:
			f.add(UseWallClock, importPath)
		}
	}
	collectDecls(f, syntax)

	lines := fixtureLatencyLines(fset, syntax)
	var visit func(n ast.Node, inBubble bool)
	visit = func(n ast.Node, inBubble bool) {
		ast.Inspect(n, func(node ast.Node) bool {
			switch node := node.(type) {
			case *ast.Ident:
				if !f.decls[node.Name] {
					f.refs[node.Name] = true
				}
			case *ast.SelectorExpr:
				f.classifySelector(node, imports, fset)
			case *ast.BasicLit:
				// Locating go.mod means the test reads the source tree.
				if node.Kind == token.STRING && node.Value == `"go.mod"` {
					f.add(UseSource, "go.mod")
				}
			case *ast.CallExpr:
				callee := selectorPath(node.Fun, imports)
				if callee == "testing/synctest.Test" || callee == "testing/synctest.Run" {
					visit(node.Fun, inBubble)
					for _, arg := range node.Args {
						visit(arg, true)
					}
					return false
				}
				if callee == "time.Sleep" && !inBubble {
					pos := fset.Position(node.Pos())
					if !lines[pos.Line] {
						f.Sleeps = append(f.Sleeps, fmt.Sprintf("%s:%d", rel, pos.Line))
						f.add(UseWallClock, fmt.Sprintf("%s:%d", rel, pos.Line))
					} else {
						f.add(UseWallClock, fmt.Sprintf("%s:%d fixture latency", rel, pos.Line))
					}
				}
			}
			return true
		})
	}
	for _, decl := range syntax.Decls {
		visit(decl, false)
	}
	return f, nil
}

func (f *File) add(use Use, evidence string) {
	f.Own[use] = append(f.Own[use], evidence)
	if use != UseSource {
		f.Uses[use] = true
	}
}

func (f *File) classifySelector(sel *ast.SelectorExpr, imports map[string]string, fset *token.FileSet) {
	if callee := selectorPath(sel, imports); callee != "" {
		switch {
		case filesystemCalls[callee]:
			f.add(UseFilesystem, callee)
		case socketCalls[callee]:
			f.add(UseSocket, callee)
		case processEnvCalls[callee]:
			f.add(UseProcessEnv, callee)
		case processCalls[callee]:
			f.add(UseProcess, callee)
		case wallClockCalls[callee]:
			f.add(UseWallClock, callee)
		}
		return
	}
	if _, isIdent := sel.X.(*ast.Ident); !isIdent {
		return
	}
	pos := fmt.Sprintf("%s:%d", f.Path, fset.Position(sel.Pos()).Line)
	switch {
	case filesystemMethods[sel.Sel.Name]:
		f.add(UseFilesystem, pos+" "+sel.Sel.Name)
	case processEnvMethods[sel.Sel.Name]:
		f.add(UseProcessEnv, pos+" "+sel.Sel.Name)
	}
}

// selectorPath returns "<import path>.<name>" for a selector on an imported
// package, or "" for any other expression.
func selectorPath(expr ast.Expr, imports map[string]string) string {
	sel, ok := expr.(*ast.SelectorExpr)
	if !ok {
		return ""
	}
	x, ok := sel.X.(*ast.Ident)
	if !ok || x.Obj != nil {
		return ""
	}
	importPath, ok := imports[x.Name]
	if !ok {
		return ""
	}
	return importPath + "." + sel.Sel.Name
}

func collectDecls(f *File, syntax *ast.File) {
	for _, decl := range syntax.Decls {
		switch decl := decl.(type) {
		case *ast.FuncDecl:
			if decl.Recv != nil {
				continue
			}
			name := decl.Name.Name
			f.decls[name] = true
			for _, prefix := range []string{"Test", "Benchmark", "Fuzz", "Example"} {
				if strings.HasPrefix(name, prefix) && name != "TestMain" {
					f.Tests++
					break
				}
			}
		case *ast.GenDecl:
			for _, spec := range decl.Specs {
				switch spec := spec.(type) {
				case *ast.TypeSpec:
					f.decls[spec.Name.Name] = true
				case *ast.ValueSpec:
					for _, name := range spec.Names {
						if name.Name != "_" {
							f.decls[name.Name] = true
						}
					}
				}
			}
		}
	}
}

// fixtureLatencyLines returns the lines that carry a FixtureLatency comment.
func fixtureLatencyLines(fset *token.FileSet, syntax *ast.File) map[int]bool {
	lines := make(map[int]bool)
	for _, group := range syntax.Comments {
		for _, comment := range group.List {
			if strings.HasPrefix(comment.Text, FixtureLatency) {
				lines[fset.Position(comment.Pos()).Line] = true
			}
		}
	}
	return lines
}

// declaredClasses returns the classes the file's //go:build constraint names.
func declaredClasses(syntax *ast.File) ([]Class, error) {
	for _, group := range syntax.Comments {
		if group.Pos() >= syntax.Package {
			break
		}
		for _, comment := range group.List {
			if !constraint.IsGoBuild(comment.Text) {
				continue
			}
			expr, errParse := constraint.Parse(comment.Text)
			if errParse != nil {
				return nil, errParse
			}
			names := make(map[Class]bool)
			collectTags(expr, names)
			var declared []Class
			for _, class := range Classes {
				if names[class] {
					declared = append(declared, class)
				}
			}
			return declared, nil
		}
	}
	return nil, nil
}

func collectTags(expr constraint.Expr, names map[Class]bool) {
	switch expr := expr.(type) {
	case *constraint.TagExpr:
		names[Class(expr.Tag)] = true
	case *constraint.AndExpr:
		collectTags(expr.X, names)
		collectTags(expr.Y, names)
	case *constraint.OrExpr:
		collectTags(expr.X, names)
		collectTags(expr.Y, names)
	case *constraint.NotExpr:
		collectTags(expr.X, names)
	}
}

func set(values ...string) map[string]bool {
	m := make(map[string]bool, len(values))
	for _, v := range values {
		m[v] = true
	}
	return m
}

// Evidence lists, in a stable order, what backs each of the file's own uses.
func (f *File) Evidence() []string {
	var out []string
	for use, items := range f.Own {
		sort.Strings(items)
		out = append(out, fmt.Sprintf("%s: %s", use, strings.Join(items, ", ")))
	}
	sort.Strings(out)
	return out
}
