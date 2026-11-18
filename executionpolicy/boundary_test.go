package executionpolicy

import (
	"go/parser"
	"go/token"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// This architecture constraint guards against importing I/O or infrastructure
// capabilities into the pure evaluator. It examines production imports only.
func TestProductionImportBoundary(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(token.NewFileSet(), name, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, spec := range f.Imports {
			p, err := strconv.Unquote(spec.Path.Value)
			if err != nil {
				t.Fatal(err)
			}
			for _, forbidden := range []string{"github.com/", "os", "net", "io", "log", "syscall", "unsafe", "plugin"} {
				if p == forbidden || strings.HasPrefix(p, forbidden+"/") || strings.HasPrefix(p, "github.com/") {
					t.Fatalf("pure evaluator imports forbidden capability: %s", p)
				}
			}
		}
	}
}
