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

	for _, stmt := range file.Stmts {
		decl, ok := stmt.Cmd.(*syntax.FuncDecl)
		if ok && decl.Name != nil && decl.Name.Value == name {
			return decl.Body
		}
	}
	require.Failf(t, "missing shell function", "%s must define %s()", path, name)
	return nil
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

func imageShellAllExecutableRoots(file *syntax.File) []syntax.Node {
	roots := imageShellTopLevel(file)
	for _, stmt := range file.Stmts {
		if decl, ok := stmt.Cmd.(*syntax.FuncDecl); ok {
			roots = append(roots, decl.Body)
		}
	}
	return roots
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
	var calls []imageShellCall
	for _, root := range roots {
		syntax.Walk(root, func(node syntax.Node) bool {
			if node == nil {
				return true
			}
			if _, nestedFunction := node.(*syntax.FuncDecl); nestedFunction {
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

func imageShellTests(t *testing.T, roots ...syntax.Node) []string {
	t.Helper()
	var tests []string
	for _, root := range roots {
		syntax.Walk(root, func(node syntax.Node) bool {
			if node == nil {
				return true
			}
			if _, nestedFunction := node.(*syntax.FuncDecl); nestedFunction {
				return false
			}
			if clause, ok := node.(*syntax.TestClause); ok {
				tests = append(tests, imageShellRender(t, clause))
			}
			return true
		})
	}
	return tests
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

func imageRequireCallSubsequence(t *testing.T, calls []imageShellCall, want ...string) {
	t.Helper()
	for _, call := range calls {
		for start := 0; start+len(want) <= len(call.args); start++ {
			if slices.Equal(call.args[start:start+len(want)], want) {
				return
			}
		}
	}
	require.Failf(t, "missing executable shell argument sequence", "want args %#v; parsed calls: %#v", want, calls)
}

func imageRequireCommandArguments(t *testing.T, calls []imageShellCall, command string, want ...string) {
	t.Helper()
	for _, call := range calls {
		if len(call.args) == 0 || call.args[0] != command {
			continue
		}
		missing := false
		for _, argument := range want {
			if !slices.Contains(call.args[1:], argument) {
				missing = true
				break
			}
		}
		if !missing {
			return
		}
	}
	require.Failf(t, "missing executable shell command arguments",
		"want %s args %#v; parsed calls: %#v", command, want, calls)
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
	topTests := imageShellTests(t, topLevel...)

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
	var manifestCheck imageShellCall
	for _, call := range topCalls {
		if len(call.args) > 0 && call.args[0] == "jq" && slices.Contains(call.args, `"$IMAGE_MANIFEST"`) {
			manifestCheck = call
			break
		}
	}
	require.NotEmpty(t, manifestCheck.args, "the top-level runner must execute jq against the fixture manifest")
	manifestExpression := strings.Join(manifestCheck.args, "\n")
	require.Contains(t, manifestExpression, `.kind == "pilothouse-ucore-image-fixture"`)
	require.Contains(t, manifestExpression, `.producer_uid == 0`)
	require.Contains(t, manifestExpression, `.source == "ghcr.io/ublue-os/ucore:latest"`)
	require.Contains(t, topTests, `[[ "$actual_id" == "$expected_id" ]]`,
		"both local refs must be rechecked against their manifested image IDs")
	require.Contains(t, topTests, `[[ "${baseline_ref%:*}" == "${update_ref%:*}" ]]`,
		"the two slots must belong to one private fixture prefix")

	podmanNode := imageShellFunction(t, imageVMRunnerPath, file, "podman_fixture")
	podmanCalls := imageShellCalls(t, podmanNode)
	imageRequireCallSubsequence(t, podmanCalls,
		"timeout", "--signal=TERM", "--kill-after=30s", `"$1"`,
		"podman", `"${podman_args[@]}"`, `"${@:2}"`,
	)
	podmanAssignments := imageShellAssignments(t, topLevel...)
	var podmanArgs string
	for _, assignment := range podmanAssignments {
		if assignment.name == "podman_args" {
			podmanArgs = assignment.value
		}
	}
	require.NotEmpty(t, podmanArgs)
	for _, option := range []string{
		"--remote=false",
		`--root "$storage_root"`,
		`--imagestore "$image_store"`,
		`--runroot "$run_root"`,
		`--tmpdir "$podman_tmpdir"`,
		"--events-backend none",
		"--storage-driver overlay",
	} {
		require.Contains(t, podmanArgs, option)
	}

	for _, call := range imageShellCalls(t, imageShellAllExecutableRoots(file)...) {
		require.False(t,
			len(call.args) >= 2 &&
				(call.args[0] == "podman" || call.args[0] == "docker" || call.args[0] == "skopeo") &&
				(call.args[1] == "push" || call.args[1] == "copy"),
			"the fixture consumer must never publish or copy an image: %#v", call.args)
		require.False(t, len(call.args) >= 2 && call.args[0] == "rm" && call.args[1] == "-rf",
			"the fixture consumer must leave recursive workspace cleanup to the exact-store owner")
	}
}

func TestUCoreVMRunnerUsesComposefsAndLocalUpdateTransport(t *testing.T) {
	file := imageParseShell(t, imageVMRunnerPath, syntax.LangBash)
	topLevel := imageShellTopLevel(file)
	mainCalls := imageShellCalls(t, topLevel...)
	mainTests := imageShellTests(t, topLevel...)

	imageRequireCallSubsequence(t, mainCalls,
		"podman_fixture", "45m", "run",
	)
	imageRequireCallSubsequence(t, mainCalls,
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
	)
	imageRequireCall(t, mainCalls,
		"podman_fixture", "20m", "save", "--format", "oci-archive",
		"--output", `"$UPDATE_ARCHIVE"`, `"$update_ref"`,
	)
	imageRequireCall(t, mainCalls,
		"guest_run_long", "podman", "load", "--input", "/var/tmp/pilothouse-image-update.oci",
	)
	imageRequireCall(t, mainCalls,
		"guest_run_long", "bootc", "switch", "--quiet",
		"--transport", "containers-storage", `"$update_ref"`,
	)
	for _, call := range mainCalls {
		require.NotContains(t, call.args, "registry:2")
		require.False(t,
			len(call.args) > 2 && call.args[0] == "guest_run_long" &&
				call.args[1] == "bootc" && call.args[2] == "switch" &&
				slices.Contains(call.args, "docker://"),
			"the update must not use a registry transport: %#v", call.args)
	}

	for _, continuity := range []string{
		`[[ "$(guest_status_digest booted)" == "$staged_digest" ]]`,
		`[[ "$(guest_status_digest rollback)" == "$baseline_booted" ]]`,
		`[[ "$(guest_status_digest booted)" == "$pre_rollback_target" ]]`,
		`[[ "$(guest_status_digest rollback)" == "$pre_rollback_booted" ]]`,
	} {
		require.Contains(t, mainTests, continuity)
	}
	imageRequireCall(t, mainCalls, "run_guest_validation", "update")
	require.Equal(t, 2, countImageShellCalls(mainCalls, "run_guest_validation", "baseline"),
		"the baseline must be checked both before update and after rollback")
}

func TestUCoreVMRunnerOwnsAndWaitsForEveryLiveResource(t *testing.T) {
	file := imageParseShell(t, imageVMRunnerPath, syntax.LangBash)
	allCalls := imageShellCalls(t, imageShellAllExecutableRoots(file)...)

	for _, escape := range []string{"setsid", "nohup", "disown", "daemonize"} {
		for _, call := range allCalls {
			require.NotEqualf(t, escape, call.args[0],
				"%s must not let a helper escape the teardown owner", imageVMRunnerPath)
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
	imageRequireCallSubsequence(t, detachCalls, "losetup", "--detach", `"$loop"`)
	imageRequireCallSubsequence(t, detachCalls, "losetup", "-j", `"$DISK_IMAGE"`)

	mainCalls := imageShellCalls(t, imageShellTopLevel(file)...)
	imageRequireCallSubsequence(t, mainCalls,
		"--volume", `"$SSH_KEY.pub:/run/pilothouse-image-test-key.pub:ro"`,
	)
	for _, call := range allCalls {
		for _, arg := range call.args {
			require.NotContains(t, arg, "${loop_device}p3")
			require.NotContains(t, arg, "authorized_keys",
				"the runner must not write an authorized_keys path into a guessed disk layout")
		}
	}
	imageRequireCommandArguments(t, mainCalls, "qemu-system-x86_64", "-serial", "stdio")
	for _, call := range mainCalls {
		require.NotContains(t, call.args, "console.log")
		require.NotContains(t, call.args, "qemu-stderr.log")
	}
}

func TestUCoreGuestChecksOnlyImageHostDeltas(t *testing.T) {
	file := imageParseShell(t, imageVMGuestPath, syntax.LangPOSIX)
	topLevel := imageShellTopLevel(file)
	topCalls := imageShellCalls(t, topLevel...)
	allCalls := imageShellCalls(t, imageShellAllExecutableRoots(file)...)

	imageRequireCall(t, topCalls,
		"[", `"$(cat /usr/lib/pilothouse-image-test/slot)"`, "=", `"$expected_slot"`, "]",
	)
	imageRequireCall(t, topCalls, "[", `"$(getenforce)"`, "=", "Enforcing", "]")
	imageRequireCall(t, topCalls, "bootc", "status", "--json")
	imageRequireCall(t, topCalls, "grep", "-qx", "bootc", `"$work_dir/actual"`)

	for _, expectedProbe := range [][]string{
		{"systemctl", "show-environment"},
		{"journalctl", "--no-pager", "--lines", "0"},
		{"systemd-sysext", "list"},
		{"bootc", "status", "--json"},
		{"rpm-ostree", "status", "--json"},
		{"systemctl", "list-unit-files", "bootc-fetch-apply-updates.timer"},
		{"systemctl", "list-unit-files", "bootc-fetch-apply-updates.service"},
		{"systemctl", "list-unit-files", "rpm-ostreed-automatic.timer"},
		{"systemctl", "list-unit-files", "rpm-ostreed-automatic.service"},
	} {
		imageRequireCallSubsequence(t, topCalls, expectedProbe...)
	}
	for _, optedOut := range []string{"updex", "podman", "docker", "incus"} {
		require.Zero(t, countImageShellCalls(topCalls, "printf", "%s\\n", optedOut),
			"unconfigured optional dependencies must not enter the expected advertised set")
	}

	var availabilityCheck imageShellCall
	for _, call := range topCalls {
		if len(call.args) > 0 && call.args[0] == "jq" &&
			slices.Contains(call.args, `"$work_dir/host-image.json"`) {
			availabilityCheck = call
			break
		}
	}
	require.NotEmpty(t, availabilityCheck.args)
	require.Contains(t, strings.Join(availabilityCheck.args, "\n"), ".result.bootc_available == true")
	imageRequireCall(t, topCalls, "bootc", "status", "--json")
	imageRequireCall(t, topCalls,
		"cmp", "-s", `"$work_dir/expected-host-image.json"`, `"$work_dir/actual-host-image.json"`,
	)

	imageRequireCallSubsequence(t, topCalls,
		"journalctl", "--no-pager", `--after-cursor="$journal_cursor"`, "-o", "cat",
	)
	imageRequireCallSubsequence(t, topCalls, "grep", "-Ei", "'avc:[[:space:]]+denied'")
	imageRequireCallSubsequence(t, topCalls,
		"grep", "-Ei", "'pilothouse|pilothoused|/run/pilothouse|/var/lib/pilothouse'",
	)
	require.Contains(t, imageReadHarness(t, imageVMGuestPath),
		"not a claim that the RPM provides a dedicated Pilothouse SELinux domain")
	imageRequireCallSubsequence(t, topCalls,
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
