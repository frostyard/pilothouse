package imagetest

import (
	"fmt"
	"go/ast"
	"go/build/constraint"
	"go/constant"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

const imageSourceGlob = "*.go"

type imageFinding struct {
	File   string
	Line   int
	Rule   string
	Detail string
}

type imageParsedFile struct {
	path    string
	name    string
	set     *token.FileSet
	file    *ast.File
	imports map[string]string
}

func (f imageParsedFile) finding(node ast.Node, rule, detail string) imageFinding {
	line := 1
	if node != nil {
		line = f.set.Position(node.Pos()).Line
	}

	return imageFinding{
		File:   f.name,
		Line:   line,
		Rule:   rule,
		Detail: detail,
	}
}

type imageCallUse struct {
	file     imageParsedFile
	selector *ast.SelectorExpr
	call     *ast.CallExpr
	assign   *ast.AssignStmt
	function *ast.FuncDecl
	nested   bool
}

type imageTimeoutDecl struct {
	file imageParsedFile
	node *ast.ValueSpec
	expr ast.Expr
}

type imageAudit struct {
	findings        []imageFinding
	commandContext  []imageCallUse
	withTimeout     []imageCallUse
	timeoutDecls    []imageTimeoutDecl
	syscallCounts   map[string]int
	processFields   int
	groupKillCalls  int
	groupKillRefs   int
	groupKillNested int
	runners         []struct {
		file imageParsedFile
		decl *ast.FuncDecl
	}
}

func imageAuditDir(dir string) ([]imageFinding, error) {
	packagePaths, err := filepath.Glob(filepath.Join(dir, imageSourceGlob))
	if err != nil {
		return nil, fmt.Errorf("glob %s: %w", imageSourceGlob, err)
	}
	if len(packagePaths) == 0 {
		return nil, fmt.Errorf("no file matches %s in %s", imageSourceGlob, dir)
	}

	audit := imageAudit{syscallCounts: map[string]int{}}

	for _, path := range packagePaths {
		set := token.NewFileSet()
		file, err := parser.ParseFile(set, path, nil, parser.ParseComments)
		if err != nil {
			return nil, fmt.Errorf("parse %s: %w", path, err)
		}

		parsed := imageParsedFile{
			path:    path,
			name:    filepath.Base(path),
			set:     set,
			file:    file,
			imports: imageImports(file),
		}
		audit.inspectFile(parsed)
	}

	audit.finish()

	sort.Slice(audit.findings, func(i, j int) bool {
		left, right := audit.findings[i], audit.findings[j]
		if left.File != right.File {
			return left.File < right.File
		}
		if left.Line != right.Line {
			return left.Line < right.Line
		}
		if left.Rule != right.Rule {
			return left.Rule < right.Rule
		}

		return left.Detail < right.Detail
	})

	return audit.findings, nil
}

func imageImports(file *ast.File) map[string]string {
	imports := map[string]string{}

	for _, spec := range file.Imports {
		path, err := strconv.Unquote(spec.Path.Value)
		if err != nil {
			continue
		}

		name := filepath.Base(path)
		if spec.Name != nil {
			name = spec.Name.Name
		}

		imports[name] = path
	}

	return imports
}

func (a *imageAudit) inspectFile(file imageParsedFile) {
	a.inspectBuildConstraints(file)
	a.inspectImports(file)
	a.inspectImportShadows(file)
	a.inspectTimeoutDecls(file)

	for _, decl := range file.file.Decls {
		function, ok := decl.(*ast.FuncDecl)
		if ok && function.Name.Name == "imageRunChild" {
			a.runners = append(a.runners, struct {
				file imageParsedFile
				decl *ast.FuncDecl
			}{file: file, decl: function})
		}
	}

	var stack []ast.Node

	ast.Inspect(file.file, func(node ast.Node) bool {
		if node == nil {
			stack = stack[:len(stack)-1]

			return true
		}

		stack = append(stack, node)

		if identifier, ok := node.(*ast.Ident); ok && identifier.Name == "imageKillProcessGroup" {
			parentIsDeclaration := false
			if len(stack) >= 2 {
				function, ok := stack[len(stack)-2].(*ast.FuncDecl)
				parentIsDeclaration = ok && function.Name == identifier
			}
			if !parentIsDeclaration {
				a.groupKillRefs++
			}
		}

		if call, ok := node.(*ast.CallExpr); ok {
			identifier, direct := call.Fun.(*ast.Ident)
			if direct && identifier.Name == "imageKillProcessGroup" {
				a.groupKillCalls++
				if imageInsideFunctionLiteral(stack) {
					a.groupKillNested++
				}
				if !imageValidProcessGroupCall(stack, call) {
					a.findings = append(a.findings,
						file.finding(call, "process-group-call", "want imageKillProcessGroup(command.Process.Pid)"))
				}
			}
		}

		selector, ok := node.(*ast.SelectorExpr)
		if !ok {
			return true
		}

		identifier, ok := selector.X.(*ast.Ident)
		if !ok {
			return true
		}

		importPath, ok := file.imports[identifier.Name]
		if !ok {
			if selector.Sel.Name == "SysProcAttr" {
				a.processFields++
			}

			return true
		}

		switch importPath {
		case "os/exec":
			a.inspectExecSelector(file, stack, selector)
		case "os":
			if selector.Sel.Name == "StartProcess" {
				a.findings = append(a.findings,
					file.finding(selector, "os-start-process", imageSelectorUse(stack, selector)))
			}
		case "context":
			if selector.Sel.Name == "WithTimeout" {
				a.inspectWithTimeout(file, stack, selector)
			}
		case "syscall":
			a.inspectSyscallSelector(file, stack, selector)
		}

		return true
	})
}

func imageValidProcessGroupCall(stack []ast.Node, call *ast.CallExpr) bool {
	function := imageEnclosingFunction(stack)
	if function == nil || function.Name.Name != "imageRunChild" || len(call.Args) != 1 {
		return false
	}
	pid, ok := call.Args[0].(*ast.SelectorExpr)
	if !ok || pid.Sel.Name != "Pid" {
		return false
	}
	process, ok := pid.X.(*ast.SelectorExpr)
	if !ok || process.Sel.Name != "Process" {
		return false
	}
	command, ok := process.X.(*ast.Ident)
	if !ok || command.Name != "command" {
		return false
	}
	if imageInsideFunctionLiteral(stack) && !imageInsideCommandCancel(stack) {
		return false
	}

	return true
}

func imageInsideCommandCancel(stack []ast.Node) bool {
	var literal *ast.FuncLit
	for _, node := range stack {
		if function, ok := node.(*ast.FuncLit); ok {
			literal = function
		}
	}
	if literal == nil {
		return false
	}
	for _, node := range stack {
		assignment, ok := node.(*ast.AssignStmt)
		if !ok || assignment.Tok != token.ASSIGN ||
			len(assignment.Lhs) != 1 || len(assignment.Rhs) != 1 || assignment.Rhs[0] != literal {
			continue
		}
		target, ok := assignment.Lhs[0].(*ast.SelectorExpr)
		if !ok || target.Sel.Name != "Cancel" {
			return false
		}
		command, ok := target.X.(*ast.Ident)

		return ok && command.Name == "command"
	}

	return false
}

func (a *imageAudit) inspectBuildConstraints(file imageParsedFile) {
	for _, group := range file.file.Comments {
		if group.Pos() > file.file.Package {
			continue
		}

		for _, comment := range group.List {
			text := strings.TrimSpace(comment.Text)
			if !strings.HasPrefix(text, "//go:build") && !strings.HasPrefix(text, "// +build") {
				continue
			}

			detail := "valid"
			if _, err := constraint.Parse(text); err != nil {
				detail = "invalid: " + err.Error()
			}

			a.findings = append(a.findings,
				file.finding(comment, "build-constraint", detail))
		}
	}
}

func (a *imageAudit) inspectImports(file imageParsedFile) {
	allowedImports := map[string]bool{
		"context":                             true,
		"crypto/sha256":                       true,
		"errors":                              true,
		"fmt":                                 true,
		"go/ast":                              true,
		"go/build/constraint":                 true,
		"go/constant":                         true,
		"go/parser":                           true,
		"go/token":                            true,
		"io":                                  true,
		"io/fs":                               true,
		"os":                                  true,
		"os/exec":                             true,
		"path/filepath":                       true,
		"sort":                                true,
		"strconv":                             true,
		"strings":                             true,
		"sync/atomic":                         true,
		"syscall":                             true,
		"testing":                             true,
		"time":                                true,
		"github.com/stretchr/testify/require": true,
	}

	for _, spec := range file.file.Imports {
		path, err := strconv.Unquote(spec.Path.Value)
		if err != nil {
			continue
		}

		local := filepath.Base(path)
		if spec.Name != nil {
			local = spec.Name.Name
		}

		switch {
		case path == "C" || strings.HasPrefix(path, "golang.org/x/sys/"):
			a.findings = append(a.findings,
				file.finding(spec, "forbidden-import", path))
		case !allowedImports[path]:
			a.findings = append(a.findings,
				file.finding(spec, "image-import", path))
		case path == "os" && local == ".":
			a.findings = append(a.findings,
				file.finding(spec, "dot-os-import", path))
		case path == "os/exec":
			if file.name != "image_child_test.go" || local != "exec" || spec.Name != nil {
				a.findings = append(a.findings,
					file.finding(spec, "restricted-import", path+" as "+local))
			}
		case path == "context":
			if file.name != "image_child_test.go" || local != "context" || spec.Name != nil {
				a.findings = append(a.findings,
					file.finding(spec, "restricted-import", path+" as "+local))
			}
		case path == "syscall":
			if file.name != "image_child_test.go" || local != "syscall" || spec.Name != nil {
				a.findings = append(a.findings,
					file.finding(spec, "restricted-import", path+" as "+local))
			}
		}
	}
}

func (a *imageAudit) inspectImportShadows(file imageParsedFile) {
	restricted := map[string]bool{
		"context": true,
		"exec":    true,
		"error":   true,
		"false":   true,
		"int":     true,
		"nil":     true,
		"os":      true,
		"string":  true,
		"syscall": true,
		"true":    true,
	}

	record := func(identifier *ast.Ident) {
		if identifier != nil && restricted[identifier.Name] {
			a.findings = append(a.findings,
				file.finding(identifier, "restricted-name-shadow", identifier.Name))
		}
	}
	recordFields := func(fields *ast.FieldList) {
		if fields == nil {
			return
		}
		for _, field := range fields.List {
			for _, identifier := range field.Names {
				record(identifier)
			}
		}
	}

	ast.Inspect(file.file, func(node ast.Node) bool {
		switch typed := node.(type) {
		case *ast.AssignStmt:
			if typed.Tok == token.DEFINE {
				for _, left := range typed.Lhs {
					identifier, _ := left.(*ast.Ident)
					record(identifier)
				}
			}
		case *ast.ValueSpec:
			for _, identifier := range typed.Names {
				record(identifier)
			}
		case *ast.TypeSpec:
			record(typed.Name)
		case *ast.RangeStmt:
			if typed.Tok == token.DEFINE {
				for _, expression := range []ast.Expr{typed.Key, typed.Value} {
					identifier, _ := expression.(*ast.Ident)
					record(identifier)
				}
			}
		case *ast.FuncDecl:
			record(typed.Name)
			recordFields(typed.Recv)
			recordFields(typed.Type.Params)
			recordFields(typed.Type.Results)
		case *ast.FuncLit:
			recordFields(typed.Type.Params)
			recordFields(typed.Type.Results)
		}

		return true
	})
}

func (a *imageAudit) inspectTimeoutDecls(file imageParsedFile) {
	for _, decl := range file.file.Decls {
		general, ok := decl.(*ast.GenDecl)
		if !ok || general.Tok != token.CONST {
			continue
		}

		for _, item := range general.Specs {
			value, ok := item.(*ast.ValueSpec)
			if !ok {
				continue
			}

			for index, name := range value.Names {
				if name.Name != "imageProcessTimeout" {
					continue
				}

				var expression ast.Expr
				switch {
				case index < len(value.Values):
					expression = value.Values[index]
				case len(value.Values) == 1:
					expression = value.Values[0]
				}

				a.timeoutDecls = append(a.timeoutDecls, imageTimeoutDecl{
					file: file,
					node: value,
					expr: expression,
				})
			}
		}
	}
}

func (a *imageAudit) inspectExecSelector(file imageParsedFile, stack []ast.Node, selector *ast.SelectorExpr) {
	use := imageSelectorUse(stack, selector)

	switch selector.Sel.Name {
	case "Command":
		a.findings = append(a.findings,
			file.finding(selector, "exec-command", use))
	case "CommandContext":
		call := imageDirectCall(stack, selector)
		function := imageEnclosingFunction(stack)
		entry := imageCallUse{
			file:     file,
			selector: selector,
			call:     call,
			assign:   imageDirectAssignment(stack, call),
			function: function,
			nested:   imageInsideFunctionLiteral(stack),
		}
		a.commandContext = append(a.commandContext, entry)

		if call == nil {
			a.findings = append(a.findings,
				file.finding(selector, "command-context-reference", use))
		} else if entry.nested || function == nil || function.Name.Name != "imageRunChild" {
			a.findings = append(a.findings,
				file.finding(selector, "command-context-location", imageCallLocation(entry)))
		}
	case "Cmd":
		a.findings = append(a.findings,
			file.finding(selector, "exec-cmd", use))
	case "LookPath":
		if file.name != "image_child_test.go" {
			a.findings = append(a.findings,
				file.finding(selector, "exec-selector", "LookPath"))
		}
	case "ExitError":
		if !imageIsTypePosition(stack, selector) {
			a.findings = append(a.findings,
				file.finding(selector, "exec-exit-error-value", use))
		}
	default:
		a.findings = append(a.findings,
			file.finding(selector, "exec-selector", selector.Sel.Name))
	}
}

func (a *imageAudit) inspectSyscallSelector(
	file imageParsedFile,
	stack []ast.Node,
	selector *ast.SelectorExpr,
) {
	allowed := map[string]bool{
		"ESRCH":       true,
		"Kill":        true,
		"SIGKILL":     true,
		"SysProcAttr": true,
	}
	if file.name == "image_child_test.go" && allowed[selector.Sel.Name] {
		a.syscallCounts[selector.Sel.Name]++

		valid := false
		switch selector.Sel.Name {
		case "SysProcAttr":
			valid = imageValidSysProcAttr(stack, selector)
		case "Kill":
			valid = imageValidGroupKill(file, stack, selector)
		case "SIGKILL":
			valid = imageValidSyscallArgument(file, stack, selector, "Kill", 1)
		case "ESRCH":
			valid = imageValidImportedArgument(file, stack, selector, "errors", "Is", 1)
		}
		if !valid {
			a.findings = append(a.findings,
				file.finding(selector, "syscall-shape", selector.Sel.Name))
		}

		return
	}

	a.findings = append(a.findings,
		file.finding(selector, "syscall-selector", selector.Sel.Name))
}

func imageValidSysProcAttr(stack []ast.Node, selector *ast.SelectorExpr) bool {
	if len(stack) < 5 {
		return false
	}
	composite, ok := stack[len(stack)-2].(*ast.CompositeLit)
	if !ok || composite.Type != selector || len(composite.Elts) != 1 {
		return false
	}
	field, ok := composite.Elts[0].(*ast.KeyValueExpr)
	if !ok {
		return false
	}
	key, keyOK := field.Key.(*ast.Ident)
	value, valueOK := field.Value.(*ast.Ident)
	if !keyOK || !valueOK || key.Name != "Setpgid" || value.Name != "true" {
		return false
	}
	address, ok := stack[len(stack)-3].(*ast.UnaryExpr)
	if !ok || address.Op != token.AND || address.X != composite {
		return false
	}
	assignment, ok := stack[len(stack)-4].(*ast.AssignStmt)
	if !ok || assignment.Tok != token.ASSIGN || len(assignment.Lhs) != 1 || len(assignment.Rhs) != 1 ||
		assignment.Rhs[0] != address {
		return false
	}
	target, ok := assignment.Lhs[0].(*ast.SelectorExpr)
	if !ok || target.Sel.Name != "SysProcAttr" {
		return false
	}
	command, ok := target.X.(*ast.Ident)
	function := imageEnclosingFunction(stack)

	return ok && command.Name == "command" && function != nil && function.Name.Name == "imageRunChild"
}

func imageValidGroupKill(file imageParsedFile, stack []ast.Node, selector *ast.SelectorExpr) bool {
	call := imageDirectCall(stack, selector)
	function := imageEnclosingFunction(stack)
	if call == nil || function == nil || function.Name.Name != "imageKillProcessGroup" || len(call.Args) != 2 {
		return false
	}
	if function.Type.Params == nil || len(function.Type.Params.List) != 1 {
		return false
	}
	parameter := function.Type.Params.List[0]
	if len(parameter.Names) != 1 || parameter.Names[0].Name != "pid" {
		return false
	}
	parameterType, ok := parameter.Type.(*ast.Ident)
	if !ok || parameterType.Name != "int" {
		return false
	}
	group, ok := call.Args[0].(*ast.UnaryExpr)
	if !ok || group.Op != token.SUB {
		return false
	}
	pid, ok := group.X.(*ast.Ident)
	if !ok || pid.Name != "pid" {
		return false
	}
	pidUses := 0
	ast.Inspect(function.Body, func(node ast.Node) bool {
		identifier, ok := node.(*ast.Ident)
		if ok && identifier.Name == "pid" {
			pidUses++
		}

		return true
	})
	if pidUses != 1 {
		return false
	}

	signal, ok := call.Args[1].(*ast.SelectorExpr)

	return ok && imageSelectorImport(file, signal, "syscall", "SIGKILL")
}

func imageValidSyscallArgument(
	file imageParsedFile,
	stack []ast.Node,
	selector *ast.SelectorExpr,
	function string,
	index int,
) bool {
	if len(stack) < 2 {
		return false
	}
	call, ok := stack[len(stack)-2].(*ast.CallExpr)
	if !ok || index >= len(call.Args) || call.Args[index] != selector {
		return false
	}
	target, ok := call.Fun.(*ast.SelectorExpr)

	return ok && imageSelectorImport(file, target, "syscall", function)
}

func imageValidImportedArgument(
	file imageParsedFile,
	stack []ast.Node,
	selector *ast.SelectorExpr,
	importPath string,
	function string,
	index int,
) bool {
	if len(stack) < 2 {
		return false
	}
	call, ok := stack[len(stack)-2].(*ast.CallExpr)
	if !ok || index >= len(call.Args) || call.Args[index] != selector {
		return false
	}
	target, ok := call.Fun.(*ast.SelectorExpr)

	return ok && imageSelectorImport(file, target, importPath, function)
}

func imageSelectorImport(file imageParsedFile, selector *ast.SelectorExpr, path, name string) bool {
	identifier, ok := selector.X.(*ast.Ident)

	return ok && file.imports[identifier.Name] == path && selector.Sel.Name == name
}

func (a *imageAudit) inspectWithTimeout(file imageParsedFile, stack []ast.Node, selector *ast.SelectorExpr) {
	call := imageDirectCall(stack, selector)
	function := imageEnclosingFunction(stack)
	entry := imageCallUse{
		file:     file,
		selector: selector,
		call:     call,
		assign:   imageDirectAssignment(stack, call),
		function: function,
		nested:   imageInsideFunctionLiteral(stack),
	}
	a.withTimeout = append(a.withTimeout, entry)

	if call == nil {
		a.findings = append(a.findings,
			file.finding(selector, "with-timeout-reference", imageSelectorUse(stack, selector)))
	} else if entry.nested || function == nil || function.Name.Name != "imageRunChild" {
		a.findings = append(a.findings,
			file.finding(selector, "with-timeout-location", imageCallLocation(entry)))
	}
}

func imageDirectCall(stack []ast.Node, selector *ast.SelectorExpr) *ast.CallExpr {
	if len(stack) < 2 {
		return nil
	}

	call, ok := stack[len(stack)-2].(*ast.CallExpr)
	if !ok || call.Fun != selector {
		return nil
	}

	return call
}

func imageDirectAssignment(stack []ast.Node, call *ast.CallExpr) *ast.AssignStmt {
	if call == nil {
		return nil
	}

	var current ast.Node = call
	for index := len(stack) - 3; index >= 0; index-- {
		switch parent := stack[index].(type) {
		case *ast.ParenExpr:
			if parent.X != current {
				return nil
			}
			current = parent
		case *ast.AssignStmt:
			if len(parent.Rhs) == 1 && parent.Rhs[0] == current {
				return parent
			}

			return nil
		default:
			return nil
		}
	}

	return nil
}

func imageEnclosingFunction(stack []ast.Node) *ast.FuncDecl {
	for index := len(stack) - 2; index >= 0; index-- {
		if function, ok := stack[index].(*ast.FuncDecl); ok {
			return function
		}
	}

	return nil
}

func imageFunctionName(function *ast.FuncDecl) string {
	if function == nil {
		return "<none>"
	}

	return function.Name.Name
}

func imageInsideFunctionLiteral(stack []ast.Node) bool {
	for _, node := range stack {
		if _, ok := node.(*ast.FuncLit); ok {
			return true
		}
	}

	return false
}

func imageCallLocation(call imageCallUse) string {
	if call.nested {
		return "<func literal>"
	}

	return imageFunctionName(call.function)
}

func imageSelectorUse(stack []ast.Node, selector *ast.SelectorExpr) string {
	if len(stack) < 2 {
		return "selector"
	}

	parent := stack[len(stack)-2]

	switch typed := parent.(type) {
	case *ast.CallExpr:
		if typed.Fun == selector {
			return "call"
		}

		identifier, ok := typed.Fun.(*ast.Ident)
		if ok && identifier.Name == "new" {
			return "new"
		}
	case *ast.CompositeLit:
		if typed.Type == selector {
			if len(stack) >= 3 {
				if unary, ok := stack[len(stack)-3].(*ast.UnaryExpr); ok && unary.Op == token.AND {
					return "pointer-composite-lit"
				}
			}

			return "composite-lit"
		}
	case *ast.StarExpr:
		return "type"
	}

	if imageIsTypePosition(stack, selector) {
		return "type"
	}

	return "method-value"
}

func imageIsTypePosition(stack []ast.Node, node ast.Node) bool {
	current := node

	for index := len(stack) - 2; index >= 0; index-- {
		parent := stack[index]

		switch typed := parent.(type) {
		case *ast.ParenExpr:
			if typed.X != current {
				return false
			}
			current = typed
		case *ast.StarExpr:
			if typed.X != current {
				return false
			}
			current = typed
		case *ast.ArrayType:
			if typed.Elt != current {
				return false
			}
			current = typed
		case *ast.MapType:
			if typed.Key != current && typed.Value != current {
				return false
			}
			current = typed
		case *ast.ChanType:
			if typed.Value != current {
				return false
			}
			current = typed
		case *ast.IndexExpr:
			if typed.Index != current {
				return false
			}
			current = typed
		case *ast.IndexListExpr:
			found := false
			for _, index := range typed.Indices {
				found = found || index == current
			}
			if !found {
				return false
			}
			current = typed
		case *ast.ValueSpec:
			return typed.Type == current
		case *ast.Field:
			return typed.Type == current
		case *ast.TypeSpec:
			return typed.Type == current
		case *ast.TypeAssertExpr:
			return typed.Type == current
		default:
			return false
		}
	}

	return false
}

func (a *imageAudit) finish() {
	a.requireCount("command-context-count", len(a.commandContext), 1)
	a.requireCount("with-timeout-count", len(a.withTimeout), 1)
	a.requireCount("timeout-constant-count", len(a.timeoutDecls), 1)
	a.requireCount("runner-count", len(a.runners), 1)
	a.requireCount("process-field-count", a.processFields, 1)
	a.requireCount("process-group-call-count", a.groupKillCalls, 2)
	a.requireCount("process-group-nested-count", a.groupKillNested, 1)
	a.requireCount("process-group-reference-count", a.groupKillRefs, 2)
	for _, selector := range []string{"ESRCH", "Kill", "SIGKILL", "SysProcAttr"} {
		a.requireCount("syscall-"+strings.ToLower(selector)+"-count", a.syscallCounts[selector], 1)
	}

	if len(a.timeoutDecls) == 1 {
		decl := a.timeoutDecls[0]
		value, ok := imageDurationValue(decl.expr, decl.file.imports)
		if !ok {
			a.findings = append(a.findings,
				decl.file.finding(decl.node, "timeout-constant-value", "unevaluated"))
		} else if value <= 0 || value > int64(10*time.Second) {
			a.findings = append(a.findings,
				decl.file.finding(decl.node, "timeout-constant-value", time.Duration(value).String()))
		}
	}

	if len(a.runners) == 1 && len(a.withTimeout) == 1 && len(a.commandContext) == 1 {
		a.inspectRunnerFlow(a.runners[0].file, a.runners[0].decl, a.withTimeout[0], a.commandContext[0])
	}
}

func (a *imageAudit) requireCount(rule string, got, want int) {
	if got == want {
		return
	}

	a.findings = append(a.findings, imageFinding{
		File:   "<package>",
		Line:   1,
		Rule:   rule,
		Detail: fmt.Sprintf("got %d, want %d", got, want),
	})
}

func imageDurationValue(expression ast.Expr, imports map[string]string) (int64, bool) {
	value, ok := imageConstantValue(expression, imports)
	if !ok || value.Kind() != constant.Int {
		return 0, false
	}

	result, exact := constant.Int64Val(value)

	return result, exact
}

func imageConstantValue(expression ast.Expr, imports map[string]string) (constant.Value, bool) {
	switch typed := expression.(type) {
	case *ast.BasicLit:
		value := constant.MakeFromLiteral(typed.Value, typed.Kind, 0)

		return value, value.Kind() != constant.Unknown
	case *ast.ParenExpr:
		return imageConstantValue(typed.X, imports)
	case *ast.UnaryExpr:
		value, ok := imageConstantValue(typed.X, imports)
		if !ok {
			return nil, false
		}

		return imageUnaryConstant(typed.Op, value)
	case *ast.BinaryExpr:
		left, leftOK := imageConstantValue(typed.X, imports)
		right, rightOK := imageConstantValue(typed.Y, imports)
		if !leftOK || !rightOK {
			return nil, false
		}
		return imageBinaryConstant(left, typed.Op, right)
	case *ast.SelectorExpr:
		identifier, ok := typed.X.(*ast.Ident)
		if !ok || imports[identifier.Name] != "time" {
			return nil, false
		}

		units := map[string]time.Duration{
			"Nanosecond":  time.Nanosecond,
			"Microsecond": time.Microsecond,
			"Millisecond": time.Millisecond,
			"Second":      time.Second,
			"Minute":      time.Minute,
			"Hour":        time.Hour,
		}
		value, ok := units[typed.Sel.Name]
		if !ok {
			return nil, false
		}

		return constant.MakeInt64(int64(value)), true
	default:
		return nil, false
	}
}

func imageUnaryConstant(operator token.Token, value constant.Value) (result constant.Value, ok bool) {
	defer func() {
		if recover() != nil {
			result = nil
			ok = false
		}
	}()

	return constant.UnaryOp(operator, value, 0), true
}

func imageBinaryConstant(left constant.Value, operator token.Token, right constant.Value) (
	result constant.Value,
	ok bool,
) {
	defer func() {
		if recover() != nil {
			result = nil
			ok = false
		}
	}()

	return constant.BinaryOp(left, operator, right), true
}

func (a *imageAudit) inspectRunnerFlow(
	file imageParsedFile,
	runner *ast.FuncDecl,
	timeout imageCallUse,
	command imageCallUse,
) {
	if timeout.function != runner || timeout.call == nil || timeout.assign == nil {
		a.findings = append(a.findings,
			file.finding(runner, "timeout-binding", "not a direct assignment in imageRunChild"))

		return
	}
	a.inspectRunnerReturn(file, runner)

	assignment := timeout.assign
	if assignment.Tok != token.DEFINE || len(assignment.Lhs) != 2 || len(assignment.Rhs) != 1 {
		a.findings = append(a.findings,
			file.finding(assignment, "timeout-binding", "want ctx, cancel := context.WithTimeout(...)"))

		return
	}

	contextName, contextOK := assignment.Lhs[0].(*ast.Ident)
	cancelName, cancelOK := assignment.Lhs[1].(*ast.Ident)
	if !contextOK || !cancelOK {
		a.findings = append(a.findings,
			file.finding(assignment, "timeout-binding", "bindings must be identifiers"))

		return
	}

	if len(timeout.call.Args) != 2 {
		a.findings = append(a.findings,
			file.finding(timeout.call, "timeout-argument", "want exactly two arguments"))
	} else {
		constantName, ok := timeout.call.Args[1].(*ast.Ident)
		if !ok || constantName.Name != "imageProcessTimeout" {
			a.findings = append(a.findings,
				file.finding(timeout.call.Args[1], "timeout-argument", "want imageProcessTimeout"))
		}
	}

	contextBindings := imageBindingCount(runner, contextName.Name)
	cancelBindings := imageBindingCount(runner, cancelName.Name)

	for name, count := range map[string]int{
		contextName.Name: contextBindings,
		cancelName.Name:  cancelBindings,
	} {
		if count == 1 {
			continue
		}

		a.findings = append(a.findings,
			file.finding(runner, "shadowed-binding", fmt.Sprintf("%s defined %d times", name, count)))
	}

	for _, name := range []string{contextName.Name, cancelName.Name} {
		if count := imageBoundNameWrites(runner.Body, assignment, name); count != 0 {
			a.findings = append(a.findings,
				file.finding(runner, "bound-name-write", fmt.Sprintf("%s written %d times", name, count)))
		}
	}

	deferredIdentifiers := imageDirectDeferredIdentifiers(runner.Body, cancelName.Name)
	if cancelBindings != 1 || len(deferredIdentifiers) != 1 {
		a.findings = append(a.findings,
			file.finding(runner, "cancel-not-deferred",
				fmt.Sprintf("bindings %d, direct defers %d", cancelBindings, len(deferredIdentifiers))))
	}

	if command.function != runner || command.call == nil {
		return
	}
	if command.assign == nil || command.assign.Tok != token.DEFINE ||
		len(command.assign.Lhs) != 1 || len(command.assign.Rhs) != 1 ||
		command.assign.Rhs[0] != command.call {
		a.findings = append(a.findings,
			file.finding(command.call, "command-binding", "want command := exec.CommandContext(...)"))
	} else {
		commandName, ok := command.assign.Lhs[0].(*ast.Ident)
		if !ok || commandName.Name != "command" {
			a.findings = append(a.findings,
				file.finding(command.assign, "command-binding", "want command identifier"))
		} else {
			a.inspectCommandUses(file, runner, commandName)
		}
	}

	if len(command.call.Args) == 0 {
		a.findings = append(a.findings,
			file.finding(command.call, "context-not-passed", "CommandContext has no arguments"))

		return
	}

	passed, ok := command.call.Args[0].(*ast.Ident)
	if contextBindings != 1 || !ok || passed.Name != contextName.Name {
		a.findings = append(a.findings,
			file.finding(command.call.Args[0], "context-not-passed", "want bound timeout context"))
	}

	allowedContext := map[*ast.Ident]bool{contextName: true}
	if ok && passed.Name == contextName.Name {
		allowedContext[passed] = true
	}
	allowedCancel := map[*ast.Ident]bool{cancelName: true}
	for _, identifier := range deferredIdentifiers {
		allowedCancel[identifier] = true
	}

	for name, allowed := range map[string]map[*ast.Ident]bool{
		contextName.Name: allowedContext,
		cancelName.Name:  allowedCancel,
	} {
		if count := imageUnexpectedIdentifierUses(runner.Body, name, allowed); count != 0 {
			a.findings = append(a.findings,
				file.finding(runner, "bound-name-use", fmt.Sprintf("%s has %d unexpected uses", name, count)))
		}
	}
}

func (a *imageAudit) inspectCommandUses(
	file imageParsedFile,
	runner *ast.FuncDecl,
	binding *ast.Ident,
) {
	var stack []ast.Node
	runCount := 0

	ast.Inspect(runner.Body, func(node ast.Node) bool {
		if node == nil {
			stack = stack[:len(stack)-1]

			return true
		}
		stack = append(stack, node)

		identifier, ok := node.(*ast.Ident)
		if !ok || identifier.Name != binding.Name {
			return true
		}
		if selector, ok := imageParentSelector(stack, identifier); ok &&
			selector.Sel.Name == "Run" {
			runCount++
		}
		if identifier == binding || imageValidCommandUse(stack, runner.Body) {
			return true
		}

		a.findings = append(a.findings,
			file.finding(identifier, "command-use", "command may not be aliased or mutated"))

		return true
	})

	if runCount != 1 {
		a.findings = append(a.findings,
			file.finding(runner, "command-run-count", fmt.Sprintf("got %d, want 1", runCount)))
	}
}

func imageParentSelector(
	stack []ast.Node,
	identifier *ast.Ident,
) (*ast.SelectorExpr, bool) {
	if len(stack) < 2 {
		return nil, false
	}
	selector, ok := stack[len(stack)-2].(*ast.SelectorExpr)

	return selector, ok && selector.X == identifier
}

func imageValidCommandUse(stack []ast.Node, runnerBody *ast.BlockStmt) bool {
	if len(stack) < 3 {
		return false
	}

	identifier, ok := stack[len(stack)-1].(*ast.Ident)
	selector, selectorOK := stack[len(stack)-2].(*ast.SelectorExpr)
	if !ok || !selectorOK || selector.X != identifier {
		return false
	}

	parent := stack[len(stack)-3]
	switch selector.Sel.Name {
	case "Dir", "Env", "Stderr", "Stdout", "SysProcAttr":
		assignment, ok := parent.(*ast.AssignStmt)

		return ok && assignment.Tok == token.ASSIGN &&
			len(assignment.Lhs) == 1 && assignment.Lhs[0] == selector &&
			imageDirectStatement(runnerBody, assignment)
	case "Cancel":
		assignment, ok := parent.(*ast.AssignStmt)
		if !ok || assignment.Tok != token.ASSIGN ||
			len(assignment.Lhs) != 1 || assignment.Lhs[0] != selector ||
			len(assignment.Rhs) != 1 || !imageDirectStatement(runnerBody, assignment) {
			return false
		}
		literal, ok := assignment.Rhs[0].(*ast.FuncLit)

		return ok && imageValidCancelLiteral(literal)
	case "Run":
		call, ok := parent.(*ast.CallExpr)

		return ok && call.Fun == selector && len(call.Args) == 0 &&
			imageCallInDirectAssignment(stack, call, runnerBody)
	case "Process":
		return imageValidCommandProcessUse(stack, selector, runnerBody)
	default:
		return false
	}
}

func imageValidCancelLiteral(literal *ast.FuncLit) bool {
	if literal.Type.Params.NumFields() != 0 ||
		literal.Type.Results.NumFields() != 1 ||
		len(literal.Type.Results.List) != 1 ||
		!imageIsIdent(literal.Type.Results.List[0].Type, "error") {
		return false
	}

	statements := literal.Body.List
	if len(statements) != 2 {
		return false
	}

	expression, ok := statements[0].(*ast.ExprStmt)
	if !ok {
		return false
	}
	store, ok := expression.X.(*ast.CallExpr)
	if !ok || len(store.Args) != 1 || !imageIsIdent(store.Args[0], "true") {
		return false
	}
	method, ok := store.Fun.(*ast.SelectorExpr)
	if !ok || method.Sel.Name != "Store" ||
		!imageIsIdent(method.X, "deadlineCancelled") {
		return false
	}

	return imageIsProcessGroupReturn(statements[1])
}

func imageIsProcessGroupReturn(statement ast.Stmt) bool {
	result, ok := statement.(*ast.ReturnStmt)
	if !ok || len(result.Results) != 1 {
		return false
	}
	call, ok := result.Results[0].(*ast.CallExpr)
	if !ok || len(call.Args) != 1 || !imageIsIdent(call.Fun, "imageKillProcessGroup") {
		return false
	}
	pid, ok := call.Args[0].(*ast.SelectorExpr)
	if !ok || pid.Sel.Name != "Pid" {
		return false
	}
	process, ok := pid.X.(*ast.SelectorExpr)

	return ok && process.Sel.Name == "Process" && imageIsIdent(process.X, "command")
}

func imageValidCommandProcessUse(
	stack []ast.Node,
	process *ast.SelectorExpr,
	runnerBody *ast.BlockStmt,
) bool {
	parent := stack[len(stack)-3]
	if comparison, ok := parent.(*ast.BinaryExpr); ok {
		if comparison.Op != token.NEQ {
			return false
		}
		nilOnLeft := imageIsIdent(comparison.X, "nil") && comparison.Y == process
		nilOnRight := comparison.X == process && imageIsIdent(comparison.Y, "nil")

		return (nilOnLeft || nilOnRight) &&
			imageExpressionInDirectIf(stack, comparison, runnerBody)
	}

	pid, ok := parent.(*ast.SelectorExpr)
	if !ok || pid.X != process || pid.Sel.Name != "Pid" || len(stack) < 4 {
		return false
	}
	call, ok := stack[len(stack)-4].(*ast.CallExpr)
	if !ok || len(call.Args) != 1 || call.Args[0] != pid {
		return false
	}
	function, ok := call.Fun.(*ast.Ident)

	return ok && function.Name == "imageKillProcessGroup" &&
		(imageInsideCommandCancel(stack) ||
			imageCallInDirectStatementOrIf(stack, call, runnerBody))
}

func imageIsIdent(expression ast.Expr, name string) bool {
	identifier, ok := expression.(*ast.Ident)

	return ok && identifier.Name == name
}

func imageDirectStatement(body *ast.BlockStmt, statement ast.Stmt) bool {
	for _, candidate := range body.List {
		if candidate == statement {
			return true
		}
	}

	return false
}

func imageCallInDirectAssignment(
	stack []ast.Node,
	call *ast.CallExpr,
	body *ast.BlockStmt,
) bool {
	for index, node := range stack {
		if node != call || index == 0 {
			continue
		}
		assignment, ok := stack[index-1].(*ast.AssignStmt)

		return ok && len(assignment.Rhs) == 1 && assignment.Rhs[0] == call &&
			imageDirectStatement(body, assignment)
	}

	return false
}

func imageCallInDirectStatementOrIf(
	stack []ast.Node,
	call *ast.CallExpr,
	body *ast.BlockStmt,
) bool {
	statement := imageEnclosingStatement(stack, call)
	if statement == nil {
		return false
	}
	if imageDirectStatement(body, statement) {
		return true
	}

	for index, node := range stack {
		if node != statement || index < 2 {
			continue
		}
		block, ok := stack[index-1].(*ast.BlockStmt)
		conditional, conditionalOK := stack[index-2].(*ast.IfStmt)

		return ok && conditionalOK && conditional.Body == block &&
			imageDirectStatement(body, conditional)
	}

	return false
}

func imageExpressionInDirectIf(
	stack []ast.Node,
	expression ast.Expr,
	body *ast.BlockStmt,
) bool {
	for index, node := range stack {
		if node != expression || index == 0 {
			continue
		}
		conditional, ok := stack[index-1].(*ast.IfStmt)

		return ok && conditional.Cond == expression &&
			imageDirectStatement(body, conditional)
	}

	return false
}

func imageEnclosingStatement(stack []ast.Node, child ast.Node) ast.Stmt {
	found := false
	for index := len(stack) - 1; index >= 0; index-- {
		if stack[index] == child {
			found = true

			continue
		}
		if found {
			if statement, ok := stack[index].(ast.Stmt); ok {
				return statement
			}
		}
	}

	return nil
}

func (a *imageAudit) inspectRunnerReturn(file imageParsedFile, runner *ast.FuncDecl) {
	var returns []*ast.ReturnStmt
	ast.Inspect(runner.Body, func(node ast.Node) bool {
		if _, ok := node.(*ast.FuncLit); ok {
			return false
		}
		if result, ok := node.(*ast.ReturnStmt); ok {
			returns = append(returns, result)
		}

		return true
	})

	if len(returns) != 1 {
		a.findings = append(a.findings,
			file.finding(runner, "runner-return-count",
				fmt.Sprintf("got %d, want 1", len(returns))))

		return
	}
	result := returns[0]
	if len(runner.Body.List) == 0 ||
		runner.Body.List[len(runner.Body.List)-1] != result ||
		len(result.Results) != 1 {
		a.findings = append(a.findings,
			file.finding(result, "runner-return", "want one final direct imageChildResult"))

		return
	}
	composite, ok := result.Results[0].(*ast.CompositeLit)
	if !ok || !imageIsIdent(composite.Type, "imageChildResult") {
		a.findings = append(a.findings,
			file.finding(result, "runner-return", "want one final direct imageChildResult"))
	}
}

func imageBindingCount(runner *ast.FuncDecl, name string) int {
	count := 0

	if runner.Type.Params != nil {
		for _, field := range runner.Type.Params.List {
			for _, identifier := range field.Names {
				if identifier.Name == name {
					count++
				}
			}
		}
	}

	ast.Inspect(runner.Body, func(node ast.Node) bool {
		switch typed := node.(type) {
		case *ast.AssignStmt:
			if typed.Tok != token.DEFINE {
				return true
			}
			for _, left := range typed.Lhs {
				identifier, ok := left.(*ast.Ident)
				if ok && identifier.Name == name {
					count++
				}
			}
		case *ast.ValueSpec:
			for _, identifier := range typed.Names {
				if identifier.Name == name {
					count++
				}
			}
		case *ast.FuncLit:
			fields := append([]*ast.Field(nil), typed.Type.Params.List...)
			if typed.Type.Results != nil {
				fields = append(fields, typed.Type.Results.List...)
			}
			for _, field := range fields {
				for _, identifier := range field.Names {
					if identifier.Name == name {
						count++
					}
				}
			}
		case *ast.RangeStmt:
			if typed.Tok != token.DEFINE {
				return true
			}
			for _, expression := range []ast.Expr{typed.Key, typed.Value} {
				identifier, ok := expression.(*ast.Ident)
				if ok && identifier.Name == name {
					count++
				}
			}
		}

		return true
	})

	return count
}

func imageBoundNameWrites(body *ast.BlockStmt, binding *ast.AssignStmt, name string) int {
	count := 0

	ast.Inspect(body, func(node ast.Node) bool {
		switch typed := node.(type) {
		case *ast.AssignStmt:
			if typed == binding {
				return true
			}
			for _, left := range typed.Lhs {
				identifier, ok := left.(*ast.Ident)
				if ok && identifier.Name == name {
					count++
				}
			}
		case *ast.IncDecStmt:
			identifier, ok := typed.X.(*ast.Ident)
			if ok && identifier.Name == name {
				count++
			}
		case *ast.RangeStmt:
			for _, expression := range []ast.Expr{typed.Key, typed.Value} {
				identifier, ok := expression.(*ast.Ident)
				if ok && identifier.Name == name {
					count++
				}
			}
		}

		return true
	})

	return count
}

func imageDirectDeferredIdentifiers(body *ast.BlockStmt, name string) []*ast.Ident {
	var identifiers []*ast.Ident
	var stack []ast.Node

	ast.Inspect(body, func(node ast.Node) bool {
		if node == nil {
			stack = stack[:len(stack)-1]

			return true
		}

		stack = append(stack, node)

		deferred, ok := node.(*ast.DeferStmt)
		if !ok {
			return true
		}

		if len(stack) < 2 || stack[len(stack)-2] != ast.Node(body) {
			return true
		}

		identifier, ok := deferred.Call.Fun.(*ast.Ident)
		if ok && identifier.Name == name && len(deferred.Call.Args) == 0 {
			identifiers = append(identifiers, identifier)
		}

		return true
	})

	return identifiers
}

func imageUnexpectedIdentifierUses(body *ast.BlockStmt, name string, allowed map[*ast.Ident]bool) int {
	count := 0

	ast.Inspect(body, func(node ast.Node) bool {
		identifier, ok := node.(*ast.Ident)
		if ok && identifier.Name == name && !allowed[identifier] {
			count++
		}

		return true
	})

	return count
}

func TestImageProcessLiveAudit(t *testing.T) {
	findings, err := imageAuditDir(".")
	require.NoError(t, err)
	require.Empty(t, findings)
}

func TestImageProcessAuditIsNotVacuous(t *testing.T) {
	dir := t.TempDir()
	source := `package packaging

import "os/exec"

func bad() {
	_ = exec.Command("sh")
}
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "image_bad.go"), []byte(source), 0o600))

	findings, err := imageAuditDir(dir)
	require.NoError(t, err)

	var rules []string
	for _, finding := range findings {
		rules = append(rules, finding.Rule)
	}

	require.Contains(t, rules, "exec-command")
}

const imageCleanFixture = `package packaging

import (
	"context"
	"errors"
	"os/exec"
	"syscall"
	"time"
)

const imageProcessTimeout = time.Second

func imageRunChild(tool string, args ...string) imageChildResult {
	ctx, cancel := context.WithTimeout(context.Background(), imageProcessTimeout)
	defer cancel()

	command := exec.CommandContext(ctx, tool, args...)
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	command.Cancel = func() error {
		deadlineCancelled.Store(true); return imageKillProcessGroup(command.Process.Pid)
	}
	_ = imageKillProcessGroup(command.Process.Pid); _ = command.Run()
	var _ *exec.ExitError
	_, _ = exec.LookPath(tool); return imageChildResult{}
}

func imageKillProcessGroup(pid int) error {
	err := syscall.Kill(-pid, syscall.SIGKILL)
	if errors.Is(err, syscall.ESRCH) {
		return nil
	}
	return err
}
`

const imageReformattedFixture = `package packaging

import (
	"context"
	"errors"
	"os/exec"
	"syscall"
	"time"
)

const imageProcessTimeout = (1 * time.Second)

func imageRunChild(tool string, args ...string) imageChildResult {
	ctx,
		cancel :=
		context.WithTimeout(
			context.Background(),
			imageProcessTimeout,
		)
	defer /* binding is direct */ cancel()

	command := exec.CommandContext(
		ctx, // the timeout context
		tool,
		args...,
	)
	command.SysProcAttr = &syscall.SysProcAttr{
		Setpgid: true,
	}
	command.Cancel = func() error {
		deadlineCancelled.Store(true)
		return imageKillProcessGroup(
			command.Process.Pid,
		)
	}
	_ = imageKillProcessGroup(
		command.Process.Pid,
	)
	_ = command.Run()
	var _ *exec.ExitError
	_, _ = exec.LookPath(
		tool,
	)
	return imageChildResult{}
}

func imageKillProcessGroup(pid int) error {
	err := syscall.Kill(
		-pid,
		syscall.SIGKILL,
	)
	if errors.Is(
		err,
		syscall.ESRCH,
	) {
		return nil
	}
	return err
}
`

const imageCommandBlock = "\tcommand := exec.CommandContext(ctx, tool, args...)\n" +
	"\tcommand.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}\n" +
	"\tcommand.Cancel = func() error {\n" +
	"\t\tdeadlineCancelled.Store(true); return imageKillProcessGroup(command.Process.Pid)\n" +
	"\t}\n" +
	"\t_ = imageKillProcessGroup(command.Process.Pid); _ = command.Run()\n"

const imageNoProcessGroupFixture = `package packaging

import (
	"context"
	"os/exec"
	"time"
)

const imageProcessTimeout = time.Second

func imageRunChild(tool string, args ...string) imageChildResult {
	ctx, cancel := context.WithTimeout(context.Background(), imageProcessTimeout)
	defer cancel()

	command := exec.CommandContext(ctx, tool, args...)
	_ = command.Run()
	var _ *exec.ExitError
	_, _ = exec.LookPath(tool); return imageChildResult{}
}
`

type imageAuditFixture struct {
	files map[string]string
	want  []string
}

func imageFixtureReplace(t *testing.T, source, old, replacement string) string {
	t.Helper()
	require.Contains(t, source, old, "fixture replacement must change a known clean-source fragment")

	return strings.Replace(source, old, replacement, 1)
}

func imageFindingKeys(findings []imageFinding) []string {
	keys := make([]string, 0, len(findings))
	for _, finding := range findings {
		keys = append(keys, fmt.Sprintf("%s:%d:%s:%s",
			finding.File, finding.Line, finding.Rule, finding.Detail))
	}

	sort.Strings(keys)

	return keys
}

// TestImageProcessDifferential drives hand-written source fixtures through the
// same imageAuditDir boundary as the live check. Expectations are independent
// exact multisets: duplicates survive, and neither the audit nor a subset of
// its output is used to construct the expected side.
func TestImageProcessDifferential(t *testing.T) {
	fixtures := map[string]imageAuditFixture{
		"clean": {
			files: map[string]string{"image_child_test.go": imageCleanFixture},
		},
		"clean reformatted": {
			files: map[string]string{"image_child_test.go": imageReformattedFixture},
		},
		"clean ExitError type alias": {
			files: map[string]string{"image_child_test.go": imageCleanFixture +
				"\ntype imageFixtureExitError = exec.ExitError\n"},
		},
		"missing process group contract": {
			files: map[string]string{"image_child_test.go": imageNoProcessGroupFixture},
			want: []string{
				"<package>:1:process-field-count:got 0, want 1",
				"<package>:1:process-group-call-count:got 0, want 2",
				"<package>:1:process-group-nested-count:got 0, want 1",
				"<package>:1:process-group-reference-count:got 0, want 2",
				"<package>:1:syscall-esrch-count:got 0, want 1",
				"<package>:1:syscall-kill-count:got 0, want 1",
				"<package>:1:syscall-sigkill-count:got 0, want 1",
				"<package>:1:syscall-sysprocattr-count:got 0, want 1",
			},
		},
		"bare exec Command": {
			files: map[string]string{"image_child_test.go": imageFixtureReplace(t, imageCleanFixture,
				imageCommandBlock,
				imageCommandBlock+"\t_ = exec.Command(\"sh\")\n")},
			want: []string{"image_child_test.go:23:exec-command:call"},
		},
		"aliased os exec import": {
			files: map[string]string{"image_child_test.go": imageFixtureReplace(t, imageCleanFixture,
				"\t\"os/exec\"\n",
				"\texec \"os/exec\"\n")},
			want: []string{"image_child_test.go:6:restricted-import:os/exec as exec"},
		},
		"dot os exec import": {
			files: map[string]string{"image_child_test.go": imageFixtureReplace(t, imageCleanFixture,
				"\t\"os/exec\"\n",
				"\t\"os/exec\"\n\t. \"os/exec\"\n")},
			want: []string{"image_child_test.go:7:restricted-import:os/exec as ."},
		},
		"blank os exec import": {
			files: map[string]string{"image_child_test.go": imageFixtureReplace(t, imageCleanFixture,
				"\t\"os/exec\"\n",
				"\t\"os/exec\"\n\t_ \"os/exec\"\n")},
			want: []string{"image_child_test.go:7:restricted-import:os/exec as _"},
		},
		"os exec imported outside child file": {
			files: map[string]string{
				"image_child_test.go": imageCleanFixture,
				"image_other_test.go": "package packaging\n\nimport \"os/exec\"\n\nvar _ = exec.LookPath\n",
			},
			want: []string{
				"image_other_test.go:3:restricted-import:os/exec as exec",
				"image_other_test.go:5:exec-selector:LookPath",
			},
		},
		"second CommandContext call": {
			files: map[string]string{"image_child_test.go": imageFixtureReplace(t, imageCleanFixture,
				imageCommandBlock,
				imageCommandBlock+"\t_ = exec.CommandContext(ctx, tool)\n")},
			want: []string{"<package>:1:command-context-count:got 2, want 1"},
		},
		"CommandContext method value": {
			files: map[string]string{"image_child_test.go": imageFixtureReplace(t, imageCleanFixture,
				imageCommandBlock,
				imageCommandBlock+"\t_ = exec.CommandContext\n")},
			want: []string{
				"<package>:1:command-context-count:got 2, want 1",
				"image_child_test.go:23:command-context-reference:method-value",
			},
		},
		"CommandContext outside runner": {
			files: map[string]string{"image_child_test.go": imageCleanFixture +
				"\nfunc other(ctx context.Context, tool string) {\n\t_ = exec.CommandContext(ctx, tool)\n}\n"},
			want: []string{
				"<package>:1:command-context-count:got 2, want 1",
				"image_child_test.go:36:command-context-location:other",
			},
		},
		"exec Cmd constructions": {
			files: map[string]string{"image_child_test.go": imageFixtureReplace(t, imageCleanFixture,
				imageCommandBlock,
				imageCommandBlock+"\t_ = exec.Cmd{}\n\t_ = &exec.Cmd{}\n\t_ = new(exec.Cmd)\n\tvar _ exec.Cmd\n")},
			want: []string{
				"image_child_test.go:23:exec-cmd:composite-lit",
				"image_child_test.go:24:exec-cmd:pointer-composite-lit",
				"image_child_test.go:25:exec-cmd:new",
				"image_child_test.go:26:exec-cmd:type",
			},
		},
		"ExitError value": {
			files: map[string]string{"image_child_test.go": imageFixtureReplace(t, imageCleanFixture,
				"\tvar _ *exec.ExitError\n",
				"\tvar _ *exec.ExitError\n\t_ = exec.ExitError{}\n")},
			want: []string{"image_child_test.go:24:exec-exit-error-value:composite-lit"},
		},
		"non allowlisted exec selector": {
			files: map[string]string{"image_child_test.go": imageFixtureReplace(t, imageCleanFixture,
				"\t_, _ = exec.LookPath(tool); return imageChildResult{}\n",
				"\t_, _ = exec.LookPath(tool); _ = exec.Other; return imageChildResult{}\n")},
			want: []string{"image_child_test.go:24:exec-selector:Other"},
		},
		"aliased os StartProcess call and method value": {
			files: map[string]string{"image_child_test.go": imageFixtureReplace(t, imageCleanFixture,
				"\t\"os/exec\"\n",
				"\tstdos \"os\"\n\t\"os/exec\"\n") + "\nfunc start() {\n\t_, _ = stdos.StartProcess(\"\", nil, nil)\n\t_ = stdos.StartProcess\n}\n"},
			want: []string{
				"image_child_test.go:37:os-start-process:call",
				"image_child_test.go:38:os-start-process:method-value",
			},
		},
		"dot os import": {
			files: map[string]string{"image_child_test.go": imageFixtureReplace(t, imageCleanFixture,
				"\t\"os/exec\"\n",
				"\t. \"os\"\n\t\"os/exec\"\n")},
			want: []string{"image_child_test.go:6:dot-os-import:os"},
		},
		"forbidden imports": {
			files: map[string]string{"image_child_test.go": imageFixtureReplace(t, imageCleanFixture,
				"\t\"os/exec\"\n",
				"\t\"C\"\n\t\"golang.org/x/sys/unix\"\n\t\"os/exec\"\n\t\"syscall\"\n")},
			want: []string{
				"image_child_test.go:6:forbidden-import:C",
				"image_child_test.go:7:forbidden-import:golang.org/x/sys/unix",
			},
		},
		"syscall outside child file": {
			files: map[string]string{
				"image_child_test.go": imageCleanFixture,
				"image_other_test.go": "package packaging\n\nimport \"syscall\"\n\nvar _ = syscall.Kill\n",
			},
			want: []string{
				"image_other_test.go:3:restricted-import:syscall as syscall",
				"image_other_test.go:5:syscall-selector:Kill",
			},
		},
		"unknown image dependency": {
			files: map[string]string{"image_child_test.go": imageFixtureReplace(t, imageCleanFixture,
				"\t\"os/exec\"\n",
				"\t\"example.com/spawn\"\n\t\"os/exec\"\n")},
			want: []string{"image_child_test.go:6:image-import:example.com/spawn"},
		},
		"restricted import name shadow": {
			files: map[string]string{"image_child_test.go": imageFixtureReplace(t, imageCleanFixture,
				"func imageRunChild(tool string, args ...string) imageChildResult {\n",
				"func imageRunChild(tool string, args ...string) imageChildResult {\n"+
					"\tcontext := fakeContextAPI{}\n")},
			want: []string{"image_child_test.go:14:restricted-name-shadow:context"},
		},
		"hidden package helper": {
			files: map[string]string{
				"helper_test.go": "package packaging\n\nimport \"os/exec\"\n\nfunc hiddenSpawn() { _ = exec.Command(\"sh\") }\n",
				"image_child_test.go": imageFixtureReplace(t, imageCleanFixture,
					"\t_, _ = exec.LookPath(tool); return imageChildResult{}\n",
					"\t_, _ = exec.LookPath(tool); hiddenSpawn(); return imageChildResult{}\n"),
			},
			want: []string{
				"helper_test.go:3:restricted-import:os/exec as exec",
				"helper_test.go:5:exec-command:call",
			},
		},
		"timeout greater than ten seconds": {
			files: map[string]string{"image_child_test.go": imageFixtureReplace(t, imageCleanFixture,
				"const imageProcessTimeout = time.Second",
				"const imageProcessTimeout = 11 * time.Second")},
			want: []string{"image_child_test.go:11:timeout-constant-value:11s"},
		},
		"timeout cannot be evaluated": {
			files: map[string]string{"image_child_test.go": imageFixtureReplace(t, imageCleanFixture,
				"const imageProcessTimeout = time.Second",
				"const imageProcessTimeout = timeoutValue()")},
			want: []string{"image_child_test.go:11:timeout-constant-value:unevaluated"},
		},
		"timeout expression is ill typed": {
			files: map[string]string{"image_child_test.go": imageFixtureReplace(t, imageCleanFixture,
				"const imageProcessTimeout = time.Second",
				"const imageProcessTimeout = \"slow\" * time.Second")},
			want: []string{"image_child_test.go:11:timeout-constant-value:unevaluated"},
		},
		"timeout quotient is ill typed": {
			files: map[string]string{"image_child_test.go": imageFixtureReplace(t, imageCleanFixture,
				"const imageProcessTimeout = time.Second",
				"const imageProcessTimeout = \"slow\" / \"units\"")},
			want: []string{"image_child_test.go:11:timeout-constant-value:unevaluated"},
		},
		"timeout is wrapped by another call": {
			files: map[string]string{"image_child_test.go": imageFixtureReplace(t, imageCleanFixture,
				"ctx, cancel := context.WithTimeout(context.Background(), imageProcessTimeout)",
				"ctx, cancel := discardBounded(context.WithTimeout(context.Background(), imageProcessTimeout))")},
			want: []string{
				"image_child_test.go:13:timeout-binding:not a direct assignment in imageRunChild",
			},
		},
		"background context passed": {
			files: map[string]string{"image_child_test.go": imageFixtureReplace(t, imageCleanFixture,
				"command := exec.CommandContext(ctx, tool, args...)",
				"command := exec.CommandContext(context.Background(), tool, args...)")},
			want: []string{"image_child_test.go:17:context-not-passed:want bound timeout context"},
		},
		"second WithTimeout outside runner": {
			files: map[string]string{"image_child_test.go": imageCleanFixture +
				"\nfunc other() {\n\t_, cancel := context.WithTimeout(context.Background(), imageProcessTimeout)\n\tdefer cancel()\n}\n"},
			want: []string{
				"<package>:1:with-timeout-count:got 2, want 1",
				"image_child_test.go:36:with-timeout-location:other",
			},
		},
		"shadowed cancel": {
			files: map[string]string{"image_child_test.go": imageFixtureReplace(t, imageCleanFixture,
				"\tdefer cancel()\n",
				"\t{\n\t\tcancel := func() {}\n\t\tdefer cancel()\n\t}\n")},
			want: []string{
				"image_child_test.go:13:bound-name-use:cancel has 2 unexpected uses",
				"image_child_test.go:13:bound-name-write:cancel written 1 times",
				"image_child_test.go:13:cancel-not-deferred:bindings 2, direct defers 0",
				"image_child_test.go:13:shadowed-binding:cancel defined 2 times",
			},
		},
		"shadowed context": {
			files: map[string]string{"image_child_test.go": imageFixtureReplace(t, imageCleanFixture,
				imageCommandBlock,
				"\t{\n\t\tctx := context.Background()\n"+
					strings.ReplaceAll(imageCommandBlock, "\n\t", "\n\t\t")+"\t}\n")},
			want: []string{
				"image_child_test.go:13:bound-name-use:ctx has 1 unexpected uses",
				"image_child_test.go:13:bound-name-write:ctx written 1 times",
				"image_child_test.go:13:shadowed-binding:ctx defined 2 times",
				"image_child_test.go:19:context-not-passed:want bound timeout context",
				"image_child_test.go:20:command-use:command may not be aliased or mutated",
				"image_child_test.go:21:command-use:command may not be aliased or mutated",
				"image_child_test.go:24:command-use:command may not be aliased or mutated",
				"image_child_test.go:24:command-use:command may not be aliased or mutated",
			},
		},
		"reassigned context and cancel": {
			files: map[string]string{"image_child_test.go": imageFixtureReplace(t, imageCleanFixture,
				"\tdefer cancel()\n",
				"\tctx = context.Background()\n\tcancel = func() {}\n\tdefer cancel()\n")},
			want: []string{
				"image_child_test.go:13:bound-name-use:cancel has 1 unexpected uses",
				"image_child_test.go:13:bound-name-use:ctx has 1 unexpected uses",
				"image_child_test.go:13:bound-name-write:cancel written 1 times",
				"image_child_test.go:13:bound-name-write:ctx written 1 times",
			},
		},
		"indirect context and cancel writes": {
			files: map[string]string{"image_child_test.go": imageFixtureReplace(t, imageCleanFixture,
				"\tdefer cancel()\n",
				"\tcontextPointer := &ctx\n\t*contextPointer = context.Background()\n"+
					"\tcancelPointer := &cancel\n\t*cancelPointer = func() {}\n\tdefer cancel()\n")},
			want: []string{
				"image_child_test.go:13:bound-name-use:cancel has 1 unexpected uses",
				"image_child_test.go:13:bound-name-use:ctx has 1 unexpected uses",
			},
		},
		"CommandContext in nested function": {
			files: map[string]string{"image_child_test.go": imageFixtureReplace(t, imageCleanFixture,
				imageCommandBlock,
				"\tfunc(ctx context.Context) {\n"+
					strings.ReplaceAll(imageCommandBlock, "\n\t", "\n\t\t")+
					"\t}(context.Background())\n")},
			want: []string{
				"<package>:1:process-group-nested-count:got 2, want 1",
				"image_child_test.go:13:bound-name-use:ctx has 1 unexpected uses",
				"image_child_test.go:13:shadowed-binding:ctx defined 2 times",
				"image_child_test.go:18:command-context-location:<func literal>",
				"image_child_test.go:18:context-not-passed:want bound timeout context",
				"image_child_test.go:19:command-use:command may not be aliased or mutated",
				"image_child_test.go:20:command-use:command may not be aliased or mutated",
				"image_child_test.go:23:command-use:command may not be aliased or mutated",
				"image_child_test.go:23:command-use:command may not be aliased or mutated",
				"image_child_test.go:23:process-group-call:want imageKillProcessGroup(command.Process.Pid)",
			},
		},
		"cancel deferred only in nested function": {
			files: map[string]string{"image_child_test.go": imageFixtureReplace(t, imageCleanFixture,
				"\tdefer cancel()\n",
				"\tfunc() {\n\t\tdefer cancel()\n\t}()\n")},
			want: []string{
				"image_child_test.go:13:bound-name-use:cancel has 1 unexpected uses",
				"image_child_test.go:13:cancel-not-deferred:bindings 1, direct defers 0",
			},
		},
		"cancel deferred conditionally": {
			files: map[string]string{"image_child_test.go": imageFixtureReplace(t, imageCleanFixture,
				"\tdefer cancel()\n",
				"\tif tool != \"\" {\n\t\tdefer cancel()\n\t}\n")},
			want: []string{
				"image_child_test.go:13:bound-name-use:cancel has 1 unexpected uses",
				"image_child_test.go:13:cancel-not-deferred:bindings 1, direct defers 0",
			},
		},
		"duplicate bare exec Command": {
			files: map[string]string{"image_child_test.go": imageFixtureReplace(t, imageCleanFixture,
				imageCommandBlock,
				imageCommandBlock+"\t_ = exec.Command(\"one\")\n\t_ = exec.Command(\"two\")\n")},
			want: []string{
				"image_child_test.go:23:exec-command:call",
				"image_child_test.go:24:exec-command:call",
			},
		},
		"extra syscall Kill": {
			files: map[string]string{"image_child_test.go": imageCleanFixture +
				"\nfunc imageKillHost() { _ = syscall.Kill(1, syscall.SIGKILL) }\n"},
			want: []string{
				"<package>:1:syscall-kill-count:got 2, want 1",
				"<package>:1:syscall-sigkill-count:got 2, want 1",
				"image_child_test.go:35:syscall-shape:Kill",
			},
		},
		"extra SysProcAttr field": {
			files: map[string]string{"image_child_test.go": imageFixtureReplace(t, imageCleanFixture,
				"&syscall.SysProcAttr{Setpgid: true}",
				"&syscall.SysProcAttr{Setpgid: true, Setsid: true}")},
			want: []string{"image_child_test.go:18:syscall-shape:SysProcAttr"},
		},
		"mutated SysProcAttr after assignment": {
			files: map[string]string{"image_child_test.go": imageFixtureReplace(t, imageCleanFixture,
				"\tcommand.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}\n",
				"\tcommand.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}\n"+
					"\tcommand.SysProcAttr.Setpgid = false\n")},
			want: []string{
				"<package>:1:process-field-count:got 2, want 1",
				"image_child_test.go:19:command-use:command may not be aliased or mutated",
			},
		},
		"SysProcAttr on fake command": {
			files: map[string]string{"image_child_test.go": imageCleanFixture +
				"\nfunc imageShapeOnly() {\n\tcommand := fakeCommand{}\n" +
				"\tcommand.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}\n}\n"},
			want: []string{
				"<package>:1:process-field-count:got 2, want 1",
				"<package>:1:syscall-sysprocattr-count:got 2, want 1",
				"image_child_test.go:37:syscall-shape:SysProcAttr",
			},
		},
		"extra process group caller": {
			files: map[string]string{"image_child_test.go": imageCleanFixture +
				"\nfunc imageKillHost() { _ = imageKillProcessGroup(1) }\n"},
			want: []string{
				"<package>:1:process-group-call-count:got 3, want 2",
				"<package>:1:process-group-reference-count:got 3, want 2",
				"image_child_test.go:35:process-group-call:want imageKillProcessGroup(command.Process.Pid)",
			},
		},
		"mutated command process pid": {
			files: map[string]string{"image_child_test.go": imageFixtureReplace(t, imageCleanFixture,
				"\t\tdeadlineCancelled.Store(true); return imageKillProcessGroup(command.Process.Pid)\n",
				"\t\tdeadlineCancelled.Store(true); command.Process.Pid = 1; "+
					"return imageKillProcessGroup(command.Process.Pid)\n")},
			want: []string{
				"image_child_test.go:19:command-use:command may not be aliased or mutated",
				"image_child_test.go:20:command-use:command may not be aliased or mutated",
			},
		},
		"overwritten command cancel": {
			files: map[string]string{"image_child_test.go": imageFixtureReplace(t, imageCleanFixture,
				"\t}\n\t_ = imageKillProcessGroup(command.Process.Pid); _ = command.Run()\n",
				"\t}\n\tcommand.Cancel = func() error { return nil }\n"+
					"\t_ = imageKillProcessGroup(command.Process.Pid); _ = command.Run()\n")},
			want: []string{
				"image_child_test.go:22:command-use:command may not be aliased or mutated",
			},
		},
		"conditional command cancel": {
			files: map[string]string{"image_child_test.go": imageFixtureReplace(t, imageCleanFixture,
				"\tcommand.Cancel = func() error {\n",
				"\tcommand.Cancel = func() error {\n"+
					"\t\tif tool != \"bash\" { return nil }\n")},
			want: []string{
				"image_child_test.go:19:command-use:command may not be aliased or mutated",
			},
		},
		"conditional command run": {
			files: map[string]string{"image_child_test.go": imageFixtureReplace(t, imageCleanFixture,
				"\t_ = imageKillProcessGroup(command.Process.Pid); _ = command.Run()\n",
				"\t_ = imageKillProcessGroup(command.Process.Pid)\n"+
					"\tvar runErr error\n"+
					"\tif tool == \"bash\" { runErr = command.Run() }\n"+
					"\t_ = runErr\n")},
			want: []string{
				"image_child_test.go:24:command-use:command may not be aliased or mutated",
			},
		},
		"conditional SysProcAttr": {
			files: map[string]string{"image_child_test.go": imageFixtureReplace(t, imageCleanFixture,
				"\tcommand.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}\n",
				"\tif tool == \"bash\" {\n"+
					"\t\tcommand.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}\n"+
					"\t}\n")},
			want: []string{
				"image_child_test.go:19:command-use:command may not be aliased or mutated",
			},
		},
		"missing command run": {
			files: map[string]string{"image_child_test.go": imageFixtureReplace(t, imageCleanFixture,
				"; _ = command.Run()",
				"")},
			want: []string{
				"image_child_test.go:13:command-run-count:got 0, want 1",
			},
		},
		"early runner return": {
			files: map[string]string{"image_child_test.go": imageFixtureReplace(t, imageCleanFixture,
				"\tcommand := exec.CommandContext(ctx, tool, args...)\n",
				"\tcommand := exec.CommandContext(ctx, tool, args...)\n"+
					"\tif tool != \"bash\" { return imageChildResult{} }\n")},
			want: []string{
				"image_child_test.go:13:runner-return-count:got 2, want 1",
			},
		},
		"shadowed predeclared true": {
			files: map[string]string{"image_child_test.go": imageFixtureReplace(t, imageCleanFixture,
				"\tcommand.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}\n",
				"\ttrue := tool == \"bash\"\n"+
					"\tcommand.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}\n")},
			want: []string{
				"image_child_test.go:18:restricted-name-shadow:true",
			},
		},
		"CommandContext result bound under another name": {
			files: map[string]string{"image_child_test.go": imageFixtureReplace(t, imageCleanFixture,
				"\tcommand := exec.CommandContext(ctx, tool, args...)\n",
				"\tother := exec.CommandContext(ctx, tool, args...)\n\tcommand := other\n")},
			want: []string{"image_child_test.go:17:command-binding:want command identifier"},
		},
		"rewritten process group pid": {
			files: map[string]string{"image_child_test.go": imageFixtureReplace(t, imageCleanFixture,
				"\terr := syscall.Kill(-pid, syscall.SIGKILL)",
				"\tpid = -1\n\terr := syscall.Kill(-pid, syscall.SIGKILL)")},
			want: []string{"image_child_test.go:29:syscall-shape:Kill"},
		},
		"build constraint": {
			files: map[string]string{"image_child_test.go": "//go:build linux\n\n" + imageCleanFixture},
			want:  []string{"image_child_test.go:1:build-constraint:valid"},
		},
	}

	for name, fixture := range fixtures {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()

			for file, source := range fixture.files {
				require.NoError(t, os.WriteFile(filepath.Join(dir, file), []byte(source), 0o600))
			}

			findings, err := imageAuditDir(dir)
			require.NoError(t, err)

			want := append([]string(nil), fixture.want...)
			if want == nil {
				want = []string{}
			}
			sort.Strings(want)
			require.Equal(t, want, imageFindingKeys(findings))
		})
	}
}
