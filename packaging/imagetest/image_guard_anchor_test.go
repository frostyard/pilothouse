package imagetest

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestImageProcessGuardAnchor is compiled independently from the guarded test
// files. A build constraint or deletion therefore cannot silently remove the
// audit or its runtime proofs and turn a targeted go test invocation into a
// green run with no matching tests.
func TestImageProcessGuardAnchor(t *testing.T) {
	files := map[string][]string{
		"image_process_test.go": {
			"TestImageProcessAuditIsNotVacuous",
			"TestImageProcessDifferential",
			"TestImageProcessLiveAudit",
		},
		"image_child_test.go": {
			"TestImageChildDeadlineIsBounded",
			"TestImageChildDirectToolDeadlineIsBounded",
			"TestImageChildCleansDescendantAfterSuccessfulParent",
		},
	}

	for name, expected := range files {
		t.Run(name, func(t *testing.T) {
			imageAssertGuardedTests(t, name, expected)
		})
	}
}

func imageAssertGuardedTests(t *testing.T, name string, expected []string) {
	t.Helper()

	path := filepath.Join(".", name)
	set := token.NewFileSet()
	source, err := parser.ParseFile(set, path, nil, parser.ParseComments)
	require.NoError(t, err)

	for _, group := range source.Comments {
		if group.Pos() > source.Package {
			continue
		}
		for _, comment := range group.List {
			text := strings.TrimSpace(comment.Text)
			require.Falsef(t,
				strings.HasPrefix(text, "//go:build") || strings.HasPrefix(text, "// +build"),
				"%s must not carry a build constraint", path)
		}
	}

	found := map[string]bool{}
	for _, declaration := range source.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if ok && imageIsTopLevelGoTest(function) {
			found[function.Name.Name] = true
		}
	}

	for _, test := range expected {
		require.Truef(t, found[test],
			"%s must define top-level func %s(*testing.T)", path, test)
	}
}

func imageIsTopLevelGoTest(function *ast.FuncDecl) bool {
	if function.Recv != nil || function.Type.TypeParams != nil ||
		function.Type.Params.NumFields() != 1 ||
		function.Type.Results.NumFields() != 0 {
		return false
	}

	parameter := function.Type.Params.List[0]
	if len(parameter.Names) > 1 {
		return false
	}

	pointer, ok := parameter.Type.(*ast.StarExpr)
	if !ok {
		return false
	}
	selector, ok := pointer.X.(*ast.SelectorExpr)
	if !ok || selector.Sel.Name != "T" {
		return false
	}
	pkg, ok := selector.X.(*ast.Ident)
	return ok && pkg.Name == "testing"
}
