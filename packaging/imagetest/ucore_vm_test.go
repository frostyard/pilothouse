package imagetest

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"mvdan.cc/sh/v3/syntax"
)

const (
	imageVMRunnerPath        = "test/image/ucore-vm-test.sh"
	imageVMGuestPath         = "test/image/guest/validate-ucore.sh"
	imageCapabilityJQProgram = `
    if length == 1 and
       (.[0].result.capabilities |
            type == "array" and
            all(.[]; type == "string" and test("^[a-z0-9][a-z0-9-]*$")))
    then .[0].result.capabilities[]
    else error("want exactly one response with canonical capability identifiers")
    end
`
	imageExpectedHostJQProgram = `
    def observed($slot):
        .status[$slot] as $entry |
        if $entry == null then null
        else {
            image: ($entry.image.image.image // ""),
            digest: ($entry.image.imageDigest // "")
        }
        end;
    {
        booted: observed("booted"),
        staged: observed("staged"),
        rollback: observed("rollback")
    }
`
	imageActualHostJQProgram = `
    def reported($entry):
        if $entry == null then null
        else {
            image: ($entry.image // ""),
            digest: ($entry.digest // "")
        }
        end;
    .result | {
        booted: reported(.booted),
        staged: reported(.staged),
        rollback: reported(.rollback)
    }
`
	imageWindowAVCJQProgram = `
    test("avc:[[:space:]]+denied"; "i") | not
`
	imagePilothouseAVCJQProgram = `
    [
        splits("\n") |
        select(
            test("avc:[[:space:]]+denied"; "i") and
            test("pilothouse|pilothoused|/run/pilothouse|/var/lib/pilothouse"; "i")
        )
    ] | length == 0
`
)

func imageReadHarness(t *testing.T, path string) string {
	t.Helper()

	content, err := os.ReadFile(filepath.Join(imageRepositoryRoot(t), path))
	require.NoError(t, err)

	return string(content)
}

func imageParseShell(t *testing.T, path string, language syntax.LangVariant) *syntax.File {
	t.Helper()

	return imageParseShellSource(t, path, imageReadHarness(t, path), language)
}

func imageParseShellSource(t *testing.T, path, source string, language syntax.LangVariant) *syntax.File {
	t.Helper()

	file, err := syntax.NewParser(syntax.Variant(language)).Parse(strings.NewReader(source), path)
	require.NoError(t, err)
	return file
}

func imageShellFunction(t *testing.T, path string, file *syntax.File, name string) syntax.Node {
	t.Helper()

	functions := imageShellFunctions(file, name)
	require.Lenf(t, functions, 1, "%s must define %s() exactly once", path, name)
	return functions[0]
}

func imageShellFunctions(file *syntax.File, name string) []syntax.Node {
	var functions []syntax.Node
	syntax.Walk(file, func(node syntax.Node) bool {
		decl, ok := node.(*syntax.FuncDecl)
		if ok && decl.Name != nil && decl.Name.Value == name {
			functions = append(functions, decl.Body)
		}
		return true
	})
	return functions
}

func imageRequireExactFunction(
	t *testing.T,
	path string,
	file *syntax.File,
	language syntax.LangVariant,
	name string,
	body string,
) {
	t.Helper()

	expected := imageParseShellSource(
		t,
		"expected-"+name+".sh",
		name+"() {\n"+body+"\n}\n",
		language,
	)
	require.Equal(
		t,
		imageShellRender(t, imageShellFunction(t, "expected-"+name+".sh", expected, name)),
		imageShellRender(t, imageShellFunction(t, path, file, name)),
		"%s must retain the exact reviewed %s() implementation",
		path,
		name,
	)
}

func imageRequireUniqueFunctions(t *testing.T, path string, file *syntax.File) {
	t.Helper()
	counts := map[string]int{}
	syntax.Walk(file, func(node syntax.Node) bool {
		decl, ok := node.(*syntax.FuncDecl)
		if ok && decl.Name != nil {
			counts[decl.Name.Value]++
		}
		return true
	})
	for name, count := range counts {
		require.Equalf(t, 1, count, "%s must define %s() exactly once", path, name)
	}
}

func imageRequireExactFunctionSet(t *testing.T, path string, file *syntax.File, want ...string) {
	t.Helper()
	var names []string
	syntax.Walk(file, func(node syntax.Node) bool {
		decl, ok := node.(*syntax.FuncDecl)
		if ok && decl.Name != nil {
			names = append(names, decl.Name.Value)
		}
		return true
	})
	require.ElementsMatchf(t, want, names,
		"%s must define exactly the reviewed function set; extra functions can shadow safety commands", path)
}

func imageShellTopLevel(file *syntax.File) []syntax.Node {
	var nodes []syntax.Node
	for _, stmt := range file.Stmts {
		if _, isFunction := stmt.Cmd.(*syntax.FuncDecl); !isFunction {
			nodes = append(nodes, stmt)
		}
	}
	return nodes
}

type imageShellCall struct {
	args        []string
	assignments []string
	staticArgs  []string
	static      []bool
	line        uint
}

type imageShellDeclaration struct {
	variant string
	name    string
	value   string
}

type imageShellAssignment struct {
	name  string
	value string
}

type imageShellWrite struct {
	command          string
	fd               string
	op               string
	target           string
	descriptorTarget bool
	line             uint
}

type imageShellWriter struct {
	command string
	fd      string
	op      string
}

func imageShellRender(t *testing.T, node syntax.Node) string {
	t.Helper()
	var output bytes.Buffer
	require.NoError(t, syntax.NewPrinter(syntax.Minify(true)).Print(&output, node))
	return output.String()
}

func imageShellStaticWord(word *syntax.Word) (string, bool) {
	var value strings.Builder
	var appendParts func([]syntax.WordPart) bool
	appendParts = func(parts []syntax.WordPart) bool {
		for _, part := range parts {
			switch part := part.(type) {
			case *syntax.Lit:
				if strings.Contains(part.Value, `\`) {
					return false
				}
				value.WriteString(part.Value)
			case *syntax.SglQuoted:
				if part.Dollar && strings.Contains(part.Value, `\`) {
					return false
				}
				value.WriteString(part.Value)
			case *syntax.DblQuoted:
				if !appendParts(part.Parts) {
					return false
				}
			default:
				return false
			}
		}
		return true
	}
	if !appendParts(word.Parts) {
		return "", false
	}
	return value.String(), true
}

func imageShellStaticArgument(argument string) (string, bool) {
	file, err := syntax.NewParser(syntax.Variant(syntax.LangBash)).
		Parse(strings.NewReader(argument), "argument.sh")
	if err != nil || len(file.Stmts) != 1 {
		return "", false
	}
	call, ok := file.Stmts[0].Cmd.(*syntax.CallExpr)
	if !ok || len(call.Args) != 1 || len(call.Assigns) != 0 || len(file.Stmts[0].Redirs) != 0 {
		return "", false
	}
	return imageShellStaticWord(call.Args[0])
}

func imageShellCalls(t *testing.T, roots ...syntax.Node) []imageShellCall {
	t.Helper()
	return imageCollectShellCalls(t, false, roots...)
}

func imageShellAllCalls(t *testing.T, roots ...syntax.Node) []imageShellCall {
	t.Helper()
	return imageCollectShellCalls(t, true, roots...)
}

func imageCollectShellCalls(t *testing.T, includeFunctions bool, roots ...syntax.Node) []imageShellCall {
	t.Helper()
	var calls []imageShellCall
	for _, root := range roots {
		syntax.Walk(root, func(node syntax.Node) bool {
			if node == nil {
				return true
			}
			if _, nestedFunction := node.(*syntax.FuncDecl); nestedFunction && !includeFunctions {
				return false
			}
			call, ok := node.(*syntax.CallExpr)
			if !ok || len(call.Args) == 0 {
				return true
			}
			args := make([]string, 0, len(call.Args))
			staticArgs := make([]string, 0, len(call.Args))
			static := make([]bool, 0, len(call.Args))
			for _, word := range call.Args {
				args = append(args, imageShellRender(t, word))
				value, ok := imageShellStaticWord(word)
				staticArgs = append(staticArgs, value)
				static = append(static, ok)
			}
			assignments := make([]string, 0, len(call.Assigns))
			for _, assignment := range call.Assigns {
				assignments = append(assignments, imageShellRender(t, assignment))
			}
			calls = append(calls, imageShellCall{
				args: args, assignments: assignments,
				staticArgs: staticArgs, static: static, line: call.Pos().Line(),
			})
			return true
		})
	}
	return calls
}

func countImageFailingTests(t *testing.T, roots []syntax.Node, condition string) int {
	t.Helper()
	matches := 0
	for _, root := range roots {
		syntax.Walk(root, func(node syntax.Node) bool {
			if node == nil {
				return true
			}
			if _, nestedFunction := node.(*syntax.FuncDecl); nestedFunction {
				return false
			}
			binary, ok := node.(*syntax.BinaryCmd)
			if !ok || binary.Op.String() != "||" {
				return true
			}
			clause, ok := binary.X.Cmd.(*syntax.TestClause)
			if !ok || imageShellRender(t, clause) != condition {
				return true
			}
			failure, ok := binary.Y.Cmd.(*syntax.CallExpr)
			if !ok || len(failure.Args) == 0 || imageShellRender(t, failure.Args[0]) != "fail" {
				return true
			}
			matches++
			return true
		})
	}
	return matches
}

func imageRequireFailingTest(t *testing.T, roots []syntax.Node, condition string) {
	t.Helper()
	matches := countImageFailingTests(t, roots, condition)
	require.Equalf(t, 1, matches, "%s must occur exactly once as the left side of || fail", condition)
}

func imageRequireDirectFailingTest(t *testing.T, file *syntax.File, condition string) {
	t.Helper()
	matches := 0
	for _, statement := range file.Stmts {
		binary, ok := statement.Cmd.(*syntax.BinaryCmd)
		if !ok || binary.Op.String() != "||" {
			continue
		}
		clause, ok := binary.X.Cmd.(*syntax.TestClause)
		if !ok || imageShellRender(t, clause) != condition {
			continue
		}
		failure, ok := binary.Y.Cmd.(*syntax.CallExpr)
		if !ok || len(failure.Args) == 0 || imageShellRender(t, failure.Args[0]) != "fail" {
			continue
		}
		matches++
		require.Empty(t, statement.Redirs)
		for _, direct := range []*syntax.Stmt{statement, binary.X, binary.Y} {
			require.False(t, direct.Background)
			require.False(t, direct.Negated)
		}
	}
	require.Equalf(t, 1, matches,
		"critical comparison %s must be one direct foreground top-level || fail statement",
		condition)
}

func imageFailingTestLine(t *testing.T, roots []syntax.Node, condition string) uint {
	t.Helper()
	var lines []uint
	for _, root := range roots {
		syntax.Walk(root, func(node syntax.Node) bool {
			if node == nil {
				return true
			}
			if _, nestedFunction := node.(*syntax.FuncDecl); nestedFunction {
				return false
			}
			binary, ok := node.(*syntax.BinaryCmd)
			if !ok || binary.Op.String() != "||" {
				return true
			}
			clause, ok := binary.X.Cmd.(*syntax.TestClause)
			if !ok || imageShellRender(t, clause) != condition {
				return true
			}
			failure, ok := binary.Y.Cmd.(*syntax.CallExpr)
			if ok && len(failure.Args) > 0 && imageShellRender(t, failure.Args[0]) == "fail" {
				lines = append(lines, binary.Pos().Line())
			}
			return true
		})
	}
	require.Lenf(t, lines, 1, "want one failing comparison %s", condition)
	return lines[0]
}

func imageShellDeclarations(t *testing.T, roots ...syntax.Node) []imageShellDeclaration {
	t.Helper()
	var declarations []imageShellDeclaration
	for _, root := range roots {
		syntax.Walk(root, func(node syntax.Node) bool {
			if node == nil {
				return true
			}
			if _, nestedFunction := node.(*syntax.FuncDecl); nestedFunction {
				return false
			}
			clause, ok := node.(*syntax.DeclClause)
			if !ok {
				return true
			}
			for _, assignment := range clause.Args {
				if assignment.Name == nil || assignment.Value == nil {
					continue
				}
				declarations = append(declarations, imageShellDeclaration{
					variant: clause.Variant.Value,
					name:    assignment.Name.Value,
					value:   imageShellRender(t, assignment.Value),
				})
			}
			return true
		})
	}
	return declarations
}

func imageShellAssignments(t *testing.T, roots ...syntax.Node) []imageShellAssignment {
	t.Helper()
	var assignments []imageShellAssignment
	for _, root := range roots {
		syntax.Walk(root, func(node syntax.Node) bool {
			if node == nil {
				return true
			}
			if _, nestedFunction := node.(*syntax.FuncDecl); nestedFunction {
				return false
			}
			assignment, ok := node.(*syntax.Assign)
			if !ok || assignment.Name == nil {
				return true
			}
			value := ""
			if assignment.Value != nil {
				value = imageShellRender(t, assignment.Value)
			} else if assignment.Array != nil {
				var elements []string
				for _, element := range assignment.Array.Elems {
					if element.Value != nil {
						elements = append(elements, imageShellRender(t, element.Value))
					}
				}
				value = strings.Join(elements, " ")
			}
			assignments = append(assignments, imageShellAssignment{
				name:  assignment.Name.Value,
				value: value,
			})
			return true
		})
	}
	return assignments
}

func imageShellAssignmentOnlyCalls(t *testing.T, roots ...syntax.Node) [][]string {
	t.Helper()
	var calls [][]string
	for _, root := range roots {
		syntax.Walk(root, func(node syntax.Node) bool {
			if node == nil {
				return true
			}
			if _, nestedFunction := node.(*syntax.FuncDecl); nestedFunction {
				return false
			}
			call, ok := node.(*syntax.CallExpr)
			if !ok || len(call.Args) != 0 || len(call.Assigns) == 0 {
				return true
			}
			assignments := make([]string, 0, len(call.Assigns))
			for _, assignment := range call.Assigns {
				assignments = append(assignments, imageShellRender(t, assignment))
			}
			calls = append(calls, assignments)
			return true
		})
	}
	return calls
}

func imageShellStatementAssignmentNames(statement *syntax.Stmt) []string {
	var assignments []*syntax.Assign
	switch command := statement.Cmd.(type) {
	case *syntax.CallExpr:
		assignments = command.Assigns
	case *syntax.DeclClause:
		assignments = command.Args
	default:
		return nil
	}
	names := make([]string, 0, len(assignments))
	for _, assignment := range assignments {
		if assignment.Name != nil {
			names = append(names, assignment.Name.Value)
		}
	}
	return names
}

func imageRequireContiguousAssignmentSets(
	t *testing.T,
	file *syntax.File,
	want ...[]string,
) {
	t.Helper()
	matches := 0
	for start := 0; start+len(want) <= len(file.Stmts); start++ {
		matched := true
		for offset, names := range want {
			if !slices.Equal(imageShellStatementAssignmentNames(file.Stmts[start+offset]), names) {
				matched = false
				break
			}
		}
		if matched {
			matches++
			for offset := range want {
				statement := file.Stmts[start+offset]
				require.False(t, statement.Background)
				require.False(t, statement.Negated)
				require.Empty(t, statement.Redirs)
			}
		}
	}
	require.Equalf(t, 1, matches,
		"assignment/declaration sequence %#v must occur once contiguously at top level", want)
}

func imageRequireExactTopLevelSequence(
	t *testing.T,
	path string,
	file *syntax.File,
	language syntax.LangVariant,
	source string,
) {
	t.Helper()
	expected := imageParseShellSource(t, "expected-"+filepath.Base(path), source, language)
	expectedStatements := make([]string, 0, len(expected.Stmts))
	for _, statement := range expected.Stmts {
		expectedStatements = append(expectedStatements, imageShellRender(t, statement))
	}
	actualStatements := make([]string, 0, len(file.Stmts))
	for _, statement := range file.Stmts {
		actualStatements = append(actualStatements, imageShellRender(t, statement))
	}
	matches := 0
	for start := 0; start+len(expectedStatements) <= len(actualStatements); start++ {
		if slices.Equal(actualStatements[start:start+len(expectedStatements)], expectedStatements) {
			matches++
		}
	}
	require.Equalf(t, 1, matches,
		"%s must contain the exact reviewed contiguous top-level sequence", path)
}

func imageShellOutputWrites(t *testing.T, roots ...syntax.Node) []imageShellWrite {
	t.Helper()
	var writes []imageShellWrite
	for _, root := range roots {
		syntax.Walk(root, func(node syntax.Node) bool {
			if node == nil {
				return true
			}
			if _, nestedFunction := node.(*syntax.FuncDecl); nestedFunction {
				return false
			}
			statement, ok := node.(*syntax.Stmt)
			if !ok {
				return true
			}
			command := "<compound>"
			if call, isCall := statement.Cmd.(*syntax.CallExpr); isCall {
				command = "<redirection-only>"
				if len(call.Args) > 0 {
					var static bool
					command, static = imageShellStaticWord(call.Args[0])
					if !static {
						command = imageShellRender(t, call.Args[0])
					}
				}
			}
			for _, redirect := range statement.Redirs {
				if redirect.Word == nil {
					continue
				}
				op := redirect.Op.String()
				descriptorTarget := false
				switch op {
				case ">", ">>", ">|", "&>", "<>":
				case ">&", "<&":
					target, static := imageShellStaticWord(redirect.Word)
					descriptorTarget = static && (target == "-" ||
						(target != "" && strings.Trim(target, "0123456789") == ""))
				default:
					continue
				}
				fd := "1"
				if op == "<>" || op == "<&" {
					fd = "0"
				}
				if redirect.N != nil {
					fd = redirect.N.Value
				}
				writes = append(writes, imageShellWrite{
					command:          command,
					fd:               fd,
					op:               op,
					target:           imageShellRender(t, redirect.Word),
					descriptorTarget: descriptorTarget,
					line:             statement.Pos().Line(),
				})
			}
			return true
		})
	}
	return writes
}

func imageShellArrayAssignment(t *testing.T, name string, roots ...syntax.Node) []string {
	t.Helper()
	var matches [][]string
	for _, root := range roots {
		syntax.Walk(root, func(node syntax.Node) bool {
			if node == nil {
				return true
			}
			if _, nestedFunction := node.(*syntax.FuncDecl); nestedFunction {
				return false
			}
			assignment, ok := node.(*syntax.Assign)
			if !ok || assignment.Name == nil || assignment.Name.Value != name || assignment.Array == nil {
				return true
			}
			var elements []string
			for _, element := range assignment.Array.Elems {
				if element.Value != nil {
					elements = append(elements, imageShellRender(t, element.Value))
				}
			}
			matches = append(matches, elements)
			return true
		})
	}
	require.Lenf(t, matches, 1, "shell array %s must be assigned exactly once", name)
	return matches[0]
}

func imageRequireCall(t *testing.T, calls []imageShellCall, want ...string) {
	t.Helper()
	for _, call := range calls {
		if slices.Equal(call.args, want) {
			return
		}
	}
	require.Failf(t, "missing executable shell call", "want args %#v; parsed calls: %#v", want, calls)
}

func imageRequireExactCallCount(t *testing.T, calls []imageShellCall, count int, want ...string) {
	t.Helper()
	require.Equalf(t, count, countImageShellCalls(calls, want...),
		"want exactly %d executable calls with args %#v; parsed calls: %#v", count, want, calls)
}

func imageCallMatchesAny(call imageShellCall, allowed ...[]string) bool {
	for _, want := range allowed {
		if slices.Equal(call.args, want) {
			return true
		}
	}
	return false
}

func countCallsWithEffectiveCommand(calls []imageShellCall, command string) int {
	count := 0
	for _, call := range calls {
		if imageCallEffectiveCommand(call) == command {
			count++
		}
	}
	return count
}

func imageExactCallLine(t *testing.T, calls []imageShellCall, want ...string) uint {
	t.Helper()
	var lines []uint
	for _, call := range calls {
		if slices.Equal(call.args, want) {
			lines = append(lines, call.line)
		}
	}
	require.Lenf(t, lines, 1, "want one executable call with args %#v", want)
	return lines[0]
}

func countImageShellCalls(calls []imageShellCall, want ...string) int {
	count := 0
	for _, call := range calls {
		if slices.Equal(call.args, want) {
			count++
		}
	}
	return count
}

func imageCallStaticArgument(call imageShellCall, index int) (string, bool) {
	if index < 0 || index >= len(call.args) {
		return "", false
	}
	if len(call.static) == len(call.args) && len(call.staticArgs) == len(call.args) {
		return call.staticArgs[index], call.static[index]
	}
	return imageShellStaticArgument(call.args[index])
}

func imageCallHasStaticCommand(call imageShellCall) bool {
	commandIndex := 0
	for commandIndex < len(call.args) {
		command, ok := imageCallStaticArgument(call, commandIndex)
		if !ok {
			return false
		}
		if command != "builtin" && command != "command" {
			return true
		}
		commandIndex++
		for commandIndex < len(call.args) {
			option, ok := imageCallStaticArgument(call, commandIndex)
			if !ok {
				return false
			}
			if !strings.HasPrefix(option, "-") {
				break
			}
			if option == "-v" || option == "-V" {
				return true
			}
			commandIndex++
		}
	}
	return false
}

func imageCallContainsProgram(call imageShellCall, want string) bool {
	for index := range call.args {
		literal, ok := imageCallStaticArgument(call, index)
		if !ok {
			continue
		}
		if literal == want || filepath.Base(literal) == want {
			return true
		}
	}
	return false
}

func imageCallMutatesShellResolution(call imageShellCall) bool {
	if len(call.args) == 0 {
		return false
	}
	for index := range call.args {
		argument, ok := imageCallStaticArgument(call, index)
		if !ok {
			continue
		}
		if slices.Contains(
			[]string{"alias", "enable", "eval", "hash", "shopt", "source", "unalias"},
			argument,
		) {
			return true
		}
	}
	return imageCallEffectiveCommand(call) == "."
}

func imageCallEffectiveCommand(call imageShellCall) string {
	commandIndex := 0
	for commandIndex < len(call.args) {
		command, ok := imageCallStaticArgument(call, commandIndex)
		if !ok {
			return ""
		}
		if command != "builtin" && command != "command" {
			break
		}
		commandIndex++
		for commandIndex < len(call.args) {
			option, ok := imageCallStaticArgument(call, commandIndex)
			if !ok {
				return ""
			}
			if !strings.HasPrefix(option, "-") {
				break
			}
			commandIndex++
		}
	}
	if commandIndex >= len(call.args) {
		return ""
	}
	command, _ := imageCallStaticArgument(call, commandIndex)
	return command
}

func imageGuestReviewedTopLevelCommand(call imageShellCall) string {
	if slices.Equal(call.args, []string{"command", "-v", `"$tool"`}) {
		return "command-v"
	}
	return filepath.Base(imageCallEffectiveCommand(call))
}

func imageGuestCommandIsReviewed(call imageShellCall) bool {
	switch imageGuestReviewedTopLevelCommand(call) {
	case ":", "[", "bootc", "broker_query", "cat", "chmod", "chpasswd", "cmp",
		"command-v", "curl", "exit", "fail", "getenforce", "getent", "grep", "id",
		"journalctl", "jq", "log", "mktemp", "printf", "rpm-ostree", "sed", "set",
		"sort", "systemctl", "systemd-sysext", "trap", "unset", "useradd", "usermod":
		return true
	default:
		return false
	}
}

func imageCallIsForbiddenEvidenceMutator(call imageShellCall) bool {
	for index := range call.args {
		argument, static := imageCallStaticArgument(call, index)
		if static && slices.Contains(
			[]string{
				"bash", "cp", "dash", "dd", "env", "find", "install", "ln", "mv",
				"nice", "nohup", "read", "rsync", "setsid", "sh", "tee", "timeout",
				"touch", "truncate", "xargs",
			},
			filepath.Base(argument),
		) {
			return true
		}
	}
	command := filepath.Base(imageCallEffectiveCommand(call))
	if command == "curl" {
		for _, argument := range call.args {
			for _, evidence := range []string{
				"actual", "expected", "bootc-status.json", "expected-host-image.json",
				"actual-host-image.json", "new-avcs", "boot-journal",
			} {
				if strings.Contains(argument, evidence) {
					return true
				}
			}
		}
	}
	if command == "sed" {
		for _, argument := range call.args[1:] {
			if argument == "-i" || strings.HasPrefix(argument, "-i") ||
				argument == "--in-place" || strings.HasPrefix(argument, "--in-place=") {
				return true
			}
		}
	}
	if command == "printf" {
		for _, argument := range call.args[1:] {
			if argument == "-v" || strings.HasPrefix(argument, "-v") {
				return true
			}
		}
	}
	return false
}

func imageCallInvokesHostProgram(call imageShellCall, program string) bool {
	if !imageCallContainsProgram(call, program) || len(call.args) == 0 {
		return false
	}
	switch call.args[0] {
	case "guest_run", "guest_run_long", "guest_run_timeout", "guest_probe":
		return false
	default:
		return true
	}
}

func imageRequireExactShellMode(t *testing.T, file *syntax.File, want ...string) {
	t.Helper()
	allCalls := imageShellAllCalls(t, file)
	imageRequireExactCallCount(t, allCalls, 1, want...)
	require.NotEmpty(t, file.Stmts)
	first, ok := file.Stmts[0].Cmd.(*syntax.CallExpr)
	require.True(t, ok, "the shell error mode must be the first executable statement")
	var firstArgs []string
	for _, word := range first.Args {
		firstArgs = append(firstArgs, imageShellRender(t, word))
	}
	require.Equal(t, want, firstArgs,
		"the shell error mode must be established before any other executable statement")
	for _, call := range allCalls {
		for index := range call.args {
			argument, ok := imageCallStaticArgument(call, index)
			if !ok || argument != "set" {
				continue
			}
			require.Equal(t, want, call.args,
				"the reviewed shell error mode must be the only invocation of set")
		}
		require.Falsef(t, imageCallMutatesShellResolution(call),
			"shell resolution mutation could replace the reviewed error mode or safety commands: %#v",
			call.args)
	}
}

func countImageFailingCalls(t *testing.T, roots []syntax.Node, want ...string) int {
	t.Helper()
	matches := 0
	for _, root := range roots {
		syntax.Walk(root, func(node syntax.Node) bool {
			if node == nil {
				return true
			}
			if _, nestedFunction := node.(*syntax.FuncDecl); nestedFunction {
				return false
			}
			binary, ok := node.(*syntax.BinaryCmd)
			if !ok || binary.Op.String() != "||" {
				return true
			}
			call, ok := binary.X.Cmd.(*syntax.CallExpr)
			if !ok {
				return true
			}
			args := make([]string, 0, len(call.Args))
			for _, word := range call.Args {
				args = append(args, imageShellRender(t, word))
			}
			if !slices.Equal(args, want) {
				return true
			}
			failure, ok := binary.Y.Cmd.(*syntax.CallExpr)
			if !ok || len(failure.Args) == 0 || imageShellRender(t, failure.Args[0]) != "fail" {
				return true
			}
			matches++
			return true
		})
	}
	return matches
}

func imageRequireFailingCall(t *testing.T, roots []syntax.Node, want ...string) {
	t.Helper()
	require.Equalf(t, 1, countImageFailingCalls(t, roots, want...),
		"call %#v must occur exactly once as the left side of || fail", want)
}

func imageRequireDirectFailingCallCount(
	t *testing.T,
	file *syntax.File,
	count int,
	want ...string,
) {
	t.Helper()
	matches := 0
	for _, statement := range file.Stmts {
		binary, ok := statement.Cmd.(*syntax.BinaryCmd)
		if !ok || binary.Op.String() != "||" {
			continue
		}
		call, ok := binary.X.Cmd.(*syntax.CallExpr)
		if !ok {
			continue
		}
		var args []string
		for _, word := range call.Args {
			args = append(args, imageShellRender(t, word))
		}
		if !slices.Equal(args, want) {
			continue
		}
		failure, ok := binary.Y.Cmd.(*syntax.CallExpr)
		if !ok || len(failure.Args) == 0 || imageShellRender(t, failure.Args[0]) != "fail" {
			continue
		}
		matches++
		require.Empty(t, statement.Redirs)
		for _, direct := range []*syntax.Stmt{statement, binary.X, binary.Y} {
			require.False(t, direct.Background)
			require.False(t, direct.Negated)
		}
	}
	require.Equalf(t, count, matches,
		"critical evidence call %#v must have %d direct foreground top-level || fail statements",
		want, count)
}

func imageRequireDirectFailingCall(t *testing.T, file *syntax.File, want ...string) {
	t.Helper()
	imageRequireDirectFailingCallCount(t, file, 1, want...)
}

func imageRequireDirectCall(t *testing.T, file *syntax.File, want ...string) {
	t.Helper()
	matches := 0
	for _, statement := range file.Stmts {
		call, ok := statement.Cmd.(*syntax.CallExpr)
		if !ok {
			continue
		}
		var args []string
		for _, word := range call.Args {
			args = append(args, imageShellRender(t, word))
		}
		if !slices.Equal(args, want) {
			continue
		}
		matches++
		require.False(t, statement.Background)
		require.False(t, statement.Negated)
		require.Empty(t, statement.Redirs)
	}
	require.Equalf(t, 1, matches,
		"critical call %#v must be one direct foreground top-level statement", want)
}

func countImageStatusCaptures(
	t *testing.T,
	roots []syntax.Node,
	statusVariable string,
	want ...string,
) int {
	t.Helper()
	matches := 0
	for _, root := range roots {
		syntax.Walk(root, func(node syntax.Node) bool {
			if node == nil {
				return true
			}
			if _, nestedFunction := node.(*syntax.FuncDecl); nestedFunction {
				return false
			}
			binary, ok := node.(*syntax.BinaryCmd)
			if !ok || binary.Op.String() != "||" {
				return true
			}
			call, ok := binary.X.Cmd.(*syntax.CallExpr)
			if !ok {
				return true
			}
			args := make([]string, 0, len(call.Args))
			for _, word := range call.Args {
				args = append(args, imageShellRender(t, word))
			}
			if !slices.Equal(args, want) {
				return true
			}
			capture, ok := binary.Y.Cmd.(*syntax.CallExpr)
			if !ok || len(capture.Args) != 0 || len(capture.Assigns) != 1 {
				return true
			}
			assignment := capture.Assigns[0]
			if assignment.Name == nil || assignment.Name.Value != statusVariable ||
				assignment.Value == nil || imageShellRender(t, assignment.Value) != "$?" {
				return true
			}
			matches++
			return true
		})
	}
	return matches
}

func imageRequireOrderedCalls(t *testing.T, calls []imageShellCall, names ...string) {
	t.Helper()
	index := 0
	for _, call := range calls {
		if len(call.args) > 0 && call.args[0] == names[index] {
			index++
			if index == len(names) {
				return
			}
		}
	}
	require.Failf(t, "missing ordered executable calls", "want command names %#v; parsed calls: %#v", names, calls)
}

func imageRequireDeclaration(t *testing.T, declarations []imageShellDeclaration, want imageShellDeclaration) {
	t.Helper()
	require.Contains(t, declarations, want)
}

func imageCallPublishes(call imageShellCall) bool {
	for index, argument := range call.args {
		switch argument {
		case "podman", "docker":
			tail := call.args[index+1:]
			if slices.Contains(tail, "push") {
				return true
			}
		case "podman_fixture":
			if index+2 >= len(call.args) {
				continue
			}
			tail := call.args[index+2:]
			if slices.Contains(tail, "push") {
				return true
			}
		case "skopeo":
			if slices.Contains(call.args[index+1:], "copy") {
				return true
			}
		}
	}
	return false
}

func imageCallRecursivelyRemoves(call imageShellCall) bool {
	for index, argument := range call.args {
		if argument != "rm" {
			continue
		}
		recursive := false
		for _, option := range call.args[index+1:] {
			switch option {
			case "--recursive":
				recursive = true
			default:
				if strings.HasPrefix(option, "-") && !strings.HasPrefix(option, "--") {
					flags := strings.TrimLeft(option, "-")
					recursive = recursive || strings.ContainsAny(flags, "rR")
				}
			}
		}
		if recursive {
			return true
		}
	}
	return false
}

func imageCallUsesRegistry(call imageShellCall) bool {
	for _, argument := range call.args {
		if strings.Contains(argument, "docker://") || argument == "registry:2" {
			return true
		}
	}
	return false
}

func imageCallContainsSequence(call imageShellCall, want ...string) bool {
	for start := 0; start+len(want) <= len(call.args); start++ {
		if slices.Equal(call.args[start:start+len(want)], want) {
			return true
		}
	}
	return false
}

func imageCallRunsFixture(call imageShellCall) bool {
	for index, argument := range call.args {
		if argument == "podman_fixture" && index+2 < len(call.args) && call.args[index+2] == "run" {
			return true
		}
	}
	return false
}

func TestUCoreVMHarnessModesAndShellcheck(t *testing.T) {
	root := imageRepositoryRoot(t)

	runner, err := os.Stat(filepath.Join(root, imageVMRunnerPath))
	require.NoError(t, err)
	require.NotZerof(t, runner.Mode().Perm()&0o111,
		"%s is the directly executed entry point", imageVMRunnerPath)

	guest, err := os.Stat(filepath.Join(root, imageVMGuestPath))
	require.NoError(t, err)
	require.Zerof(t, guest.Mode().Perm()&0o111,
		"%s is copied and invoked through an explicit sh interpreter", imageVMGuestPath)

	for path, dialect := range map[string]string{
		imageVMRunnerPath: "bash",
		imageVMGuestPath:  "sh",
	} {
		language := syntax.LangPOSIX
		if dialect == "bash" {
			language = syntax.LangBash
		}
		file := imageParseShell(t, path, language)
		imageRequireUniqueFunctions(t, path, file)
		for _, call := range imageShellAllCalls(t, file) {
			require.Truef(t, imageCallHasStaticCommand(call),
				"%s must not use a dynamic or non-literal command position: %#v", path, call.args)
		}
		if dialect == "bash" {
			imageRequireExactFunctionSet(t, path, file,
				"usage", "fail", "log", "manifest_value", "assert_storage_path",
				"podman_fixture", "stop_qemu", "remove_install_container",
				"detach_disk_loops", "cleanup", "cleanup_on_exit", "find_ovmf",
				"guest_run", "guest_run_long", "guest_run_timeout", "guest_probe",
				"guest_copy", "wait_for_ssh", "wait_for_ssh_gone", "wait_for_broker",
				"reboot_guest", "guest_status_digest", "guest_status_name",
				"run_guest_validation",
			)
			imageRequireExactShellMode(t, file, "set", "-euo", "pipefail")
			imageRequireExactFunction(t, path, file, language, "fail", `
echo "ucore-vm-test: $*" >&2
exit 1
`)
			imageRequireExactFunction(t, path, file, language, "log", `
echo "ucore-vm-test: $*"
`)
		} else {
			imageRequireExactFunctionSet(t, path, file,
				"fail", "log", "cleanup", "broker_query",
			)
			imageRequireExactShellMode(t, file, "set", "-eu")
			imageRequireExactFunction(t, path, file, language, "fail", `
printf 'ucore guest: %s\n' "$*" >&2
exit 1
`)
		}
		sandbox := newImageSandbox(t)
		result := imageRunChild(t, sandbox, imageRequireTool(t, "shellcheck"),
			"--shell="+dialect, filepath.Join(root, path))
		require.NoErrorf(t, result.Err, "shellcheck %s failed:\n%s", path, result.Stderr)
		require.Empty(t, strings.TrimSpace(result.Stdout))
		require.Empty(t, strings.TrimSpace(result.Stderr))
	}
}

func TestUCoreVMRunnerConsumesOnlyThePrivateComposedFixture(t *testing.T) {
	file := imageParseShell(t, imageVMRunnerPath, syntax.LangBash)
	topLevel := imageShellTopLevel(file)
	topCalls := imageShellCalls(t, topLevel...)
	topDeclarations := imageShellDeclarations(t, topLevel...)
	criticalDeclarationNames := map[string]bool{
		"GUEST_SCRIPT": true, "IMAGE_DIR": true, "IMAGE_MANIFEST": true,
		"VM_DIR": true, "DISK_IMAGE": true, "UPDATE_ARCHIVE": true,
		"SSH_KEY": true, "CREDENTIALS": true, "OVMF_CODE": true, "OVMF_VARS": true,
		"INSTALL_CONTAINER": true,
	}
	var criticalDeclarations []imageShellDeclaration
	for _, declaration := range topDeclarations {
		if criticalDeclarationNames[declaration.name] {
			criticalDeclarations = append(criticalDeclarations, declaration)
		}
	}
	require.Equal(t, []imageShellDeclaration{
		{variant: "readonly", name: "GUEST_SCRIPT", value: `"$SCRIPT_DIR/guest/validate-ucore.sh"`},
		{variant: "readonly", name: "IMAGE_DIR", value: `"$workspace/fixture-ucore-images"`},
		{variant: "readonly", name: "IMAGE_MANIFEST", value: `"$IMAGE_DIR/fixture.json"`},
		{variant: "readonly", name: "VM_DIR", value: `"$workspace/fixture-ucore-vm"`},
		{variant: "readonly", name: "DISK_IMAGE", value: `"$VM_DIR/disk.raw"`},
		{variant: "readonly", name: "UPDATE_ARCHIVE", value: `"$VM_DIR/update.oci"`},
		{variant: "readonly", name: "SSH_KEY", value: `"$VM_DIR/id_ed25519"`},
		{variant: "readonly", name: "CREDENTIALS", value: `"$VM_DIR/credentials.json"`},
		{variant: "readonly", name: "OVMF_CODE", value: `"$VM_DIR/OVMF_CODE.fd"`},
		{variant: "readonly", name: "OVMF_VARS", value: `"$VM_DIR/OVMF_VARS.fd"`},
		{variant: "readonly", name: "INSTALL_CONTAINER",
			value: `"pilothouse-image-install-$ssh_port"`},
	}, criticalDeclarations,
		"every derived runner resource must have one exact readonly declaration")
	imageRequireExactFunction(t, imageVMRunnerPath, file, syntax.LangBash, "manifest_value", `
local expression="$1"
jq -er "$expression | select(type == \"string\" and length > 0)" "$IMAGE_MANIFEST"
`)
	imageRequireExactFunction(t, imageVMRunnerPath, file, syntax.LangBash, "assert_storage_path", `
local actual="$1" expected="$2"
[[ "$actual" == "$expected" ]] ||
    fail "fixture storage path escaped its fixed workspace location: $actual"
`)
	imageRequireExactFunction(t, imageVMRunnerPath, file, syntax.LangBash, "podman_fixture", `
TMPDIR="$image_tmpdir" timeout --signal=TERM --kill-after=30s "$1" \
    podman "${podman_args[@]}" "${@:2}"
`)
	var scriptPathAssignments []imageShellAssignment
	for _, assignment := range imageShellAssignments(t, topLevel...) {
		if assignment.name == "SCRIPT_PATH" || assignment.name == "SCRIPT_DIR" {
			scriptPathAssignments = append(scriptPathAssignments, assignment)
		}
	}
	require.Equal(t, []imageShellAssignment{
		{name: "SCRIPT_PATH", value: `"$(readlink -f -- "${BASH_SOURCE[0]}")"`},
		{name: "SCRIPT_PATH", value: ""},
		{name: "SCRIPT_DIR", value: `"$(dirname "$SCRIPT_PATH")"`},
		{name: "SCRIPT_DIR", value: ""},
	}, scriptPathAssignments,
		"the canonical repository script path and directory must have reviewed captures and readonlys")
	imageRequireContiguousAssignmentSets(t, file,
		[]string{"SCRIPT_PATH"},
		[]string{"SCRIPT_PATH"},
		[]string{"SCRIPT_DIR"},
		[]string{"SCRIPT_DIR"},
		[]string{"GUEST_SCRIPT"},
	)
	expectedGuestSource := imageParseShellSource(t, "expected-guest-source.sh", `
[[ -f "$GUEST_SCRIPT" && ! -L "$GUEST_SCRIPT" &&
   -s "$GUEST_SCRIPT" && -r "$GUEST_SCRIPT" ]] ||
    fail "guest validation script is missing, empty or not a regular file: $GUEST_SCRIPT"
`, syntax.LangBash)
	require.Len(t, expectedGuestSource.Stmts, 1)
	expectedGuestSourceBinary, ok := expectedGuestSource.Stmts[0].Cmd.(*syntax.BinaryCmd)
	require.True(t, ok)
	guestSourceCondition := imageShellRender(t, expectedGuestSourceBinary.X.Cmd)
	imageRequireFailingTest(t, topLevel, guestSourceCondition)
	imageRequireDirectFailingTest(t, file, guestSourceCondition)
	var workspaceReadonlyIndex = -1
	for index, statement := range file.Stmts {
		declaration, ok := statement.Cmd.(*syntax.DeclClause)
		if !ok || declaration.Variant == nil || declaration.Variant.Value != "readonly" {
			continue
		}
		if slices.Equal(imageShellStatementAssignmentNames(statement),
			[]string{"workspace", "canonical_workspace", "ssh_port"}) {
			require.Equal(t, -1, workspaceReadonlyIndex,
				"workspace identity must become readonly exactly once")
			workspaceReadonlyIndex = index
		}
	}
	require.Positive(t, workspaceReadonlyIndex,
		"workspace identity must become readonly after argument validation")
	expectedPortValidation := imageParseShellSource(t, "expected-port-validation.sh", `
if [[ ! "$ssh_port" =~ ^[0-9]+$ ]] ||
   ((ssh_port < 1024 || ssh_port > 65535)); then
    fail "--ssh-port must be an integer from 1024 through 65535"
fi
`, syntax.LangBash)
	require.Len(t, expectedPortValidation.Stmts, 1)
	require.Equal(t,
		imageShellRender(t, expectedPortValidation.Stmts[0]),
		imageShellRender(t, file.Stmts[workspaceReadonlyIndex-1]),
		"workspace and port must become readonly immediately after the exact port validation")
	expectedCanonicalValidation := imageParseShellSource(t, "expected-canonical-validation.sh", `
[[ "$workspace" == "$canonical_workspace" ]] ||
    fail "--workspace must be its canonical absolute path"
`, syntax.LangBash)
	require.Len(t, expectedCanonicalValidation.Stmts, 1)
	require.Equal(t,
		imageShellRender(t, expectedCanonicalValidation.Stmts[0]),
		imageShellRender(t, file.Stmts[workspaceReadonlyIndex-2]),
		"canonical workspace equality, port validation and readonly must be contiguous")
	require.False(t, file.Stmts[workspaceReadonlyIndex].Background)
	require.False(t, file.Stmts[workspaceReadonlyIndex].Negated)
	require.Empty(t, file.Stmts[workspaceReadonlyIndex].Redirs)
	imageRequireExactTopLevelSequence(t, imageVMRunnerPath, file, syntax.LangBash, `
workspace=""
ssh_port="2222"
while (($#)); do
    case "$1" in
        --workspace)
            (($# >= 2)) || { usage; exit 2; }
            workspace="$2"
            shift 2
            ;;
        --ssh-port)
            (($# >= 2)) || { usage; exit 2; }
            ssh_port="$2"
            shift 2
            ;;
        *)
            usage
            exit 2
            ;;
    esac
done

[[ -n "$workspace" && "$workspace" == /* && -d "$workspace" ]] ||
    fail "--workspace must name an existing absolute directory"
canonical_workspace="$(cd "$workspace" && pwd -P)"
[[ "$workspace" == "$canonical_workspace" ]] ||
    fail "--workspace must be its canonical absolute path"
if [[ ! "$ssh_port" =~ ^[0-9]+$ ]] ||
   ((ssh_port < 1024 || ssh_port > 65535)); then
    fail "--ssh-port must be an integer from 1024 through 65535"
fi
readonly workspace canonical_workspace ssh_port
`)
	topAssignments := imageShellAssignments(t, topLevel...)
	for name, want := range map[string][]string{
		"workspace":           {`""`, `"$2"`, ""},
		"canonical_workspace": {`"$(cd "$workspace"&&pwd -P)"`, ""},
		"ssh_port":            {`"2222"`, `"$2"`, ""},
	} {
		var observed []string
		for _, assignment := range topAssignments {
			if assignment.name == name {
				observed = append(observed, assignment.value)
			}
		}
		require.Equalf(t, want, observed,
			"%s must retain only its reviewed parse/canonicalize/readonly assignments", name)
	}

	for _, path := range [][]string{
		{"assert_storage_path", `"$storage_root"`, `"$IMAGE_DIR/storage"`},
		{"assert_storage_path", `"$image_store"`, `"$IMAGE_DIR/imagestore"`},
		{"assert_storage_path", `"$run_root"`, `"$IMAGE_DIR/runroot"`},
		{"assert_storage_path", `"$podman_tmpdir"`, `"$IMAGE_DIR/libpod-tmp"`},
		{"assert_storage_path", `"$image_tmpdir"`, `"$IMAGE_DIR/image-tmp"`},
		{"assert_storage_path", `"$storage_config"`, `"$IMAGE_DIR/storage.conf"`},
	} {
		imageRequireCall(t, topCalls, path...)
		imageRequireDirectCall(t, file, path...)
	}
	manifestTrustCall := []string{
		"jq", "-e",
		"'\n    .schema == 1 and\n    .kind == \"pilothouse-ucore-image-fixture\" and\n    .producer_uid == 0 and\n    .source == \"ghcr.io/ublue-os/ucore:latest\" and\n    .baseline.slot == \"baseline\" and\n    .update.slot == \"update\"\n'",
		`"$IMAGE_MANIFEST"`,
	}
	imageRequireCall(t, topCalls, manifestTrustCall...)
	imageRequireFailingCall(t, topLevel, manifestTrustCall...)
	imageRequireDirectFailingCall(t, file, manifestTrustCall...)
	imageRequireFailingTest(t, topLevel, `[[ $EUID -eq 0 ]]`)
	imageRequireDirectFailingTest(t, file, `[[ $EUID -eq 0 ]]`)
	imageRequireFailingTest(t, topLevel, `[[ "$actual_id" == "$expected_id" ]]`)
	imageRequireFailingTest(t, topLevel, `[[ "${baseline_ref%:*}" == "${update_ref%:*}" ]]`)

	podmanNode := imageShellFunction(t, imageVMRunnerPath, file, "podman_fixture")
	podmanCalls := imageShellCalls(t, podmanNode)
	podmanWrapperCall := []string{
		"timeout", "--signal=TERM", "--kill-after=30s", `"$1"`,
		"podman", `"${podman_args[@]}"`, `"${@:2}"`,
	}
	imageRequireExactCallCount(t, podmanCalls, 1, podmanWrapperCall...)
	podmanArgs := imageShellArrayAssignment(t, "podman_args", topLevel...)
	require.Equal(t, []string{
		"--remote=false",
		"--root",
		`"$storage_root"`,
		"--imagestore",
		`"$image_store"`,
		"--runroot",
		`"$run_root"`,
		"--tmpdir",
		`"$podman_tmpdir"`,
		"--events-backend",
		"none",
		"--storage-driver",
		"overlay",
	}, podmanArgs)

	for _, call := range imageShellAllCalls(t, file) {
		require.False(t, imageCallPublishes(call),
			"the fixture consumer must never publish or copy an image: %#v", call.args)
		require.False(t, imageCallRecursivelyRemoves(call),
			"the fixture consumer must leave recursive workspace cleanup to the exact-store owner")
		if imageCallInvokesHostProgram(call, "podman") {
			require.Equal(t, podmanWrapperCall, call.args,
				"every host Podman invocation must go through the bounded private-store wrapper")
		}
	}
}

func TestUCoreVMRunnerUsesComposefsAndLocalUpdateTransport(t *testing.T) {
	file := imageParseShell(t, imageVMRunnerPath, syntax.LangBash)
	topLevel := imageShellTopLevel(file)
	mainCalls := imageShellCalls(t, topLevel...)
	allCalls := imageShellAllCalls(t, file)

	installCall := []string{
		"podman_fixture", "45m", "run",
		"--rm",
		"--name", `"$INSTALL_CONTAINER"`,
		"--privileged",
		"--pid=host",
		"--volume", "/dev:/dev",
		"--volume", `"$workspace:$workspace"`,
		"--volume", `"$SSH_KEY.pub:/run/pilothouse-image-test-key.pub:ro"`,
		"--security-opt", "label=type:unconfined_t",
		"--env", "CONTAINERS_CONF=/dev/null",
		"--env", `"CONTAINERS_STORAGE_CONF=$storage_config"`,
		"--env", `"TMPDIR=$image_tmpdir"`,
		`"$baseline_ref"`,
		"bootc", "install", "to-disk",
		"--generic-image",
		"--via-loopback",
		"--skip-fetch-check",
		"--composefs-backend",
		"--filesystem", "btrfs",
		"--karg", "console=ttyS0",
		"--root-ssh-authorized-keys", "/run/pilothouse-image-test-key.pub",
		`"$DISK_IMAGE"`,
	}
	imageRequireExactCallCount(t, allCalls, 1, installCall...)
	updateSaveCall := []string{
		"podman_fixture", "20m", "save", "--format", "oci-archive",
		"--output", `"$UPDATE_ARCHIVE"`, `"$update_ref"`,
	}
	imageRequireCall(t, mainCalls, updateSaveCall...)
	imageRequireDirectFailingCall(t, file, "detach_disk_loops")
	installLine := imageExactCallLine(t, allCalls, installCall...)
	postInstallDetachLine := imageExactCallLine(t, mainCalls, "detach_disk_loops")
	updateSaveLine := imageExactCallLine(t, mainCalls, updateSaveCall...)
	require.Less(t, installLine, postInstallDetachLine,
		"the installer must finish before its disk-backed loops are detached")
	require.Less(t, postInstallDetachLine, updateSaveLine,
		"the loop-free install handoff must precede update export and QEMU work")
	imageRequireCall(t, mainCalls,
		"guest_run_long", "podman", "load", "--input", "/var/tmp/pilothouse-image-update.oci",
	)
	switchCall := []string{
		"guest_run_long", "bootc", "switch", "--quiet",
		"--transport", "containers-storage", `"$update_ref"`,
	}
	imageRequireExactCallCount(t, allCalls, 1, switchCall...)
	reviewedGuestCopies := [][]string{
		{"guest_copy", `"$GUEST_SCRIPT"`, "/root/validate-ucore.sh"},
		{"guest_copy", `"$CREDENTIALS"`, "/root/pilothouse-image-credentials.json"},
		{"guest_copy", `"$UPDATE_ARCHIVE"`, "/var/tmp/pilothouse-image-update.oci"},
	}
	reviewedGuestRuns := [][]string{
		{"guest_run", "chmod", "0700", "/root/validate-ucore.sh"},
		{"guest_run", "chmod", "0600", "/root/pilothouse-image-credentials.json"},
		{"guest_run", "sh", "/root/validate-ucore.sh", "prepare"},
		{"guest_run", "rm", "-f", "/var/tmp/pilothouse-image-update.oci"},
	}
	reviewedGuestLongRuns := [][]string{
		{"guest_run_long", "podman", "load", "--input", "/var/tmp/pilothouse-image-update.oci"},
		switchCall,
		{"guest_run_long", "bootc", "rollback"},
	}
	for _, call := range mainCalls {
		for _, bridgeProgram := range []string{"ssh", "scp", "sftp", "rsync"} {
			require.Falsef(t, imageCallContainsProgram(call, bridgeProgram),
				"the runner main path must reach the guest only through reviewed wrappers; call: %#v",
				call.args)
		}
		require.NotContains(t,
			[]string{"bash", "dash", "env", "nc", "ncat", "sh", "socat", "xargs"},
			filepath.Base(imageCallEffectiveCommand(call)),
			"the runner main path must not add an alternate guest-command wrapper")
		switch imageCallEffectiveCommand(call) {
		case "guest_copy":
			require.True(t, imageCallMatchesAny(call, reviewedGuestCopies...),
				"the runner may copy only the validator, credentials and local update archive: %#v",
				call.args)
		case "guest_run":
			require.True(t, imageCallMatchesAny(call, reviewedGuestRuns...),
				"the runner may issue only the reviewed direct guest setup/removal calls: %#v",
				call.args)
		case "guest_run_long":
			require.True(t, imageCallMatchesAny(call, reviewedGuestLongRuns...),
				"the runner may issue only the reviewed long guest update calls: %#v",
				call.args)
		case "guest_probe", "guest_run_timeout":
			require.Failf(t, "direct low-level guest bridge call",
				"%s may appear only inside its exact reviewed wrapper body: %#v",
				imageCallEffectiveCommand(call), call.args)
		}
	}
	require.Len(t, reviewedGuestCopies, countCallsWithEffectiveCommand(mainCalls, "guest_copy"))
	require.Len(t, reviewedGuestRuns, countCallsWithEffectiveCommand(mainCalls, "guest_run"))
	require.Len(t, reviewedGuestLongRuns, countCallsWithEffectiveCommand(mainCalls, "guest_run_long"))
	for _, call := range allCalls {
		require.False(t, imageCallUsesRegistry(call),
			"the update must not use a registry transport: %#v", call.args)
		if imageCallContainsSequence(call, "bootc", "switch") {
			require.Equal(t, switchCall, call.args,
				"the local containers-storage switch must be the only bootc switch")
		}
		if imageCallRunsFixture(call) {
			require.Equal(t, installCall, call.args,
				"the bounded baseline installer must be the only fixture Podman run")
		}
	}

	for _, continuity := range []string{
		`[[ "$(guest_status_digest booted)" == "$staged_digest" ]]`,
		`[[ "$(guest_status_digest rollback)" == "$baseline_booted" ]]`,
		`[[ "$(guest_status_digest booted)" == "$pre_rollback_target" ]]`,
		`[[ "$(guest_status_digest rollback)" == "$pre_rollback_booted" ]]`,
	} {
		imageRequireFailingTest(t, topLevel, continuity)
		imageRequireDirectFailingTest(t, file, continuity)
	}
	stagedShapeCondition := `[[ "$staged_name" == "$update_ref" && "$staged_digest" =~ $DIGEST_PATTERN ]]`
	imageRequireFailingTest(t, topLevel, stagedShapeCondition)
	imageRequireDirectFailingTest(t, file, stagedShapeCondition)
	imageRequireCall(t, mainCalls, "run_guest_validation", "update")
	require.Equal(t, 2, countImageShellCalls(mainCalls, "run_guest_validation", "baseline"),
		"the baseline must be checked both before update and after rollback")
	directValidationCalls := []imageShellCall{}
	for _, statement := range file.Stmts {
		call, ok := statement.Cmd.(*syntax.CallExpr)
		if !ok {
			continue
		}
		var args []string
		for _, word := range call.Args {
			args = append(args, imageShellRender(t, word))
		}
		if len(args) == 0 || args[0] != "run_guest_validation" {
			continue
		}
		require.False(t, statement.Background)
		require.False(t, statement.Negated)
		require.Empty(t, statement.Redirs)
		directValidationCalls = append(directValidationCalls,
			imageShellCall{args: args, line: statement.Pos().Line()})
	}
	imageRequireExactCallCount(t, directValidationCalls, 2, "run_guest_validation", "baseline")
	imageRequireExactCallCount(t, directValidationCalls, 1, "run_guest_validation", "update")
	var baselineValidationLines []uint
	var updateValidationLine uint
	for _, call := range directValidationCalls {
		switch {
		case slices.Equal(call.args, []string{"run_guest_validation", "baseline"}):
			baselineValidationLines = append(baselineValidationLines, call.line)
		case slices.Equal(call.args, []string{"run_guest_validation", "update"}):
			updateValidationLine = call.line
		}
	}
	require.Len(t, baselineValidationLines, 2)
	require.NotZero(t, updateValidationLine)

	var rebootLines []uint
	for _, statement := range file.Stmts {
		call, ok := statement.Cmd.(*syntax.CallExpr)
		if !ok || len(call.Args) != 1 || imageShellRender(t, call.Args[0]) != "reboot_guest" {
			continue
		}
		require.False(t, statement.Background)
		require.False(t, statement.Negated)
		rebootLines = append(rebootLines, statement.Pos().Line())
	}
	require.Len(t, rebootLines, 2,
		"update and rollback must each use one direct proven reboot")

	switchLine := imageExactCallLine(t, mainCalls, switchCall...)
	stagedNameLine := imageExactCallLine(t, mainCalls, "guest_status_name", "staged")
	stagedDigestLine := imageExactCallLine(t, mainCalls, "guest_status_digest", "staged")
	stagedShapeLine := imageFailingTestLine(t, topLevel, stagedShapeCondition)
	rollbackLine := imageExactCallLine(t, mainCalls, "guest_run_long", "bootc", "rollback")
	updateContinuityLines := []uint{
		imageFailingTestLine(t, topLevel, `[[ "$(guest_status_digest booted)" == "$staged_digest" ]]`),
		imageFailingTestLine(t, topLevel, `[[ "$(guest_status_digest rollback)" == "$baseline_booted" ]]`),
	}
	rollbackContinuityLines := []uint{
		imageFailingTestLine(t, topLevel, `[[ "$(guest_status_digest booted)" == "$pre_rollback_target" ]]`),
		imageFailingTestLine(t, topLevel, `[[ "$(guest_status_digest rollback)" == "$pre_rollback_booted" ]]`),
	}
	require.Less(t, baselineValidationLines[0], switchLine)
	require.Less(t, switchLine, stagedNameLine)
	require.Less(t, switchLine, stagedDigestLine)
	require.Less(t, stagedNameLine, stagedShapeLine)
	require.Less(t, stagedDigestLine, stagedShapeLine)
	require.Less(t, stagedShapeLine, rebootLines[0])
	require.Less(t, switchLine, rebootLines[0])
	for _, continuityLine := range updateContinuityLines {
		require.Less(t, rebootLines[0], continuityLine)
		require.Less(t, continuityLine, updateValidationLine)
	}
	require.Less(t, updateValidationLine, rollbackLine)
	require.Less(t, rollbackLine, rebootLines[1])
	for _, continuityLine := range rollbackContinuityLines {
		require.Less(t, rebootLines[1], continuityLine)
		require.Less(t, continuityLine, baselineValidationLines[1])
	}
}

func TestUCoreVMRunnerOwnsAndWaitsForEveryLiveResource(t *testing.T) {
	file := imageParseShell(t, imageVMRunnerPath, syntax.LangBash)
	allCalls := imageShellAllCalls(t, file)
	topCalls := imageShellCalls(t, imageShellTopLevel(file)...)
	loopQueryCall := []string{"losetup", "-j", `"$DISK_IMAGE"`}
	loopDetachCall := []string{
		"timeout", "--signal=TERM", "--kill-after=10s", "30s",
		"losetup", "--detach", `"$loop"`,
	}
	imageRequireExactCallCount(t, allCalls, 2, loopQueryCall...)
	imageRequireExactCallCount(t, allCalls, 1, loopDetachCall...)
	reviewedImageInspectCall := []string{
		"podman_fixture", "2m", "image", "inspect",
		"--format", "'{{.Id}}'", `"$ref"`,
	}
	reviewedImageSaveCall := []string{
		"podman_fixture", "20m", "save", "--format", "oci-archive",
		"--output", `"$UPDATE_ARCHIVE"`, `"$update_ref"`,
	}
	reviewedContainerRemoveCall := []string{
		"podman_fixture", "2m", "rm", "--force", "--ignore", `"$INSTALL_CONTAINER"`,
	}
	for _, call := range allCalls {
		if imageCallContainsProgram(call, "losetup") {
			require.True(t,
				slices.Equal(call.args, loopQueryCall) ||
					slices.Equal(call.args, loopDetachCall),
				"all loop-device access must remain inside the exact cleanup implementation: %#v",
				call.args)
		}
		if imageCallEffectiveCommand(call) == "podman_fixture" {
			require.True(t,
				slices.Equal(call.args, reviewedImageInspectCall) ||
					slices.Equal(call.args, reviewedImageSaveCall) ||
					slices.Equal(call.args, reviewedContainerRemoveCall) ||
					imageCallRunsFixture(call),
				"private-store Podman may only inspect, install, save or remove the named installer: %#v",
				call.args)
		}
	}
	imageRequireExactCallCount(t, topCalls, 3, "exit", "2")
	for _, call := range topCalls {
		command := imageCallEffectiveCommand(call)
		if command == "exit" {
			require.Equal(t, []string{"exit", "2"}, call.args,
				"the runner must not gain a successful early exit before VM evidence")
		}
		require.NotContains(t, []string{"exec", "return"}, command,
			"the runner main path must not gain an alternate early termination command")
	}
	imageRequireExactCallCount(t, allCalls, 1, "trap", "'cleanup_on_exit $?'", "EXIT")
	imageRequireExactCallCount(t, allCalls, 2, "trap", "-", "EXIT")
	for _, call := range allCalls {
		if !imageCallContainsProgram(call, "trap") {
			continue
		}
		require.True(t,
			slices.Equal(call.args, []string{"trap", "'cleanup_on_exit $?'", "EXIT"}) ||
				slices.Equal(call.args, []string{"trap", "-", "EXIT"}),
			"the harness must not install an additional signal/error trap: %#v", call.args)
	}
	imageRequireExactFunction(t, imageVMRunnerPath, file, syntax.LangBash, "stop_qemu", `
local pid="${qemu_pid:-}"
[[ -n "$pid" ]] || return 0

if kill -0 "$pid" 2>/dev/null; then
    kill "$pid" 2>/dev/null || true
    for _ in {1..20}; do
        kill -0 "$pid" 2>/dev/null || break
        sleep 0.5
    done
    if kill -0 "$pid" 2>/dev/null; then
        kill -KILL "$pid" 2>/dev/null || true
    fi
fi
wait "$pid" 2>/dev/null || true
kill -0 "$pid" 2>/dev/null && return 1
`)
	imageRequireExactFunction(t, imageVMRunnerPath, file, syntax.LangBash, "remove_install_container", `
podman_fixture 2m rm --force --ignore "$INSTALL_CONTAINER" >/dev/null
`)
	imageRequireExactFunction(t, imageVMRunnerPath, file, syntax.LangBash, "detach_disk_loops", `
local failed=0 loop listing remaining
listing="$(losetup -j "$DISK_IMAGE" 2>/dev/null)" || return 1
while IFS= read -r loop; do
    [[ -n "$loop" ]] || continue
    timeout --signal=TERM --kill-after=10s 30s \
        losetup --detach "$loop" || failed=1
done < <(awk -F: '{print $1}' <<<"$listing")

remaining="$(losetup -j "$DISK_IMAGE" 2>/dev/null)" || failed=1
[[ -z "$remaining" ]] || failed=1
return "$failed"
`)
	imageRequireExactFunction(t, imageVMRunnerPath, file, syntax.LangBash, "cleanup", `
local failed=0
stop_qemu || failed=1
remove_install_container || failed=1
detach_disk_loops || failed=1
return "$failed"
`)
	imageRequireExactFunction(t, imageVMRunnerPath, file, syntax.LangBash, "cleanup_on_exit", `
local status="$1"
trap - EXIT
cleanup || {
    echo "ucore-vm-test: cleanup did not fully stop processes and detach the VM disk" >&2
    [[ "$status" -ne 0 ]] || status=1
}
exit "$status"
`)
	imageRequireExactFunction(t, imageVMRunnerPath, file, syntax.LangBash, "guest_run", `
guest_run_timeout 2m "$@"
`)
	imageRequireExactFunction(t, imageVMRunnerPath, file, syntax.LangBash, "guest_run_long", `
guest_run_timeout 20m "$@"
`)
	imageRequireExactFunction(t, imageVMRunnerPath, file, syntax.LangBash, "guest_run_timeout", `
local duration="$1"
shift
timeout --signal=TERM --kill-after=10s "$duration" \
    ssh "${ssh_options[@]}" root@127.0.0.1 -- "$@"
`)
	imageRequireExactFunction(t, imageVMRunnerPath, file, syntax.LangBash, "guest_probe", `
timeout --signal=TERM --kill-after=5s 15s \
    ssh "${ssh_options[@]}" root@127.0.0.1 -- "$@"
`)
	imageRequireExactFunction(t, imageVMRunnerPath, file, syntax.LangBash, "guest_copy", `
local source="$1" destination="$2"
timeout --signal=TERM --kill-after=10s 20m \
    scp "${ssh_common_options[@]}" -P "$ssh_port" -- "$source" "root@127.0.0.1:$destination"
`)
	imageRequireExactFunction(t, imageVMRunnerPath, file, syntax.LangBash, "wait_for_ssh", `
local started=$SECONDS deadline=$((SECONDS + 300))
while ((SECONDS < deadline)); do
    if [[ -n "${qemu_pid:-}" ]] && ! kill -0 "$qemu_pid" 2>/dev/null; then
        fail "QEMU exited before the guest answered SSH"
    fi
    if guest_probe true >/dev/null 2>&1; then
        log "guest answered SSH after $((SECONDS - started))s"
        return 0
    fi
    sleep 5
done
fail "guest did not answer SSH within 300s"
`)
	imageRequireExactFunction(t, imageVMRunnerPath, file, syntax.LangBash, "wait_for_ssh_gone", `
local misses=0 deadline=$((SECONDS + 120))
while ((SECONDS < deadline)); do
    if guest_probe true >/dev/null 2>&1; then
        misses=0
    else
        misses=$((misses + 1))
        ((misses >= 3)) && return 0
    fi
    sleep 2
done
fail "pre-reboot sshd was still answering after 120s"
`)
	imageRequireExactFunction(t, imageVMRunnerPath, file, syntax.LangBash, "wait_for_broker", `
local started=$SECONDS deadline=$((SECONDS + 120))
while ((SECONDS < deadline)); do
    if guest_probe test -S /run/pilothouse/broker.sock >/dev/null 2>&1; then
        log "broker socket became ready after $((SECONDS - started))s"
        return 0
    fi
    sleep 2
done
fail "broker socket did not become ready within 120s after SSH"
`)
	imageRequireExactFunction(t, imageVMRunnerPath, file, syntax.LangBash, "reboot_guest", `
local before after output status=0
before="$(guest_run cat /proc/sys/kernel/random/boot_id)"
output="$(guest_run systemctl reboot 2>&1)" || status=$?
if [[ "$status" -ne 0 && "$status" -ne 255 && "$status" -ne 124 ]]; then
    fail "guest reboot command failed with status $status: $output"
fi
wait_for_ssh_gone
wait_for_ssh
wait_for_broker
after="$(guest_run cat /proc/sys/kernel/random/boot_id)"
[[ -n "$before" && -n "$after" && "$before" != "$after" ]] ||
    fail "guest answered after reboot without changing boot_id"
`)
	imageRequireExactFunction(t, imageVMRunnerPath, file, syntax.LangBash, "guest_status_digest", `
local slot="$1"
guest_run bootc status --format json |
    jq -er --arg slot "$slot" '.status[$slot].image.imageDigest // empty'
`)
	imageRequireExactFunction(t, imageVMRunnerPath, file, syntax.LangBash, "guest_status_name", `
local slot="$1"
guest_run bootc status --format json |
    jq -er --arg slot "$slot" '.status[$slot].image.image.image // empty'
`)
	imageRequireExactFunction(t, imageVMRunnerPath, file, syntax.LangBash, "run_guest_validation", `
local expected_slot="$1"
guest_run sh /root/validate-ucore.sh validate "$expected_slot"
`)

	for _, escape := range []string{"setsid", "nohup", "disown", "daemonize"} {
		for _, call := range allCalls {
			require.NotContainsf(t, call.args, escape,
				"%s must not let a helper escape the teardown owner; call: %#v",
				imageVMRunnerPath, call.args)
			for _, argument := range call.args {
				require.NotEqualf(t, escape, strings.TrimLeft(argument, "-"),
					"%s must not let a helper escape via an option; call: %#v",
					imageVMRunnerPath, call.args)
			}
		}
	}
	topAssignments := imageShellAssignments(t, imageShellTopLevel(file)...)
	var criticalAssignmentValues = map[string][]string{
		"storage_root":   {`"$(manifest_value '.storage.root')"`, ""},
		"image_store":    {`"$(manifest_value '.storage.imagestore')"`, ""},
		"run_root":       {`"$(manifest_value '.storage.runroot')"`, ""},
		"podman_tmpdir":  {`"$(manifest_value '.storage.podman_tmpdir')"`, ""},
		"image_tmpdir":   {`"$(manifest_value '.storage.image_tmpdir')"`, ""},
		"storage_config": {`"$(manifest_value '.storage.config')"`, ""},
		"DISK_IMAGE":     {`"$VM_DIR/disk.raw"`},
		"INSTALL_CONTAINER": {
			`"pilothouse-image-install-$ssh_port"`,
		},
		"qemu_pid": {"$!"},
		"baseline_booted": {
			`"$(guest_status_digest booted)"`, "",
		},
		"staged_name": {
			`"$(guest_status_name staged)"`, "",
		},
		"staged_digest": {
			`"$(guest_status_digest staged)"`, "",
		},
		"pre_rollback_booted": {
			`"$(guest_status_digest booted)"`, "",
		},
		"pre_rollback_target": {
			`"$(guest_status_digest rollback)"`, "",
		},
	}
	observedCriticalAssignments := map[string][]string{}
	for _, assignment := range topAssignments {
		if _, critical := criticalAssignmentValues[assignment.name]; critical {
			observedCriticalAssignments[assignment.name] = append(
				observedCriticalAssignments[assignment.name],
				assignment.value,
			)
		}
	}
	require.Equal(t, criticalAssignmentValues, observedCriticalAssignments,
		"cleanup resource identities and the owned QEMU PID must not be reassigned")
	criticalReadonlySets := map[string][]string{
		"fixture-refs": {
			"baseline_ref", "baseline_id", "update_ref", "update_id",
		},
		"fixture-storage": {
			"storage_root", "image_store", "run_root", "podman_tmpdir",
			"image_tmpdir", "storage_config",
		},
		"podman": {"podman_args"},
		"qemu":   {"qemu_pid"},
		"baseline-slot": {
			"baseline_booted",
		},
		"staged-slots": {
			"staged_name", "staged_digest",
		},
		"rollback-slots": {
			"pre_rollback_booted", "pre_rollback_target",
		},
		"image-dir":         {"IMAGE_DIR"},
		"image-manifest":    {"IMAGE_MANIFEST"},
		"vm-dir":            {"VM_DIR"},
		"disk-image":        {"DISK_IMAGE"},
		"update-archive":    {"UPDATE_ARCHIVE"},
		"ssh-key":           {"SSH_KEY"},
		"credentials":       {"CREDENTIALS"},
		"ovmf-code":         {"OVMF_CODE"},
		"ovmf-vars":         {"OVMF_VARS"},
		"install-container": {"INSTALL_CONTAINER"},
	}
	observedReadonlySets := map[string][]string{}
	for _, statement := range file.Stmts {
		declaration, ok := statement.Cmd.(*syntax.DeclClause)
		if !ok || declaration.Variant == nil || declaration.Variant.Value != "readonly" {
			continue
		}
		require.False(t, statement.Background,
			"runner readonly declarations must execute in the parent shell")
		require.False(t, statement.Negated)
		require.Empty(t, statement.Redirs)
		var names []string
		for _, assignment := range declaration.Args {
			if assignment.Name != nil {
				names = append(names, assignment.Name.Value)
			}
		}
		for key, want := range criticalReadonlySets {
			if slices.Equal(names, want) {
				observedReadonlySets[key] = names
			}
		}
	}
	require.Equal(t, criticalReadonlySets, observedReadonlySets,
		"fixture paths, Podman argv and the captured QEMU PID must become readonly")
	imageRequireContiguousAssignmentSets(t, file,
		[]string{"baseline_ref"},
		[]string{"baseline_id"},
		[]string{"update_ref"},
		[]string{"update_id"},
		[]string{"storage_root"},
		[]string{"image_store"},
		[]string{"run_root"},
		[]string{"podman_tmpdir"},
		[]string{"image_tmpdir"},
		[]string{"storage_config"},
		[]string{"baseline_ref", "baseline_id", "update_ref", "update_id"},
		[]string{
			"storage_root", "image_store", "run_root", "podman_tmpdir",
			"image_tmpdir", "storage_config",
		},
	)
	imageRequireContiguousAssignmentSets(t, file,
		[]string{"podman_args"},
		[]string{"podman_args"},
	)
	imageRequireContiguousAssignmentSets(t, file,
		[]string{"baseline_booted"},
		[]string{"baseline_booted"},
	)
	imageRequireContiguousAssignmentSets(t, file,
		[]string{"staged_name"},
		[]string{"staged_digest"},
		[]string{"staged_name", "staged_digest"},
	)
	imageRequireContiguousAssignmentSets(t, file,
		[]string{"pre_rollback_booted"},
		[]string{"pre_rollback_target"},
		[]string{"pre_rollback_booted", "pre_rollback_target"},
	)

	stopNode := imageShellFunction(t, imageVMRunnerPath, file, "stop_qemu")
	stopCalls := imageShellCalls(t, stopNode)
	imageRequireCall(t, stopCalls, "kill", `"$pid"`)
	imageRequireCall(t, stopCalls, "kill", "-KILL", `"$pid"`)
	imageRequireCall(t, stopCalls, "wait", `"$pid"`)

	cleanupNode := imageShellFunction(t, imageVMRunnerPath, file, "cleanup")
	cleanupCalls := imageShellCalls(t, cleanupNode)
	imageRequireOrderedCalls(t, cleanupCalls,
		"stop_qemu", "remove_install_container", "detach_disk_loops",
	)

	detachNode := imageShellFunction(t, imageVMRunnerPath, file, "detach_disk_loops")
	detachCalls := imageShellCalls(t, detachNode)
	imageRequireCall(t, detachCalls,
		"timeout", "--signal=TERM", "--kill-after=10s", "30s",
		"losetup", "--detach", `"$loop"`,
	)
	imageRequireCall(t, detachCalls, "losetup", "-j", `"$DISK_IMAGE"`)

	mainCalls := topCalls
	imageRequireFailingCall(t, imageShellTopLevel(file), "cleanup")
	imageRequireDirectFailingCall(t, file, "cleanup")
	imageRequireExactCallCount(t, mainCalls, 1, "trap", "'cleanup_on_exit $?'", "EXIT")
	imageRequireExactCallCount(t, mainCalls, 1, "trap", "-", "EXIT")
	var armLine, firstResourceLine, cleanupLine, disarmLine uint
	for _, call := range mainCalls {
		switch {
		case slices.Equal(call.args, []string{"trap", "'cleanup_on_exit $?'", "EXIT"}):
			armLine = call.line
		case slices.Equal(call.args, []string{"truncate", "-s", "20G", `"$DISK_IMAGE"`}):
			firstResourceLine = call.line
		case slices.Equal(call.args, []string{"cleanup"}):
			cleanupLine = call.line
		case slices.Equal(call.args, []string{"trap", "-", "EXIT"}):
			disarmLine = call.line
		}
	}
	require.NotZero(t, armLine)
	require.NotZero(t, firstResourceLine)
	require.NotZero(t, cleanupLine)
	require.NotZero(t, disarmLine)
	require.Less(t, armLine, firstResourceLine,
		"the EXIT cleanup trap must be armed before disk, installer, or QEMU resources are created")
	require.Less(t, cleanupLine, disarmLine,
		"success-path cleanup must finish before the EXIT trap is disarmed")
	cleanupStatementIndex := -1
	disarmStatementIndex := -1
	for index, statement := range file.Stmts {
		switch statement.Pos().Line() {
		case cleanupLine:
			cleanupStatementIndex = index
		case disarmLine:
			disarmStatementIndex = index
		}
	}
	require.NotEqual(t, -1, cleanupStatementIndex)
	require.Equal(t, cleanupStatementIndex+1, disarmStatementIndex,
		"successful cleanup must be immediately followed by trap disarm")
	require.Equal(t, len(file.Stmts)-2, disarmStatementIndex,
		"trap disarm must be the penultimate top-level statement")
	finalCall, ok := file.Stmts[len(file.Stmts)-1].Cmd.(*syntax.CallExpr)
	require.True(t, ok, "the runner must end with its exact PASS log")
	var finalArgs []string
	for _, word := range finalCall.Args {
		finalArgs = append(finalArgs, imageShellRender(t, word))
	}
	require.Equal(t, []string{
		"log", `"PASS: uCore baseline, update and rollback satisfied the image-host contract"`,
	}, finalArgs)
	require.False(t, file.Stmts[len(file.Stmts)-1].Background)
	require.False(t, file.Stmts[len(file.Stmts)-1].Negated)
	require.Empty(t, file.Stmts[len(file.Stmts)-1].Redirs)
	directTrapStatements := map[string]int{"arm": 0, "disarm": 0}
	for _, statement := range file.Stmts {
		call, ok := statement.Cmd.(*syntax.CallExpr)
		if !ok {
			continue
		}
		var args []string
		for _, word := range call.Args {
			args = append(args, imageShellRender(t, word))
		}
		kind := ""
		switch {
		case slices.Equal(args, []string{"trap", "'cleanup_on_exit $?'", "EXIT"}):
			kind = "arm"
		case slices.Equal(args, []string{"trap", "-", "EXIT"}):
			kind = "disarm"
		}
		if kind == "" {
			continue
		}
		directTrapStatements[kind]++
		require.False(t, statement.Background,
			"the parent harness must execute the EXIT trap statement")
		require.False(t, statement.Negated)
		require.Empty(t, statement.Redirs)
	}
	require.Equal(t, map[string]int{"arm": 1, "disarm": 1}, directTrapStatements,
		"trap arm and disarm must each be one direct parent-shell statement")
	for _, call := range allCalls {
		for _, arg := range call.args {
			require.NotContains(t, arg, "${loop_device}p3")
			require.NotContains(t, arg, "authorized_keys",
				"the runner must not write an authorized_keys path into a guessed disk layout")
		}
	}
	qemuCall := []string{
		"qemu-system-x86_64",
		"-name", "pilothouse-ucore-image-test",
		"-machine", "q35",
		"-accel", "kvm",
		"-cpu", "host",
		"-smp", "2",
		"-m", "3072",
		"-display", "none",
		"-monitor", "none",
		"-serial", "stdio",
		"-drive", `"if=pflash,format=raw,unit=0,file=$OVMF_CODE,readonly=on"`,
		"-drive", `"if=pflash,format=raw,unit=1,file=$OVMF_VARS"`,
		"-drive", `"file=$DISK_IMAGE,format=raw,if=virtio"`,
		"-netdev", `"user,id=net0,hostfwd=tcp:127.0.0.1:$ssh_port-:22"`,
		"-device", "virtio-net-pci,netdev=net0",
	}
	imageRequireExactCallCount(t, allCalls, 1, qemuCall...)
	qemuStatement := -1
	backgroundStatements := 0
	for index, statement := range file.Stmts {
		if statement.Background {
			backgroundStatements++
		}
		call, ok := statement.Cmd.(*syntax.CallExpr)
		if !ok {
			continue
		}
		var args []string
		for _, word := range call.Args {
			args = append(args, imageShellRender(t, word))
		}
		if slices.Equal(args, qemuCall) {
			require.True(t, statement.Background,
				"QEMU must be a shell-owned background job before its PID is captured")
			qemuStatement = index
		}
	}
	require.NotEqual(t, -1, qemuStatement)
	require.Equal(t, 1, backgroundStatements,
		"the owned QEMU process must be the runner's only top-level background job")
	require.Less(t, qemuStatement+1, len(file.Stmts),
		"QEMU must be followed immediately by qemu_pid=$!")
	pidStatement := file.Stmts[qemuStatement+1]
	require.False(t, pidStatement.Background)
	pidCapture, ok := pidStatement.Cmd.(*syntax.DeclClause)
	require.True(t, ok, "QEMU must be followed immediately by a readonly PID capture")
	require.Equal(t, "readonly", pidCapture.Variant.Value)
	require.Len(t, pidCapture.Args, 1)
	require.Equal(t, "qemu_pid", pidCapture.Args[0].Name.Value)
	require.Equal(t, "$!", imageShellRender(t, pidCapture.Args[0].Value))
	for _, call := range allCalls {
		if imageCallContainsProgram(call, "qemu-system-x86_64") {
			require.Equal(t, qemuCall, call.args,
				"the owned foreground QEMU invocation must be the only QEMU process")
		}
	}
	for _, call := range mainCalls {
		for _, argument := range call.args {
			require.NotContains(t, argument, "console.log")
			require.NotContains(t, argument, "qemu-stderr.log")
		}
	}
}

func TestUCoreGuestChecksOnlyImageHostDeltas(t *testing.T) {
	file := imageParseShell(t, imageVMGuestPath, syntax.LangPOSIX)
	topLevel := imageShellTopLevel(file)
	topCalls := imageShellCalls(t, topLevel...)
	allCalls := imageShellAllCalls(t, file)
	imageRequireExactFunction(t, imageVMGuestPath, file, syntax.LangPOSIX, "broker_query", `
query_id="$1"
output="$2"
status="$(
    curl --silent --show-error --max-time 30 \
        --unix-socket "$BROKER_SOCKET" \
        --request POST \
        --header 'Content-Type: application/json' \
        --header @"$work_dir/auth.header" \
        --data-binary @"$work_dir/query.json" \
        --output "$output" \
        --write-out '%{http_code}' \
        "http://localhost/v1/queries/$query_id"
)" || fail "$query_id did not complete"
[ "$status" = 200 ] ||
    fail "$query_id returned HTTP $status, expected 200"
`)
	imageRequireExactFunction(t, imageVMGuestPath, file, syntax.LangPOSIX, "cleanup", `
rm -f "$work_dir/login.json" "$work_dir/login-body.json" \
    "$work_dir/query.json" "$work_dir/query-body.json" \
    "$work_dir/auth.header" "$work_dir/actual-unsorted" \
    "$work_dir/actual" "$work_dir/expected" \
    "$work_dir/host-image.json" "$work_dir/bootc-status.json" \
    "$work_dir/expected-host-image.json" "$work_dir/actual-host-image.json" \
    "$work_dir/cursor-journal" \
    "$work_dir/new-avcs" "$work_dir/boot-journal"
rmdir "$work_dir"
`)
	imageRequireExactFunction(t, imageVMGuestPath, file, syntax.LangPOSIX, "log", `
printf 'ucore guest: %s\n' "$*"
`)
	loginCurlCall := []string{
		"curl", "--silent", "--show-error", "--max-time", "30",
		"--unix-socket", `"$BROKER_SOCKET"`,
		"--request", "POST",
		"--header", "'Content-Type: application/json'",
		"--data-binary", `@"$work_dir/login.json"`,
		"--output", `"$work_dir/login-body.json"`,
		"--write-out", "'%{http_code}'",
		"http://localhost/v1/login",
	}
	imageRequireExactCallCount(t, topCalls, 1, loginCurlCall...)
	imageRequireExactCallCount(t, allCalls, 1, "exit", "0")
	imageRequireExactCallCount(t, topCalls, 1, "trap", "cleanup", "EXIT")
	imageRequireDirectCall(t, file, "trap", "cleanup", "EXIT")
	for _, call := range topCalls {
		command := imageCallEffectiveCommand(call)
		if command == "exit" {
			require.Equal(t, []string{"exit", "0"}, call.args,
				"the reviewed prepare branch must be the guest harness's only successful early exit")
		}
		require.NotContains(t, []string{"exec", "return"}, command,
			"the guest main path must not gain an alternate early termination command")
		if filepath.Base(command) == "trap" {
			require.Equal(t, []string{"trap", "cleanup", "EXIT"}, call.args,
				"the guest must retain only its reviewed cleanup EXIT trap")
		}
	}
	assignmentOnlyCalls := imageShellAssignmentOnlyCalls(t, topLevel...)
	var assignmentOnlyNames []string
	for _, assignments := range assignmentOnlyCalls {
		require.Len(t, assignments, 1,
			"each reviewed assignment-only statement must assign exactly one variable")
		name, _, found := strings.Cut(assignments[0], "=")
		require.True(t, found)
		assignmentOnlyNames = append(assignmentOnlyNames, name)
	}
	require.ElementsMatch(t, []string{
		"CREDENTIALS", "BROKER_SOCKET", "CAPABILITY_QUERY", "HOST_IMAGE_QUERY",
		"username", "password", "expected_slot", "work_dir", "username", "password",
		"login_status", "token", "journal_cursor",
	}, assignmentOnlyNames,
		"the guest main path must retain only its reviewed assignment-only statements")
	topAssignments := imageShellAssignments(t, topLevel...)
	for _, want := range []imageShellAssignment{
		{name: "CREDENTIALS", value: "/root/pilothouse-image-credentials.json"},
		{name: "BROKER_SOCKET", value: "/run/pilothouse/broker.sock"},
		{name: "CAPABILITY_QUERY", value: "org.frostyard.pilothouse.capabilities.list"},
		{name: "HOST_IMAGE_QUERY", value: "org.frostyard.pilothouse.maintenance.host_image_status"},
		{name: "expected_slot", value: `"${2-}"`},
		{name: "work_dir", value: `"$(mktemp -d)"`},
	} {
		require.Contains(t, topAssignments, want)
	}

	slotMarkerCall := []string{
		"[", `"$(cat /usr/lib/pilothouse-image-test/slot)"`, "=", `"$expected_slot"`, "]",
	}
	imageRequireCall(t, topCalls, slotMarkerCall...)
	imageRequireFailingCall(t, topLevel, slotMarkerCall...)
	imageRequireDirectFailingCall(t, file, slotMarkerCall...)
	enforcingCall := []string{"[", `"$(getenforce)"`, "=", "Enforcing", "]"}
	imageRequireCall(t, topCalls, enforcingCall...)
	imageRequireFailingCall(t, topLevel, enforcingCall...)
	imageRequireDirectFailingCall(t, file, enforcingCall...)
	bootcStatusCall := []string{"bootc", "status", "--json"}
	imageRequireCall(t, topCalls, bootcStatusCall...)
	require.Equal(t, 2, countImageFailingCalls(t, topLevel, bootcStatusCall...),
		"both the initial bootc probe and captured host-image status must fail closed")
	imageRequireDirectFailingCallCount(t, file, 2, bootcStatusCall...)
	bootcCapabilityCall := []string{"grep", "-qx", "bootc", `"$work_dir/actual"`}
	imageRequireCall(t, topCalls, bootcCapabilityCall...)
	imageRequireDirectFailingCall(t, file, bootcCapabilityCall...)
	capabilityBrokerCall := []string{
		"broker_query", `"$CAPABILITY_QUERY"`, `"$work_dir/query-body.json"`,
	}
	imageRequireCall(t, topCalls, capabilityBrokerCall...)
	imageRequireDirectCall(t, file, capabilityBrokerCall...)
	capabilityDecodeCall := []string{
		"jq", "-ser",
		"'" + imageCapabilityJQProgram + "'",
		`"$work_dir/query-body.json"`,
	}
	imageRequireCall(t, topCalls, capabilityDecodeCall...)
	imageRequireFailingCall(t, topLevel, capabilityDecodeCall...)
	imageRequireDirectFailingCall(t, file, capabilityDecodeCall...)
	actualCapabilitySortCall := []string{
		"sort", "-u", `"$work_dir/actual-unsorted"`,
	}
	imageRequireCall(t, topCalls, actualCapabilitySortCall...)
	imageRequireFailingCall(t, topLevel, actualCapabilitySortCall...)
	imageRequireDirectFailingCall(t, file, actualCapabilitySortCall...)
	expectedCapabilitySortCall := []string{
		"sort", "-u", "-o", `"$work_dir/expected"`, `"$work_dir/expected"`,
	}
	imageRequireCall(t, topCalls, expectedCapabilitySortCall...)
	imageRequireFailingCall(t, topLevel, expectedCapabilitySortCall...)
	imageRequireDirectFailingCall(t, file, expectedCapabilitySortCall...)
	cursorJournalCall := []string{
		"journalctl", "--no-pager", "--lines", "0", "--show-cursor",
	}
	imageRequireCall(t, topCalls, cursorJournalCall...)
	imageRequireFailingCall(t, topLevel, cursorJournalCall...)
	imageRequireDirectFailingCall(t, file, cursorJournalCall...)
	cursorDecodeCall := []string{
		"sed", "-n", "'s/^-- cursor: //p'", `"$work_dir/cursor-journal"`,
	}
	imageRequireCall(t, topCalls, cursorDecodeCall...)
	cursorNonemptyCall := []string{"[", "-n", `"$journal_cursor"`, "]"}
	imageRequireCall(t, topCalls, cursorNonemptyCall...)
	imageRequireFailingCall(t, topLevel, cursorNonemptyCall...)
	imageRequireDirectFailingCall(t, file, cursorNonemptyCall...)
	var journalCursorAssignments []imageShellAssignment
	for _, assignment := range imageShellAssignments(t, topLevel...) {
		if assignment.name == "journal_cursor" {
			journalCursorAssignments = append(journalCursorAssignments, assignment)
		}
	}
	require.Equal(t, []imageShellAssignment{{
		name: "journal_cursor", value: `"$(sed -n 's/^-- cursor: //p' "$work_dir/cursor-journal")"`,
	}}, journalCursorAssignments,
		"the journal cursor must have one reviewed decode assignment")

	for _, expectedProbe := range [][]string{
		{"systemctl", "show-environment"},
		{"journalctl", "--no-pager", "--lines", "0"},
		{"systemd-sysext", "list"},
		{"bootc", "status", "--json"},
		{"rpm-ostree", "status", "--json"},
		{"systemctl", "list-unit-files", "bootc-fetch-apply-updates.timer", "--no-legend"},
		{"systemctl", "list-unit-files", "bootc-fetch-apply-updates.service", "--no-legend"},
		{"systemctl", "list-unit-files", "rpm-ostreed-automatic.timer", "--no-legend"},
		{"systemctl", "list-unit-files", "rpm-ostreed-automatic.service", "--no-legend"},
	} {
		imageRequireCall(t, topCalls, expectedProbe...)
	}
	for _, optedOut := range []string{"updex", "podman", "docker", "incus"} {
		require.Zero(t, countImageShellCalls(topCalls, "printf", "%s\\n", optedOut),
			"unconfigured optional dependencies must not enter the expected advertised set")
	}

	hostAvailabilityCall := []string{
		"jq", "-e", "'\n    .result.bootc_available == true\n'",
		`"$work_dir/host-image.json"`,
	}
	hostBrokerCall := []string{
		"broker_query", `"$HOST_IMAGE_QUERY"`, `"$work_dir/host-image.json"`,
	}
	imageRequireCall(t, topCalls, hostBrokerCall...)
	imageRequireDirectCall(t, file, hostBrokerCall...)
	imageRequireExactCallCount(t, topCalls, 1, capabilityBrokerCall...)
	imageRequireExactCallCount(t, topCalls, 1, hostBrokerCall...)
	for _, call := range topCalls {
		if imageCallEffectiveCommand(call) != "broker_query" {
			continue
		}
		require.True(t,
			slices.Equal(call.args, capabilityBrokerCall) ||
				slices.Equal(call.args, hostBrokerCall),
			"the guest must make exactly the two reviewed broker evidence queries: %#v", call.args)
	}
	imageRequireCall(t, topCalls, hostAvailabilityCall...)
	imageRequireFailingCall(t, topLevel, hostAvailabilityCall...)
	imageRequireDirectFailingCall(t, file, hostAvailabilityCall...)
	imageRequireCall(t, topCalls, "bootc", "status", "--json")
	expectedHostNormalizeCall := []string{
		"jq", "-e", "'" + imageExpectedHostJQProgram + "'",
		`"$work_dir/bootc-status.json"`,
	}
	imageRequireCall(t, topCalls, expectedHostNormalizeCall...)
	imageRequireFailingCall(t, topLevel, expectedHostNormalizeCall...)
	imageRequireDirectFailingCall(t, file, expectedHostNormalizeCall...)
	bootedImageShapeCall := []string{
		"jq", "-e", "'\n    .booted.image | type == \"string\" and length > 0\n'",
		`"$work_dir/expected-host-image.json"`,
	}
	imageRequireCall(t, topCalls, bootedImageShapeCall...)
	imageRequireFailingCall(t, topLevel, bootedImageShapeCall...)
	imageRequireDirectFailingCall(t, file, bootedImageShapeCall...)
	bootedDigestShapeCall := []string{
		"jq", "-e",
		"'\n    .booted.digest | type == \"string\" and\n        test(\"^sha256:[0-9a-f]{64}$\")\n'",
		`"$work_dir/expected-host-image.json"`,
	}
	imageRequireCall(t, topCalls, bootedDigestShapeCall...)
	imageRequireFailingCall(t, topLevel, bootedDigestShapeCall...)
	imageRequireDirectFailingCall(t, file, bootedDigestShapeCall...)
	actualHostNormalizeCall := []string{
		"jq", "-e", "'" + imageActualHostJQProgram + "'",
		`"$work_dir/host-image.json"`,
	}
	imageRequireCall(t, topCalls, actualHostNormalizeCall...)
	imageRequireFailingCall(t, topLevel, actualHostNormalizeCall...)
	imageRequireDirectFailingCall(t, file, actualHostNormalizeCall...)
	hostComparisonCall := []string{
		"cmp", "-s", `"$work_dir/expected-host-image.json"`, `"$work_dir/actual-host-image.json"`,
	}
	imageRequireCall(t, topCalls, hostComparisonCall...)
	imageRequireFailingCall(t, topLevel, hostComparisonCall...)
	imageRequireDirectFailingCall(t, file, hostComparisonCall...)
	capabilityComparisonCall := []string{
		"cmp", "-s", `"$work_dir/expected"`, `"$work_dir/actual"`,
	}
	imageRequireCall(t, topCalls, capabilityComparisonCall...)
	imageRequireFailingCall(t, topLevel, capabilityComparisonCall...)
	imageRequireDirectFailingCall(t, file, capabilityComparisonCall...)

	windowJournalCall := []string{
		"journalctl", "--no-pager", `--after-cursor="$journal_cursor"`, "-o", "cat",
	}
	imageRequireCall(t, topCalls, windowJournalCall...)
	imageRequireFailingCall(t, topLevel, windowJournalCall...)
	imageRequireDirectFailingCall(t, file, windowJournalCall...)
	windowAVCCall := []string{
		"jq", "-Rse", "'" + imageWindowAVCJQProgram + "'", `"$work_dir/new-avcs"`,
	}
	imageRequireCall(t, topCalls, windowAVCCall...)
	imageRequireFailingCall(t, topLevel, windowAVCCall...)
	imageRequireDirectFailingCall(t, file, windowAVCCall...)
	pilothouseAVCCall := []string{
		"jq", "-Rse", "'" + imagePilothouseAVCJQProgram + "'", `"$work_dir/boot-journal"`,
	}
	imageRequireCall(t, topCalls, pilothouseAVCCall...)
	imageRequireFailingCall(t, topLevel, pilothouseAVCCall...)
	imageRequireDirectFailingCall(t, file, pilothouseAVCCall...)
	require.Contains(t, imageReadHarness(t, imageVMGuestPath),
		"not a claim that the RPM provides a dedicated Pilothouse SELinux domain")
	bootJournalCall := []string{
		"journalctl", "--no-pager", "--boot", "-o", "cat",
	}
	imageRequireCall(t, topCalls, bootJournalCall...)
	imageRequireFailingCall(t, topLevel, bootJournalCall...)
	imageRequireDirectFailingCall(t, file, bootJournalCall...)

	stdoutWrite := func(command, op string) imageShellWriter {
		return imageShellWriter{command: command, fd: "1", op: op}
	}
	criticalWrites := map[string]map[imageShellWriter]int{
		`"$work_dir/actual-unsorted"`:          {stdoutWrite("jq", ">"): 1},
		`"$work_dir/actual"`:                   {stdoutWrite("sort", ">"): 1},
		`"$work_dir/expected"`:                 {stdoutWrite(":", ">"): 1, stdoutWrite("printf", ">>"): 7},
		`"$work_dir/cursor-journal"`:           {stdoutWrite("journalctl", ">"): 1},
		`"$work_dir/bootc-status.json"`:        {stdoutWrite("bootc", ">"): 1},
		`"$work_dir/expected-host-image.json"`: {stdoutWrite("jq", ">"): 1},
		`"$work_dir/actual-host-image.json"`:   {stdoutWrite("jq", ">"): 1},
		`"$work_dir/new-avcs"`:                 {stdoutWrite("journalctl", ">"): 1},
		`"$work_dir/boot-journal"`:             {stdoutWrite("journalctl", ">"): 1},
		`"$work_dir/query-body.json"`:          {},
		`"$work_dir/host-image.json"`:          {},
	}
	observedWrites := make(map[string]map[imageShellWriter]int, len(criticalWrites))
	writeLines := make(map[string][]uint, len(criticalWrites))
	for target := range criticalWrites {
		observedWrites[target] = map[imageShellWriter]int{}
	}
	writesByLine := map[uint][]imageShellWrite{}
	allowedOutputTargets := map[string]bool{
		"/dev/null":                            true,
		`"$work_dir/login.json"`:               true,
		`"$work_dir/auth.header"`:              true,
		`"$work_dir/query.json"`:               true,
		`"$work_dir/cursor-journal"`:           true,
		`"$work_dir/actual-unsorted"`:          true,
		`"$work_dir/actual"`:                   true,
		`"$work_dir/expected"`:                 true,
		`"$work_dir/bootc-status.json"`:        true,
		`"$work_dir/expected-host-image.json"`: true,
		`"$work_dir/actual-host-image.json"`:   true,
		`"$work_dir/new-avcs"`:                 true,
		`"$work_dir/boot-journal"`:             true,
	}
	for _, write := range imageShellOutputWrites(t, topLevel...) {
		writesByLine[write.line] = append(writesByLine[write.line], write)
		if !write.descriptorTarget {
			require.Truef(t, allowedOutputTargets[write.target],
				"guest output redirection must use one reviewed literal target; got %#v", write)
		}
		if _, critical := criticalWrites[write.target]; !critical {
			continue
		}
		observedWrites[write.target][imageShellWriter{
			command: write.command,
			fd:      write.fd,
			op:      write.op,
		}]++
		writeLines[write.target] = append(writeLines[write.target], write.line)
	}
	require.Equal(t, criticalWrites, observedWrites,
		"critical evidence files must have only their reviewed redirection writers")
	for target, lines := range writeLines {
		for _, line := range lines {
			require.Lenf(t, writesByLine[line], 1,
				"the statement writing %s must have no additional descriptor routing", target)
		}
	}

	reviewedJournalCalls := [][]string{
		cursorJournalCall,
		{"journalctl", "--no-pager", "--lines", "0"},
		windowJournalCall,
		bootJournalCall,
	}
	reviewedSystemctlCalls := [][]string{
		{"systemctl", "show-environment"},
		{"systemctl", "list-unit-files", "bootc-fetch-apply-updates.timer", "--no-legend"},
		{"systemctl", "list-unit-files", "bootc-fetch-apply-updates.service", "--no-legend"},
		{"systemctl", "list-unit-files", "rpm-ostreed-automatic.timer", "--no-legend"},
		{"systemctl", "list-unit-files", "rpm-ostreed-automatic.service", "--no-legend"},
	}
	for _, call := range topCalls {
		require.Falsef(t,
			imageCallIsForbiddenEvidenceMutator(call),
			"the guest evidence path must not gain a non-redirection file mutator: %#v",
			call.args)
		require.True(t, imageGuestCommandIsReviewed(call),
			"the guest main path must use only reviewed effective commands: %#v", call.args)
		command := imageCallEffectiveCommand(call)
		if filepath.Base(command) == "curl" {
			require.Equal(t, loginCurlCall, call.args,
				"the exact login request must be the only top-level curl writer")
		}
		if filepath.Base(command) == "sort" {
			require.True(t,
				slices.Equal(call.args, expectedCapabilitySortCall) ||
					slices.Equal(call.args, actualCapabilitySortCall),
				"the guest must use only the two reviewed capability sort calls: %#v", call.args)
			require.Equal(t, []string{"LC_ALL=C"}, call.assignments,
				"each reviewed capability sort must retain its exact locale assignment")
		}
		switch filepath.Base(command) {
		case "bootc":
			require.Equal(t, bootcStatusCall, call.args,
				"bootc may only provide the reviewed read-only status observation")
		case "journalctl":
			require.True(t, imageCallMatchesAny(call, reviewedJournalCalls...),
				"journalctl may only perform the four reviewed read-only observations: %#v",
				call.args)
		case "rpm-ostree":
			require.Equal(t, []string{"rpm-ostree", "status", "--json"}, call.args,
				"rpm-ostree may only provide the reviewed read-only status observation")
		case "sed":
			require.Equal(t, cursorDecodeCall, call.args,
				"sed may only decode the reviewed journal cursor")
		case "systemctl":
			require.True(t, imageCallMatchesAny(call, reviewedSystemctlCalls...),
				"systemctl may only perform the reviewed read-only capability observations: %#v",
				call.args)
		case "systemd-sysext":
			require.Equal(t, []string{"systemd-sysext", "list"}, call.args,
				"systemd-sysext may only provide the reviewed read-only list observation")
		case "trap":
			require.Equal(t, []string{"trap", "cleanup", "EXIT"}, call.args,
				"the guest must not replace its reviewed cleanup EXIT trap")
		}
	}
	var assignedCalls []imageShellCall
	for _, call := range topCalls {
		if len(call.assignments) > 0 {
			assignedCalls = append(assignedCalls, call)
		}
	}
	require.Len(t, assignedCalls, 4,
		"the guest main path must retain only the four reviewed prefix-assignment calls")
	for _, call := range assignedCalls {
		switch imageGuestReviewedTopLevelCommand(call) {
		case "sort":
			require.Equal(t, []string{"LC_ALL=C"}, call.assignments)
		case "jq":
			require.True(t,
				slices.Equal(call.assignments, []string{
					`PILOTHOUSE_IMAGE_TEST_USERNAME="$username"`,
					`PILOTHOUSE_IMAGE_TEST_PASSWORD="$password"`,
				}) ||
					slices.Equal(call.assignments, []string{
						`PILOTHOUSE_IMAGE_TEST_TOKEN="$token"`,
					}),
				"jq may retain only the reviewed credential-to-environment assignments: %#v",
				call.assignments)
		default:
			require.Failf(t, "unreviewed prefix assignment",
				"effective command %q has assignments %#v",
				imageGuestReviewedTopLevelCommand(call), call.assignments)
		}
	}

	cursorWriteLine := writeLines[`"$work_dir/cursor-journal"`][0]
	cursorDecodeLine := imageExactCallLine(t, topCalls, cursorDecodeCall...)
	cursorNonemptyLine := imageExactCallLine(t, topCalls, cursorNonemptyCall...)
	capabilityBrokerLine := imageExactCallLine(t, topCalls, capabilityBrokerCall...)
	hostBrokerLine := imageExactCallLine(t, topCalls, hostBrokerCall...)
	require.Less(t, cursorWriteLine, cursorDecodeLine)
	require.Less(t, cursorDecodeLine, cursorNonemptyLine)
	require.Less(t, cursorNonemptyLine, capabilityBrokerLine)
	require.Less(t, cursorNonemptyLine, hostBrokerLine)
	require.Less(t, capabilityBrokerLine, writeLines[`"$work_dir/new-avcs"`][0])
	require.Less(t, hostBrokerLine, writeLines[`"$work_dir/new-avcs"`][0])
	require.Less(t,
		capabilityBrokerLine,
		imageExactCallLine(t, topCalls, capabilityDecodeCall...),
	)
	capabilityCompareLine := imageExactCallLine(t, topCalls, capabilityComparisonCall...)
	for _, target := range []string{
		`"$work_dir/actual-unsorted"`, `"$work_dir/actual"`, `"$work_dir/expected"`,
	} {
		for _, writerLine := range writeLines[target] {
			require.Lessf(t, writerLine, capabilityCompareLine,
				"%s must be completely written before capability comparison", target)
		}
	}
	require.Less(t,
		hostBrokerLine,
		imageExactCallLine(t, topCalls, actualHostNormalizeCall...),
	)
	hostCompareLine := imageExactCallLine(t, topCalls, hostComparisonCall...)
	for _, target := range []string{
		`"$work_dir/bootc-status.json"`,
		`"$work_dir/expected-host-image.json"`,
		`"$work_dir/actual-host-image.json"`,
	} {
		for _, writerLine := range writeLines[target] {
			require.Lessf(t, writerLine, hostCompareLine,
				"%s must be completely written before host-image comparison", target)
		}
	}
	require.Less(t, writeLines[`"$work_dir/new-avcs"`][0],
		imageExactCallLine(t, topCalls, windowAVCCall...))
	require.Less(t, writeLines[`"$work_dir/boot-journal"`][0],
		imageExactCallLine(t, topCalls, pilothouseAVCCall...))

	for _, call := range allCalls {
		require.False(t, slices.Equal(call.args,
			[]string{"systemctl", "restart", "pilothoused.service", "pilothouse.service"}),
			"the image test must not duplicate #67 service activation")
	}

	for _, duplicate := range []string{
		"audit.db",
		"/run/pilothouse/.vm-boot-sentinel",
		"wrong password",
		"root login",
		"journal marker",
	} {
		for _, call := range allCalls {
			require.NotContainsf(t, strings.Join(call.args, " "), duplicate,
				"the image tier must not repeat the plain-VM assertion %q", duplicate)
		}
	}
}

func TestUCoreGuestCapabilityDecoderRejectsLineInjection(t *testing.T) {
	sandbox := newImageSandbox(t)
	input := filepath.Join(sandbox.cwd, "capabilities.json")
	jq := imageRequireTool(t, "jq")

	require.NoError(t, os.WriteFile(input, []byte(
		`{"result":{"capabilities":["bootc\njournald\nsystemd"]}}`+"\n",
	), 0o600))
	injected := imageRunChild(t, sandbox, jq, "-ser", imageCapabilityJQProgram, input)
	require.Error(t, injected.Err)
	require.False(t, injected.TimedOut)
	require.NotZero(t, injected.ExitCode)

	require.NoError(t, os.WriteFile(input, []byte(
		`{"result":{"capabilities":["bootc","journald","systemd","autoupdate-bootc"]}}`+"\n",
	), 0o600))
	valid := imageRunChild(t, sandbox, jq, "-ser", imageCapabilityJQProgram, input)
	require.NoError(t, valid.Err)
	require.False(t, valid.TimedOut)
	require.Equal(t, "bootc\njournald\nsystemd\nautoupdate-bootc\n", valid.Stdout)
}

func TestUCoreGuestAVCScannersFailOnForbiddenMatches(t *testing.T) {
	sandbox := newImageSandbox(t)
	input := filepath.Join(sandbox.cwd, "journal")
	jq := imageRequireTool(t, "jq")
	run := func(program, content string) imageChildResult {
		t.Helper()
		require.NoError(t, os.WriteFile(input, []byte(content), 0o600))
		return imageRunChild(t, sandbox, jq, "-Rse", program, input)
	}

	require.NoError(t, run(imageWindowAVCJQProgram, "ordinary journal entry\n").Err)
	require.Error(t, run(imageWindowAVCJQProgram,
		"type=AVC msg=audit: avc: denied { read } for comm=pilothoused\n").Err)

	require.NoError(t, run(imagePilothouseAVCJQProgram,
		"avc: denied { read } for comm=unrelated\n").Err)
	require.Error(t, run(imagePilothouseAVCJQProgram,
		"avc: denied { read } for path=/run/pilothouse/broker.sock\n").Err)
}

func TestImageShellASTGuardsRejectCommentsStringsAndWrongRegions(t *testing.T) {
	const fixture = `
target() {
    wanted-command target
    : # wanted-command target
    printf '%s' 'wanted-command target'
}

decoy() {
    wanted-command top-level
    wanted-command target
}

# wanted-command top-level
: # wanted-command top-level
printf '%s' 'wanted-command top-level'
printf '%s\n' dangerous-command argument
wanted-command top-level
`
	file := imageParseShellSource(t, "fixture.sh", fixture, syntax.LangBash)
	targetCalls := imageShellCalls(t, imageShellFunction(t, "fixture.sh", file, "target"))
	topCalls := imageShellCalls(t, imageShellTopLevel(file)...)

	require.Equal(t, 1, countImageShellCalls(targetCalls, "wanted-command", "target"),
		"a comment, no-op comment and printed copy must not satisfy an executable call guard")
	require.Equal(t, 1, countImageShellCalls(topCalls, "wanted-command", "top-level"),
		"a command moved into a decoy function must not satisfy a top-level main guard")
	require.Zero(t, countImageShellCalls(topCalls, "wanted-command", "target"),
		"a target-function command must not leak into the top-level region")
	require.Zero(t, countImageShellCalls(topCalls, "dangerous-command", "argument"),
		"a printed argv copy must not satisfy an exact-call guard")

	duplicate := imageParseShellSource(t, "duplicate.sh", `
target() { safe-command; }
target() { unsafe-command; }
`, syntax.LangBash)
	require.Len(t, imageShellFunctions(duplicate, "target"), 2,
		"duplicate function definitions must be visible to the exact-one guard")

	splitEvidence := imageParseShellSource(t, "split.sh", `
runner 45m run
runner image install
`, syntax.LangBash)
	require.Zero(t, countImageShellCalls(
		imageShellCalls(t, imageShellTopLevel(splitEvidence)...),
		"runner", "45m", "run", "image", "install",
	), "arguments split across calls must not satisfy one exact-call guard")

	nested := imageParseShellSource(t, "nested.sh", `
outer() {
    inner() { setsid unsafe-command; }
    inner
}
`, syntax.LangBash)
	require.Equal(t, 1, countImageShellCalls(imageShellAllCalls(t, nested), "setsid", "unsafe-command"),
		"whole-file negative policies must inspect nested function bodies")

	nonFailing := imageParseShellSource(t, "non-failing.sh", `
if [[ "$actual" == "$expected" ]]; then
    :
fi
`, syntax.LangBash)
	require.Zero(t, countImageFailingTests(
		t,
		imageShellTopLevel(nonFailing),
		`[[ "$actual" == "$expected" ]]`,
	), "a comparison that does not feed || fail must not satisfy a failing-test guard")

	discardedCall := imageParseShellSource(t, "discarded-call.sh", `
if critical-command evidence; then
    :
fi
`, syntax.LangBash)
	require.Zero(t, countImageFailingCalls(
		t,
		imageShellTopLevel(discardedCall),
		"critical-command", "evidence",
	), "a critical call in a non-failing branch must not satisfy a failing-call guard")

	maskedCall := imageParseShellSource(t, "masked-call.sh", `
critical-command evidence || true
`, syntax.LangBash)
	require.Zero(t, countImageFailingCalls(
		t,
		imageShellTopLevel(maskedCall),
		"critical-command", "evidence",
	), "a critical call whose error is discarded must not satisfy a failing-call guard")

	statusCapture := imageParseShellSource(t, "status-capture.sh", `
grep evidence input || evidence_status=$?
`, syntax.LangPOSIX)
	require.Equal(t, 1, countImageStatusCaptures(
		t,
		imageShellTopLevel(statusCapture),
		"evidence_status",
		"grep", "evidence", "input",
	), "the status-capture guard must recognize one exact command-to-$? assignment")

	resolutionMutation := imageParseShellSource(t, "resolution-mutation.sh", `
command -- eval 'set +e'
command command shopt -s expand_aliases
command command alias timeout=true
$'shopt' -s expand_aliases
a\lias trap=:
`, syntax.LangBash)
	for _, call := range imageShellCalls(t, imageShellTopLevel(resolutionMutation)...) {
		require.Truef(t, imageCallMutatesShellResolution(call) || !imageCallHasStaticCommand(call),
			"parsed shell-resolution mutation must be rejected: %#v", call.args)
	}
	quotedSet := imageParseShellSource(t, "quoted-set.sh", `"set" +e`, syntax.LangBash)
	quotedSetCalls := imageShellCalls(t, imageShellTopLevel(quotedSet)...)
	require.Len(t, quotedSetCalls, 1)
	quotedSetCommand, quotedSetStatic := imageCallStaticArgument(quotedSetCalls[0], 0)
	require.True(t, quotedSetStatic)
	require.Equal(t, "set", quotedSetCommand,
		"quoted builtin names must normalize before the exact error-mode check")
	dynamicCommand := imageParseShellSource(t, "dynamic-command.sh",
		`"$trap_name" 'exit 0' ERR`, syntax.LangBash)
	dynamicCalls := imageShellCalls(t, imageShellTopLevel(dynamicCommand)...)
	require.Len(t, dynamicCalls, 1)
	require.False(t, imageCallHasStaticCommand(dynamicCalls[0]),
		"dynamic command positions must be rejected before executable policy checks")
	wrappedEvidence := imageParseShellSource(t, "wrapped-evidence.sh", `
PATH=/tmp
PATH=/tmp stdbuf -o0 sort -o "$work_dir/actual" "$work_dir/expected"
chroot / sort -u "$work_dir/expected"
awk '{print}' "$work_dir/expected"
`, syntax.LangPOSIX)
	wrappedCalls := imageShellCalls(t, imageShellTopLevel(wrappedEvidence)...)
	require.Len(t, wrappedCalls, 3)
	require.Equal(t, []string{"PATH=/tmp"}, wrappedCalls[0].assignments,
		"prefix assignments must remain attached to the command they modify")
	require.Equal(t, [][]string{{"PATH=/tmp"}},
		imageShellAssignmentOnlyCalls(t, imageShellTopLevel(wrappedEvidence)...),
		"standalone assignments must remain visible outside the prefix-assignment call set")
	for _, call := range wrappedCalls {
		require.Falsef(t, imageGuestCommandIsReviewed(call),
			"execution wrapper or alternate output program must not enter the guest allowlist: %#v",
			call.args)
	}
	overwrite := imageParseShellSource(t, "evidence-overwrite.sh", `
sed -n p "$work_dir/expected" >"$work_dir/actual"
: >"$work_dir/new-avcs"
>"$work_dir/boot-journal"
{ :; } >"$work_dir/actual-host-image.json"
evidence_target="$work_dir/actual"
: >"$evidence_target"
journalctl 2>"$work_dir/new-avcs"
cat "$work_dir/expected" 0<>"$work_dir/actual"
cat "$work_dir/expected" >&"$work_dir/actual"
journalctl 3>&1 >"$work_dir/new-avcs" 1>&3
	`, syntax.LangPOSIX)
	require.ElementsMatch(t, []imageShellWrite{
		{command: "sed", fd: "1", op: ">", target: `"$work_dir/actual"`, line: 2},
		{command: ":", fd: "1", op: ">", target: `"$work_dir/new-avcs"`, line: 3},
		{command: "<compound>", fd: "1", op: ">", target: `"$work_dir/boot-journal"`, line: 4},
		{command: "<compound>", fd: "1", op: ">", target: `"$work_dir/actual-host-image.json"`, line: 5},
		{command: ":", fd: "1", op: ">", target: `"$evidence_target"`, line: 7},
		{command: "journalctl", fd: "2", op: ">", target: `"$work_dir/new-avcs"`, line: 8},
		{command: "cat", fd: "0", op: "<>", target: `"$work_dir/actual"`, line: 9},
		{command: "cat", fd: "1", op: ">&", target: `"$work_dir/actual"`, line: 10},
		{command: "journalctl", fd: "3", op: ">&", target: "1", descriptorTarget: true, line: 11},
		{command: "journalctl", fd: "1", op: ">", target: `"$work_dir/new-avcs"`, line: 11},
		{command: "journalctl", fd: "1", op: ">&", target: "3", descriptorTarget: true, line: 11},
	}, imageShellOutputWrites(t, imageShellTopLevel(overwrite)...),
		"write-capable redirections to evidence files must remain visible to the writer-set guard")
}

func TestImageShellNegativePoliciesCoverAlternateArgv(t *testing.T) {
	for _, call := range []imageShellCall{
		{args: []string{"podman_fixture", "10m", "push", "localhost/test"}},
		{args: []string{"podman", "image", "push", "localhost/test"}},
		{args: []string{"timeout", "30s", "skopeo", "copy", "containers-storage:a", "dir:b"}},
		{args: []string{"timeout", "30s", "podman_fixture", "10m", "push", "localhost/test"}},
	} {
		require.Truef(t, imageCallPublishes(call), "must identify publishing argv %#v", call.args)
	}
	for _, call := range []imageShellCall{
		{args: []string{"rm", "-r", "/tmp/work"}},
		{args: []string{"rm", "-fr", "/tmp/work"}},
		{args: []string{"command", "rm", "--recursive", "--force", "/tmp/work"}},
		{args: []string{"timeout", "30s", "rm", "-r", "-f", "/tmp/work"}},
	} {
		require.Truef(t, imageCallRecursivelyRemoves(call), "must identify recursive removal argv %#v", call.args)
	}
	require.True(t, imageCallUsesRegistry(imageShellCall{
		args: []string{"guest_run_long", "bootc", "switch", "docker://registry.example/os:test"},
	}))
	require.True(t, imageCallUsesRegistry(imageShellCall{
		args: []string{"podman_fixture", "2m", "run", "-d", "registry:2"},
	}))
	require.True(t, imageCallRunsFixture(imageShellCall{
		args: []string{"timeout", "30s", "podman_fixture", "2m", "run", "registry:2"},
	}))
	require.True(t, imageCallInvokesHostProgram(imageShellCall{
		args: []string{"timeout", "30s", "podman", "run", "-d", "alpine"},
	}, "podman"))
	require.True(t, imageCallInvokesHostProgram(imageShellCall{
		args: []string{"command", "podman", "run", "-d", "alpine"},
	}, "podman"))
	require.True(t, imageCallInvokesHostProgram(imageShellCall{
		args: []string{"/usr/bin/podman", "run", "--rm", "docker.io/library/alpine", "true"},
	}, "podman"))
	require.False(t, imageCallInvokesHostProgram(imageShellCall{
		args: []string{"guest_run_long", "podman", "load", "--input", "/tmp/update.oci"},
	}, "podman"))
	require.True(t, imageCallContainsProgram(imageShellCall{
		args: []string{`"/usr/bin/qemu-system-x86_64"`, "-S"},
	}, "qemu-system-x86_64"))
	require.True(t, imageCallIsForbiddenEvidenceMutator(imageShellCall{
		args: []string{"cp", `"$work_dir/expected"`, `"$work_dir/actual"`},
	}))
	require.True(t, imageCallIsForbiddenEvidenceMutator(imageShellCall{
		args: []string{"printf", "-v", "expected_slot", "%s", "baseline"},
	}))
	require.Equal(t, "exit", imageCallEffectiveCommand(imageShellCall{
		args: []string{"command", "--", "command", "builtin", "exit", "0"},
	}))
	for _, call := range []imageShellCall{
		{args: []string{"alias", "timeout=true"}},
		{args: []string{"shopt", "-s", "expand_aliases"}},
		{args: []string{"command", "eval", "'set +e'"}},
		{args: []string{"command", "--", "eval", "'set +e'"}},
		{args: []string{"command", "command", "shopt", "-s", "expand_aliases"}},
		{args: []string{"command", "command", "alias", "trap=:"}},
		{args: []string{"builtin", "source", "/tmp/mutate-shell"}},
	} {
		require.Truef(t, imageCallMutatesShellResolution(call),
			"must reject shell-resolution mutation %#v", call.args)
	}
}

func TestUCoreVMRunnerRejectsRelativeWorkspaceBeforeMutation(t *testing.T) {
	sandbox := newImageSandbox(t)
	result := imageRunChild(t, sandbox, imageRequireTool(t, "bash"),
		filepath.Join(imageRepositoryRoot(t), imageVMRunnerPath),
		"--workspace", "relative",
	)

	require.Error(t, result.Err)
	require.False(t, result.TimedOut)
	require.Contains(t, result.Stderr, "--workspace must name an existing absolute directory")
	entries, err := os.ReadDir(sandbox.cwd)
	require.NoError(t, err)
	require.Empty(t, entries)
}
