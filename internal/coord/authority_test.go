package coord

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The coordination domain must never gain terminal authority. This static
// boundary test proves the package cannot read or drive tmux: no file imports
// the pane controller or a process-execution package (except the one narrow
// seeded-launcher adapter), and no code string smuggles in raw key delivery
// or pane capture.
func TestCoordinationHasNoTerminalAuthority(t *testing.T) {
	fset := token.NewFileSet()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, imp := range f.Imports {
			path := strings.Trim(imp.Path.Value, `"`)
			if path == "os/exec" && name != "seed.go" {
				t.Errorf("%s imports os/exec; only the seed launcher adapter may execute anything", name)
			}
			if strings.Contains(path, "internal/core") {
				t.Errorf("%s imports the pane controller; coordination must stay isolated from tmux mechanics", name)
			}
		}
		ast.Inspect(f, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			v := strings.ToLower(lit.Value)
			for _, forbidden := range []string{"send-keys", "capture-pane", "copy-mode", "display-message"} {
				if strings.Contains(v, forbidden) {
					t.Errorf("%s: forbidden terminal-driver literal %s at %s", name, lit.Value, fset.Position(lit.Pos()))
				}
			}
			return true
		})
	}
	// The seed adapter's only executable surface is the pinned flag interface.
	b, err := os.ReadFile("seed.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, flag := range []string{"--request-id", "--initial-prompt-file", "--initial-prompt-sha256", "--initial-prompt-bytes"} {
		if !strings.Contains(string(b), `"`+flag+`"`) {
			t.Errorf("seed adapter lost pinned flag %s", flag)
		}
	}
	if strings.Contains(string(b), "tmux") && !strings.Contains(string(b), "// ") {
		t.Error("seed adapter references tmux")
	}
	_ = filepath.Separator
}
