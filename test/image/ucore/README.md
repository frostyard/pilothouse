# uCore image-test inputs

`cosign.pub` is the uCore signing public key copied byte-for-byte from
`ublue-os/ucore` commit
[`724b05abfcdb1ab4633cd3880d26b28a8dad3e64`](https://github.com/ublue-os/ucore/commit/724b05abfcdb1ab4633cd3880d26b28a8dad3e64).
Its SHA-256 is
`af78ecfda6eb21c35195af3739341715e9cfc3f2f5911dd9c10b0670547bf6e8`.
An upstream key rotation requires an explicit reviewed update here.

`Containerfile` is test infrastructure. It installs the already verified
released RPM with all repositories disabled, then overlays digest-verified
`pilothouse` and `pilothoused` executables built from the checked-out head.
The release remains the packaging substrate while the executable overlay makes
the pull-request gate exercise its own broker behavior. For only release ID `358276825`
and asset ID `486354638`, tag `v0.6.0`, and its matching RPM basename, it
verifies that RPM's known Debian PAM file digest and replaces the file with
the digest-pinned Fedora policy from
`packaging/rpm/pilothouse.pam`, SHA-256
`af72dc5708248288d056e3ef7d8188d6c24b6991f1f2b50e4805e2108f505993`;
later releases receive no override. It enables the packaged units, adds a
baseline or update slot marker, and runs `bootc container lint`. The resulting
images are fixtures in the caller's isolated Podman storage; they must never
be pushed.
