#!/bin/sh
#
# Postinstall scriptlet for the frostyard-pilothouse .deb and .rpm packages.
#
# nfpm's `scripts.postinstall` key is not per-format, so this single script is
# wired once in .goreleaser.yaml and runs as both the Debian `postinst` and the
# RPM `%post`. It does the two things nfpm's static owner/group metadata cannot:
# it creates the `pilothouse` system user and group (the payload is extracted
# before that account exists, and nfpm's DEB tar metadata leaves numeric
# UID/GID at zero), and it then repairs the ownership and mode of the
# package-owned configuration directories and environment files.
#
# Fail-fast: `set -e` below means a failure of any repair command aborts with a
# non-zero exit. On Debian that leaves the package in a not-configured state;
# on RPM the payload stays installed but rpm/dnf report the scriptlet error --
# an RPM %post runs after payload extraction and cannot roll back its
# transaction.
set -e

# PILOTHOUSE_ROOT is a test seam and nothing else: packaging/postinstall_test.go
# points it at a temporary directory so the ownership repairs can be exercised
# unprivileged. It defaults to empty, so in production the operands below are
# exactly the paths under the real /etc.
#
# Only direct filesystem operands -- the paths this script hands to chown,
# chmod, and `[ -e ... ]` -- carry the prefix. The systemd-sysusers positional
# configuration path stays root-relative, because --root already relocates
# configuration lookup and prefixing it too would double-prefix it. The
# fallback account's home and shell are recorded values, not paths this script
# operates on, so they stay literal exactly as the sysusers file declares them.
: "${PILOTHOUSE_ROOT=}"

# 1. Create the pilothouse user and group. The invocation is scoped to this
#    package's own sysusers file; a bare `systemd-sysusers` would process every
#    sysusers configuration on the host.
if command -v systemd-sysusers >/dev/null 2>&1; then
    if [ -n "${PILOTHOUSE_ROOT}" ]; then
        systemd-sysusers --root="${PILOTHOUSE_ROOT}" /usr/lib/sysusers.d/pilothouse.conf
    else
        systemd-sysusers /usr/lib/sysusers.d/pilothouse.conf
    fi
else
    # Fallback for hosts without systemd-sysusers. It reproduces every property
    # packaging/pilothouse.sysusers declares -- a system account, primary group
    # pilothouse, home /nonexistent, shell /usr/sbin/nologin, and the same
    # description -- so a login-capable service account can never result. Each
    # command is guarded by an existence check so re-running stays idempotent.
    getent group pilothouse >/dev/null 2>&1 ||
        groupadd --system pilothouse
    getent passwd pilothouse >/dev/null 2>&1 ||
        useradd --system --gid pilothouse --no-create-home --home-dir /nonexistent --shell /usr/sbin/nologin --comment "Pilothouse web service" pilothouse
fi

# 2. The configuration directory is group-readable by pilothouse so the units'
#    EnvironmentFile= reads work. The package always owns it, so no presence
#    guard applies.
chown root:pilothouse "${PILOTHOUSE_ROOT}/etc/pilothouse"
chmod 0750 "${PILOTHOUSE_ROOT}/etc/pilothouse"

# 3. Remote-mount credentials are read only by the root broker, so this
#    directory is stricter than its parent. Also unconditionally package-owned.
chown root:root "${PILOTHOUSE_ROOT}/etc/pilothouse/storage/credentials"
chmod 0700 "${PILOTHOUSE_ROOT}/etc/pilothouse/storage/credentials"

# 4. Both environment files are nfpm `type: config` entries and the units
#    reference them optionally (EnvironmentFile=-), so an administrator may
#    legitimately delete one and a Debian upgrade preserves that deletion.
#    Repair each only when present: an unconditional chown would fail under
#    set -e and break the upgrade. Intentional absence is accepted, and an
#    absent file is never recreated.
for env_file in \
    "${PILOTHOUSE_ROOT}/etc/pilothouse/pilothouse.env" \
    "${PILOTHOUSE_ROOT}/etc/pilothouse/pilothoused.env"; do
    if [ -e "${env_file}" ]; then
        chown root:pilothouse "${env_file}"
        chmod 0640 "${env_file}"
    fi
done
