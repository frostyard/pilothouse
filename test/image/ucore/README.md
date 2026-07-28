# uCore image-test inputs

`cosign.pub` is the uCore signing public key copied byte-for-byte from
`ublue-os/ucore` commit
[`724b05abfcdb1ab4633cd3880d26b28a8dad3e64`](https://github.com/ublue-os/ucore/commit/724b05abfcdb1ab4633cd3880d26b28a8dad3e64).
Its SHA-256 is
`af78ecfda6eb21c35195af3739341715e9cfc3f2f5911dd9c10b0670547bf6e8`.
An upstream key rotation requires an explicit reviewed update here.

`Containerfile` is test infrastructure. It installs the already verified
released RPM with all repositories disabled, adds a baseline or update slot
marker, and runs `bootc container lint`. The resulting images are fixtures in
the caller's isolated Podman storage; they must never be pushed.
