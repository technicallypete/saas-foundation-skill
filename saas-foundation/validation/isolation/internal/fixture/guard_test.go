package fixture

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
)

// TestGuard_ConnectionScope statically enforces which tests may use each
// privileged connection helper:
//
//	SuperuserSetup -> TestControl_* functions (or internal/fixture/seed.go)
//	OwnerTx        -> TestOwner_* functions
//	RetentionDB    -> TestRetention_* functions
func TestGuard_ConnectionScope(t *testing.T) {
	Label(t, Unit)
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	rules := map[string]string{"SuperuserSetup": "TestControl_", "OwnerTx": "TestOwner_", "RetentionDB": "TestRetention_"}
	fset := token.NewFileSet()
	checked := 0
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		if rel == "internal/fixture/db.go" {
			return nil // definitions, not uses
		}
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return err
		}
		checked++
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				name := calleeName(call.Fun)
				prefix, guarded := rules[name]
				if !guarded {
					return true
				}
				if name == "SuperuserSetup" && rel == "internal/fixture/seed.go" {
					return true
				}
				if !strings.HasPrefix(fn.Name.Name, prefix) {
					t.Errorf("%s: %s calls %s; only %s* functions may use it", rel, fn.Name.Name, name, prefix)
				}
				return true
			})
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if checked == 0 {
		t.Fatal("no Go files scanned")
	}
}

func calleeName(e ast.Expr) string {
	switch f := e.(type) {
	case *ast.Ident:
		return f.Name
	case *ast.SelectorExpr:
		return f.Sel.Name
	}
	return ""
}
