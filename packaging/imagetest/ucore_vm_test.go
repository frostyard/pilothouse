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
	args []string
	line uint
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

func imageShellRender(t *testing.T, node syntax.Node) string {
	t.Helper()
	var output bytes.Buffer
	require.NoError(t, syntax.NewPrinter(syntax.Minify(true)).Print(&output, node))
	return output.String()
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
			for _, word := range call.Args {
				args = append(args, imageShellRender(t, word))
			}
			calls = append(calls, imageShellCall{args: args, line: call.Pos().Line()})
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

func countImageShellCalls(calls []imageShellCall, want ...string) int {
	count := 0
	for _, call := range calls {
		if slices.Equal(call.args, want) {
			count++
		}
	}
	return count
}

func imageCallContainsArgument(call imageShellCall, want string) bool {
	return slices.Contains(call.args, want)
}

func imageCallContainsProgram(call imageShellCall, want string) bool {
	for _, argument := range call.args {
		if argument == want || filepath.Base(argument) == want {
			return true
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
		if imageCallContainsArgument(call, "set") {
			require.Equal(t, want, call.args,
				"the reviewed shell error mode must be the only invocation of set")
		}
		for _, dynamic := range []string{"eval", "source", "."} {
			invoked := len(call.args) > 0 && call.args[0] == dynamic
			if len(call.args) > 1 && slices.Contains([]string{"command", "builtin"}, call.args[0]) {
				invoked = invoked || call.args[1] == dynamic
			}
			require.Falsef(t, invoked,
				"dynamic shell evaluation could replace the reviewed error mode or safety functions: %#v",
				call.args)
		}
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

	imageRequireDeclaration(t, topDeclarations, imageShellDeclaration{
		variant: "readonly", name: "IMAGE_DIR", value: `"$workspace/fixture-ucore-images"`,
	})
	imageRequireDeclaration(t, topDeclarations, imageShellDeclaration{
		variant: "readonly", name: "IMAGE_MANIFEST", value: `"$IMAGE_DIR/fixture.json"`,
	})
	for _, path := range [][]string{
		{"assert_storage_path", `"$storage_root"`, `"$IMAGE_DIR/storage"`},
		{"assert_storage_path", `"$image_store"`, `"$IMAGE_DIR/imagestore"`},
		{"assert_storage_path", `"$run_root"`, `"$IMAGE_DIR/runroot"`},
		{"assert_storage_path", `"$podman_tmpdir"`, `"$IMAGE_DIR/libpod-tmp"`},
		{"assert_storage_path", `"$image_tmpdir"`, `"$IMAGE_DIR/image-tmp"`},
		{"assert_storage_path", `"$storage_config"`, `"$IMAGE_DIR/storage.conf"`},
	} {
		imageRequireCall(t, topCalls, path...)
	}
	manifestTrustCall := []string{
		"jq", "-e",
		"'\n    .schema == 1 and\n    .kind == \"pilothouse-ucore-image-fixture\" and\n    .producer_uid == 0 and\n    .source == \"ghcr.io/ublue-os/ucore:latest\" and\n    .baseline.slot == \"baseline\" and\n    .update.slot == \"update\"\n'",
		`"$IMAGE_MANIFEST"`,
	}
	imageRequireCall(t, topCalls, manifestTrustCall...)
	imageRequireFailingCall(t, topLevel, manifestTrustCall...)
	imageRequireFailingTest(t, topLevel, `[[ $EUID -eq 0 ]]`)
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
	imageRequireCall(t, mainCalls,
		"podman_fixture", "20m", "save", "--format", "oci-archive",
		"--output", `"$UPDATE_ARCHIVE"`, `"$update_ref"`,
	)
	imageRequireCall(t, mainCalls,
		"guest_run_long", "podman", "load", "--input", "/var/tmp/pilothouse-image-update.oci",
	)
	switchCall := []string{
		"guest_run_long", "bootc", "switch", "--quiet",
		"--transport", "containers-storage", `"$update_ref"`,
	}
	imageRequireExactCallCount(t, allCalls, 1, switchCall...)
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
	}
	imageRequireCall(t, mainCalls, "run_guest_validation", "update")
	require.Equal(t, 2, countImageShellCalls(mainCalls, "run_guest_validation", "baseline"),
		"the baseline must be checked both before update and after rollback")
}

func TestUCoreVMRunnerOwnsAndWaitsForEveryLiveResource(t *testing.T) {
	file := imageParseShell(t, imageVMRunnerPath, syntax.LangBash)
	allCalls := imageShellAllCalls(t, file)
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
qemu_pid=""
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
	require.Contains(t,
		imageShellAssignments(t, imageShellTopLevel(file)...),
		imageShellAssignment{name: "qemu_pid", value: "$!"},
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

	mainCalls := imageShellCalls(t, imageShellTopLevel(file)...)
	imageRequireFailingCall(t, imageShellTopLevel(file), "cleanup")
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
	for index, statement := range file.Stmts {
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
	require.Less(t, qemuStatement+1, len(file.Stmts),
		"QEMU must be followed immediately by qemu_pid=$!")
	pidStatement := file.Stmts[qemuStatement+1]
	require.False(t, pidStatement.Background)
	pidCapture, ok := pidStatement.Cmd.(*syntax.CallExpr)
	require.True(t, ok, "QEMU must be followed immediately by an assignment statement")
	require.Empty(t, pidCapture.Args)
	require.Len(t, pidCapture.Assigns, 1)
	require.Equal(t, "qemu_pid", pidCapture.Assigns[0].Name.Value)
	require.Equal(t, "$!", imageShellRender(t, pidCapture.Assigns[0].Value))
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
	imageRequireExactCallCount(t, allCalls, 1, "exit", "0")
	for _, call := range topCalls {
		if len(call.args) > 0 && call.args[0] == "exit" {
			require.Equal(t, []string{"exit", "0"}, call.args,
				"the reviewed prepare branch must be the guest harness's only successful early exit")
		}
		require.NotContains(t, []string{"exec", "return"}, call.args[0],
			"the guest main path must not gain an alternate early termination command")
	}

	slotMarkerCall := []string{
		"[", `"$(cat /usr/lib/pilothouse-image-test/slot)"`, "=", `"$expected_slot"`, "]",
	}
	imageRequireCall(t, topCalls, slotMarkerCall...)
	imageRequireFailingCall(t, topLevel, slotMarkerCall...)
	enforcingCall := []string{"[", `"$(getenforce)"`, "=", "Enforcing", "]"}
	imageRequireCall(t, topCalls, enforcingCall...)
	imageRequireFailingCall(t, topLevel, enforcingCall...)
	bootcStatusCall := []string{"bootc", "status", "--json"}
	imageRequireCall(t, topCalls, bootcStatusCall...)
	require.Equal(t, 2, countImageFailingCalls(t, topLevel, bootcStatusCall...),
		"both the initial bootc probe and captured host-image status must fail closed")
	imageRequireCall(t, topCalls, "grep", "-qx", "bootc", `"$work_dir/actual"`)
	capabilityDecodeCall := []string{
		"jq", "-ser",
		"'" + imageCapabilityJQProgram + "'",
		`"$work_dir/query-body.json"`,
	}
	imageRequireCall(t, topCalls, capabilityDecodeCall...)
	imageRequireFailingCall(t, topLevel, capabilityDecodeCall...)

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
	imageRequireCall(t, topCalls, hostAvailabilityCall...)
	imageRequireFailingCall(t, topLevel, hostAvailabilityCall...)
	imageRequireCall(t, topCalls, "bootc", "status", "--json")
	hostComparisonCall := []string{
		"cmp", "-s", `"$work_dir/expected-host-image.json"`, `"$work_dir/actual-host-image.json"`,
	}
	imageRequireCall(t, topCalls, hostComparisonCall...)
	imageRequireFailingCall(t, topLevel, hostComparisonCall...)
	capabilityComparisonCall := []string{
		"cmp", "-s", `"$work_dir/expected"`, `"$work_dir/actual"`,
	}
	imageRequireCall(t, topCalls, capabilityComparisonCall...)
	imageRequireFailingCall(t, topLevel, capabilityComparisonCall...)

	windowJournalCall := []string{
		"journalctl", "--no-pager", `--after-cursor="$journal_cursor"`, "-o", "cat",
	}
	imageRequireCall(t, topCalls, windowJournalCall...)
	imageRequireFailingCall(t, topLevel, windowJournalCall...)
	windowAVCCall := []string{
		"grep", "-Ei", "'avc:[[:space:]]+denied'", `"$work_dir/new-avcs"`,
	}
	imageRequireCall(t, topCalls, windowAVCCall...)
	require.Equal(t, 1, countImageStatusCaptures(t, topLevel, "window_avc_status", windowAVCCall...),
		"controlled-window grep status must be captured without masking grep errors")
	imageRequireFailingCall(t, topLevel,
		"[", `"$window_avc_status"`, "-le", "1", "]")
	imageRequireFailingCall(t, topLevel,
		"[", `"$window_avc_status"`, "-ne", "0", "]")
	currentBootAVCCall := []string{
		"grep", "-Ei", "'avc:[[:space:]]+denied'", `"$work_dir/boot-journal"`,
	}
	imageRequireCall(t, topCalls, currentBootAVCCall...)
	require.Equal(t, 1, countImageStatusCaptures(t, topLevel, "avc_status", currentBootAVCCall...),
		"current-boot AVC grep status must be captured without masking grep errors")
	imageRequireFailingCall(t, topLevel, "[", `"$avc_status"`, "-le", "1", "]")
	pilothouseAVCCall := []string{
		"grep", "-Ei", "'pilothouse|pilothoused|/run/pilothouse|/var/lib/pilothouse'",
		`"$work_dir/all-avcs"`,
	}
	imageRequireCall(t, topCalls, pilothouseAVCCall...)
	require.Equal(t, 1, countImageStatusCaptures(t, topLevel, "pilothouse_avc_status", pilothouseAVCCall...),
		"Pilothouse AVC grep status must be captured without masking grep errors")
	imageRequireFailingCall(t, topLevel,
		"[", `"$pilothouse_avc_status"`, "-le", "1", "]")
	imageRequireFailingCall(t, topLevel,
		"[", `"$pilothouse_avc_status"`, "-ne", "0", "]")
	require.Contains(t, imageReadHarness(t, imageVMGuestPath),
		"not a claim that the RPM provides a dedicated Pilothouse SELinux domain")
	bootJournalCall := []string{
		"journalctl", "--no-pager", "--boot", "-o", "cat",
	}
	imageRequireCall(t, topCalls, bootJournalCall...)
	imageRequireFailingCall(t, topLevel, bootJournalCall...)
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
		`{"result":{"capabilities":["bootc`+"\n"+`journald`+"\n"+`systemd"]}}`+"\n",
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
		args: []string{"/usr/bin/qemu-system-x86_64", "-S"},
	}, "qemu-system-x86_64"))
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
