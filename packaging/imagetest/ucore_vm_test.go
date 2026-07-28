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
	imageVMRunnerPath = "test/image/ucore-vm-test.sh"
	imageVMGuestPath  = "test/image/guest/validate-ucore.sh"
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

func countImageShellCalls(calls []imageShellCall, want ...string) int {
	count := 0
	for _, call := range calls {
		if slices.Equal(call.args, want) {
			count++
		}
	}
	return count
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
		imageRequireUniqueFunctions(t, path, imageParseShell(t, path, language))

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
	imageRequireCall(t, topCalls,
		"jq", "-e",
		"'\n    .schema == 1 and\n    .kind == \"pilothouse-ucore-image-fixture\" and\n    .producer_uid == 0 and\n    .source == \"ghcr.io/ublue-os/ucore:latest\" and\n    .baseline.slot == \"baseline\" and\n    .update.slot == \"update\"\n'",
		`"$IMAGE_MANIFEST"`,
	)
	imageRequireFailingTest(t, topLevel, `[[ "$actual_id" == "$expected_id" ]]`)
	imageRequireFailingTest(t, topLevel, `[[ "${baseline_ref%:*}" == "${update_ref%:*}" ]]`)

	podmanNode := imageShellFunction(t, imageVMRunnerPath, file, "podman_fixture")
	podmanCalls := imageShellCalls(t, podmanNode)
	imageRequireCall(t, podmanCalls,
		"timeout", "--signal=TERM", "--kill-after=30s", `"$1"`,
		"podman", `"${podman_args[@]}"`, `"${@:2}"`,
	)
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
	}
}

func TestUCoreVMRunnerUsesComposefsAndLocalUpdateTransport(t *testing.T) {
	file := imageParseShell(t, imageVMRunnerPath, syntax.LangBash)
	topLevel := imageShellTopLevel(file)
	mainCalls := imageShellCalls(t, topLevel...)

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
	imageRequireCall(t, mainCalls, installCall...)
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
	imageRequireCall(t, mainCalls, switchCall...)
	for _, call := range imageShellAllCalls(t, file) {
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

	for _, escape := range []string{"setsid", "nohup", "disown", "daemonize"} {
		for _, call := range allCalls {
			require.NotContainsf(t, call.args, escape,
				"%s must not let a helper escape the teardown owner; call: %#v",
				imageVMRunnerPath, call.args)
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
	for _, call := range allCalls {
		for _, arg := range call.args {
			require.NotContains(t, arg, "${loop_device}p3")
			require.NotContains(t, arg, "authorized_keys",
				"the runner must not write an authorized_keys path into a guessed disk layout")
		}
	}
	imageRequireCall(t, mainCalls,
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
	)
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

	imageRequireCall(t, topCalls,
		"[", `"$(cat /usr/lib/pilothouse-image-test/slot)"`, "=", `"$expected_slot"`, "]",
	)
	imageRequireCall(t, topCalls, "[", `"$(getenforce)"`, "=", "Enforcing", "]")
	imageRequireCall(t, topCalls, "bootc", "status", "--json")
	imageRequireCall(t, topCalls, "grep", "-qx", "bootc", `"$work_dir/actual"`)
	imageRequireCall(t, topCalls,
		"jq", "-ser",
		"'\n    if length == 1 and\n       (.[0].result.capabilities | type == \"array\" and all(.[]; type == \"string\"))\n    then .[0].result.capabilities[]\n    else error(\"want exactly one response whose capabilities are an array of strings\")\n    end\n'",
		`"$work_dir/query-body.json"`,
	)

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

	imageRequireCall(t, topCalls,
		"jq", "-e", "'\n    .result.bootc_available == true\n'",
		`"$work_dir/host-image.json"`,
	)
	imageRequireCall(t, topCalls, "bootc", "status", "--json")
	imageRequireCall(t, topCalls,
		"cmp", "-s", `"$work_dir/expected-host-image.json"`, `"$work_dir/actual-host-image.json"`,
	)

	imageRequireCall(t, topCalls,
		"journalctl", "--no-pager", `--after-cursor="$journal_cursor"`, "-o", "cat",
	)
	imageRequireCall(t, topCalls, "grep", "-Ei", "'avc:[[:space:]]+denied'", `"$work_dir/new-avcs"`)
	imageRequireCall(t, topCalls,
		"grep", "-Ei", "'pilothouse|pilothoused|/run/pilothouse|/var/lib/pilothouse'",
		`"$work_dir/all-avcs"`,
	)
	require.Contains(t, imageReadHarness(t, imageVMGuestPath),
		"not a claim that the RPM provides a dedicated Pilothouse SELinux domain")
	imageRequireCall(t, topCalls,
		"journalctl", "--no-pager", "--boot", "-o", "cat",
	)
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
