package packaging

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

const (
	debUnitPath = "deb/pilothoused.service"
	rpmUnitPath = "rpm/pilothoused.service"

	// packagedUnitDir is where both nfpm overrides install the broker unit.
	packagedUnitDir = "usr/lib/systemd/system"
)

// readUnitLines returns the lines of a packaged unit file. A trailing newline
// survives as an empty final element, so a missing or extra trailing newline
// still registers as a difference.
func readUnitLines(t *testing.T, path string) []string {
	t.Helper()

	raw, err := os.ReadFile(path)
	require.NoErrorf(t, err, "read %s", path)

	return strings.Split(string(raw), "\n")
}

// singleLineDifference reports the indexes of every line at which deb and rpm
// differ. A length mismatch is an error rather than a difference count, so a
// wholesale rewrite of one variant can never be mistaken for a one-line diff.
func singleLineDifference(deb, rpm []string) ([]int, error) {
	if len(deb) != len(rpm) {
		return nil, fmt.Errorf("unit files have different line counts: deb has %d, rpm has %d", len(deb), len(rpm))
	}

	var differing []int

	for i := range deb {
		if deb[i] != rpm[i] {
			differing = append(differing, i)
		}
	}

	return differing, nil
}

// TestBrokerUnitsDifferOnlyInAdminGroup enforces D1's binding requirement that
// the two packaged pilothoused.service variants are identical except for the
// --admin-group argument: sudo on Debian-family hosts, wheel on Fedora-family
// hosts. This is a distinct check from unit validity below; both must hold.
func TestBrokerUnitsDifferOnlyInAdminGroup(t *testing.T) {
	deb := readUnitLines(t, debUnitPath)
	rpm := readUnitLines(t, rpmUnitPath)

	differing, err := singleLineDifference(deb, rpm)
	require.NoError(t, err)
	require.Lenf(t, differing, 1,
		"%s and %s must differ in exactly one line, got differences at lines %v",
		debUnitPath, rpmUnitPath, differing)

	i := differing[0]
	require.Containsf(t, deb[i], "--admin-group sudo",
		"the single differing line in %s must set the Debian-family admin group", debUnitPath)
	require.Containsf(t, rpm[i], "--admin-group wheel",
		"the single differing line in %s must set the Fedora-family admin group", rpmUnitPath)

	// The differing line must differ *only* in the group token, so no other
	// ExecStart argument can drift between the two variants unnoticed.
	require.Equal(t,
		strings.Replace(deb[i], "--admin-group sudo", "--admin-group", 1),
		strings.Replace(rpm[i], "--admin-group wheel", "--admin-group", 1),
		"the differing line must differ only in the admin-group value")
}

// TestSingleLineDifferenceRejectsSecondLineDiff proves the comparison behind
// TestBrokerUnitsDifferOnlyInAdminGroup is a real check rather than a
// tautology: a second difference injected into the real unit contents is
// reported, and a dropped line is rejected outright.
func TestSingleLineDifferenceRejectsSecondLineDiff(t *testing.T) {
	deb := readUnitLines(t, debUnitPath)
	rpm := readUnitLines(t, rpmUnitPath)

	require.Greater(t, len(rpm), 3, "unit file is unexpectedly short")

	mutated := append([]string(nil), rpm...)
	mutated[3] += " # injected second difference"

	differing, err := singleLineDifference(deb, mutated)
	require.NoError(t, err)
	require.Len(t, differing, 2,
		"a second differing line must be detected, not absorbed into the admin-group difference")

	_, err = singleLineDifference(deb, rpm[:len(rpm)-1])
	require.Error(t, err, "a dropped line must be rejected, not compared pairwise")
}

// execStartBinary returns the absolute executable path from a unit's
// ExecStart= line, so the staged sysroot models whatever the unit actually
// runs instead of hard-coding a path that could drift.
func execStartBinary(t *testing.T, unit string) string {
	t.Helper()

	for _, line := range strings.Split(unit, "\n") {
		rest, ok := strings.CutPrefix(strings.TrimSpace(line), "ExecStart=")
		if !ok {
			continue
		}

		fields := strings.Fields(strings.TrimLeft(rest, "-@:+!"))
		require.NotEmptyf(t, fields, "ExecStart= has no command")
		require.Truef(t, filepath.IsAbs(fields[0]), "ExecStart command %q is not absolute", fields[0])

		return fields[0]
	}

	t.Fatal("unit has no ExecStart= line")

	return ""
}

// stageSysroot copies unit into the location the package installs it to
// (/usr/lib/systemd/system/pilothoused.service) inside a throwaway root, and
// materializes the two things a real host supplies but a source checkout does
// not: the executable the unit's ExecStart= runs, and the standard boot
// targets every service is implicitly ordered against. `systemd-analyze
// verify --root` then validates the packaged bytes in a hermetic environment,
// so the check behaves identically on a developer host and inside
// `make docker-ci` rather than depending on what happens to be installed.
//
// This is J3's "copy to a validly named file before verifying" option applied
// to the whole root; the copied bytes are asserted identical to the source, so
// what the validator parses is exactly the checked-in file.
func stageSysroot(t *testing.T, unit []byte) string {
	t.Helper()

	root := t.TempDir()

	binary := execStartBinary(t, string(unit))
	binaryPath := filepath.Join(root, binary)
	require.NoError(t, os.MkdirAll(filepath.Dir(binaryPath), 0o755))
	require.NoError(t, os.WriteFile(binaryPath, []byte("#!/bin/sh\nexit 0\n"), 0o755)) //nolint:gosec // stub stands in for the packaged binary

	unitDir := filepath.Join(root, packagedUnitDir)
	require.NoError(t, os.MkdirAll(unitDir, 0o755))

	for _, target := range []string{"sysinit.target", "basic.target", "shutdown.target"} {
		require.NoError(t, os.WriteFile(
			filepath.Join(unitDir, target),
			[]byte("[Unit]\nDescription=stub boot target\n"),
			0o644))
	}

	staged := filepath.Join(unitDir, "pilothoused.service")
	require.NoError(t, os.WriteFile(staged, unit, 0o644))

	roundTrip, err := os.ReadFile(staged)
	require.NoError(t, err)
	require.Equal(t, unit, roundTrip, "staged unit must be byte-identical to the packaged source")

	return root
}

// verifyUnit runs the real systemd validator against a staged sysroot. Per H1
// there is no hand-written substitute for it anywhere in this package.
func verifyUnit(t *testing.T, analyze string, unit []byte) ([]byte, error) {
	t.Helper()

	root := stageSysroot(t, unit)

	cmd := exec.Command(analyze, "verify", "--root="+root, "/"+packagedUnitDir+"/pilothoused.service")

	return cmd.CombinedOutput()
}

// lookupSystemdAnalyze returns the systemd-analyze binary, skipping the
// calling test with a logged reason when it is absent. `gates_chunk` runs on
// hosts that may not have systemd installed; `.docker/Dockerfile` installs the
// systemd package so `make docker-ci` runs this check for real.
func lookupSystemdAnalyze(t *testing.T) string {
	t.Helper()

	analyze, err := exec.LookPath("systemd-analyze")
	if err != nil {
		t.Skipf("skipping: systemd-analyze is not on PATH (%v); `make docker-ci` installs the systemd package so this check runs there", err)
	}

	t.Logf("using systemd-analyze at %s", analyze)

	return analyze
}

// TestBrokerUnitsPassSystemdAnalyzeVerify runs `systemd-analyze verify`
// against both packaged pilothoused.service variants (H1/J3).
func TestBrokerUnitsPassSystemdAnalyzeVerify(t *testing.T) {
	analyze := lookupSystemdAnalyze(t)

	for _, unitPath := range []string{debUnitPath, rpmUnitPath} {
		t.Run(unitPath, func(t *testing.T) {
			unit, err := os.ReadFile(unitPath)
			require.NoErrorf(t, err, "read %s", unitPath)

			out, err := verifyUnit(t, analyze, unit)
			require.NoErrorf(t, err, "systemd-analyze verify %s failed:\n%s", unitPath, out)
		})
	}
}

// TestSystemdAnalyzeVerifyRejectsMalformedUnit is the negative control for the
// test above: an unbalanced quote in ExecStart= — precisely the class of
// systemd.syntax(7) grammar error a hand-written parser cannot reproduce, and
// the reason H1 mandates the real validator — must make verification fail. If
// this passes, the positive check above is not vacuous.
func TestSystemdAnalyzeVerifyRejectsMalformedUnit(t *testing.T) {
	analyze := lookupSystemdAnalyze(t)

	unit, err := os.ReadFile(debUnitPath)
	require.NoErrorf(t, err, "read %s", debUnitPath)

	broken := strings.Replace(string(unit),
		"--socket /run/pilothouse/broker.sock",
		"--socket '/run/pilothouse/broker.sock", 1)
	require.NotEqual(t, string(unit), broken, "failed to inject an unbalanced quote")

	out, err := verifyUnit(t, analyze, []byte(broken))
	require.Errorf(t, err, "systemd-analyze verify accepted a unit with unbalanced quoting:\n%s", out)
}
