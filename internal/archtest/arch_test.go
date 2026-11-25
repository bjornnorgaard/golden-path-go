// Package archtest turns the repository's architectural conventions into failing tests.
// If a rule here and AGENTS.md ever disagree, this test wins.
package archtest

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
)

const modulePath = "github.com/bjornnorgaard/golden-path-go"

type file struct {
	path      string // repo-relative, slash separated
	pkgDir    string // repo-relative directory
	imports   []string
	generated bool
	ast       *ast.File
}

func repoFiles(t *testing.T) []file {
	t.Helper()
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	var out []file
	err = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && (d.Name() == ".git" || d.Name() == "node_modules" || d.Name() == "vendor") {
			return filepath.SkipDir
		}
		if d.IsDir() || !strings.HasSuffix(p, ".go") {
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		rel = filepath.ToSlash(rel)
		f, err := parser.ParseFile(token.NewFileSet(), p, nil, parser.ParseComments|parser.ImportsOnly)
		if err != nil {
			return err
		}
		full, err := parser.ParseFile(token.NewFileSet(), p, nil, parser.ParseComments)
		if err != nil {
			return err
		}
		fl := file{path: rel, pkgDir: filepath.ToSlash(filepath.Dir(rel)), ast: full, generated: ast.IsGenerated(f)}
		for _, imp := range f.Imports {
			fl.imports = append(fl.imports, strings.Trim(imp.Path.Value, `"`))
		}
		out = append(out, fl)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func importsPrefix(f file, prefixes ...string) (string, bool) {
	for _, imp := range f.imports {
		for _, p := range prefixes {
			if imp == p || strings.HasPrefix(imp, p+"/") {
				return imp, true
			}
		}
	}
	return "", false
}

func in(f file, dirs ...string) bool {
	for _, d := range dirs {
		if f.pkgDir == d || strings.HasPrefix(f.pkgDir, d+"/") {
			return true
		}
	}
	return false
}

func isTest(f file) bool { return strings.HasSuffix(f.path, "_test.go") }

// Business logic never depends on a transport: REST and GraphQL are adapters over services,
// not the other way around, so rules cannot leak into (or be duplicated by) a transport.
func TestServicesDoNotDependOnTransports(t *testing.T) {
	for _, f := range repoFiles(t) {
		if isTest(f) || !in(f, "internal/todos", "internal/users", "internal/jobs", "internal/domain", "internal/paging", "internal/id", "internal/db") {
			continue
		}
		if imp, bad := importsPrefix(f, modulePath+"/internal/transport", modulePath+"/internal/app", "net/http"); bad {
			t.Errorf("%s imports %s: services must not depend on transports or HTTP", f.path, imp)
		}
	}
}

// Transports reach data only through services, so every rule is applied on every path.
func TestTransportsDoNotBypassServices(t *testing.T) {
	for _, f := range repoFiles(t) {
		if isTest(f) || !in(f, "internal/transport") {
			continue
		}
		if imp, bad := importsPrefix(f, modulePath+"/internal/db", "github.com/jackc/pgx"); bad {
			t.Errorf("%s imports %s: transports must call services, not the database", f.path, imp)
		}
	}
}

// Configuration is read in exactly one place.
func TestEnvironmentIsOnlyReadByConfig(t *testing.T) {
	for _, f := range repoFiles(t) {
		if isTest(f) || in(f, "internal/config", "cmd") {
			continue
		}
		ast.Inspect(f.ast, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if x, ok := sel.X.(*ast.Ident); ok && x.Name == "os" && (sel.Sel.Name == "Getenv" || sel.Sel.Name == "LookupEnv" || sel.Sel.Name == "Environ") {
				t.Errorf("%s reads the environment via os.%s: add the setting to internal/config instead", f.path, sel.Sel.Name)
			}
			return true
		})
	}
}

// No init(): wiring is explicit in internal/app, never a side effect of importing a package.
func TestNoInitFunctions(t *testing.T) {
	for _, f := range repoFiles(t) {
		if f.generated {
			continue
		}
		for _, d := range f.ast.Decls {
			if fn, ok := d.(*ast.FuncDecl); ok && fn.Recv == nil && fn.Name.Name == "init" {
				t.Errorf("%s declares init(): wire dependencies explicitly in internal/app", f.path)
			}
		}
	}
}

// Generated code is clearly marked and never hand-edited: only *.gen.go / sqlc output may carry the header,
// and every *.gen.go must carry it.
func TestGeneratedFilesAreMarked(t *testing.T) {
	for _, f := range repoFiles(t) {
		isGenName := strings.HasSuffix(f.path, ".gen.go") || strings.HasPrefix(f.path, "internal/db/store/")
		switch {
		case isGenName && !f.generated:
			t.Errorf("%s looks generated but has no \"Code generated ... DO NOT EDIT.\" header: do not hand-write files with generated names", f.path)
		case !isGenName && f.generated:
			t.Errorf("%s carries a generated header but is not a recognised generated file name", f.path)
		}
	}
}

// Hand-written code must not call os.Exit or panic on request paths; main is the only place that exits.
func TestOnlyMainExits(t *testing.T) {
	for _, f := range repoFiles(t) {
		if f.generated || isTest(f) || in(f, "cmd", "tools") {
			continue
		}
		ast.Inspect(f.ast, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
				if x, ok := sel.X.(*ast.Ident); ok && x.Name == "os" && sel.Sel.Name == "Exit" {
					t.Errorf("%s calls os.Exit: return an error instead", f.path)
				}
			}
			return true
		})
	}
}
