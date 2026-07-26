# A Makefile/script `docker run` invocation must pin `--platform` and `--user` explicitly whenever the image is a caller-supplied variable

**When it applies:** Writing or reviewing a chunk that adds or edits a
`docker run` invocation (in a Makefile target, shell script, or CI step)
where the image reference comes from a caller-supplied variable (e.g.
`INSTALL_IMAGE`, `TEST_IMAGE`) rather than a single hard-coded image the
author fully controls — especially when the target's contract requires a
specific architecture (e.g. "amd64-only") or a specific privilege level
(e.g. "runs as root").

**What to do:** Never assume Docker will pick the right architecture or
user just because the documented/example image references happen to be
correct. A multi-arch image index lets Docker silently select the host's
architecture when `--platform` is omitted, and `docker run` always inherits
the image's baked-in `USER` (root or otherwise) when `--user` is omitted.
If the target's contract depends on either property, encode it explicitly
in the invocation itself (`--platform linux/amd64 --user 0`), not just in
a README comment or an example value — a caller can supply *any* image
reference matching the variable's shape, and the invocation must be correct
for all of them, not just the documented examples. When reviewing such a
chunk, check the actual `docker run` line for these flags rather than
trusting surrounding prose that says "runs as root" or "amd64 only".

**Learned from:** mill run for issue #77 (packaging verify-package-install),
chunk 3 — the same two objections (missing `--platform linux/amd64` and
missing an explicit root-user override) were raised in three consecutive
revision rounds against `make verify-package-install`'s `docker run`
invocation. Each round fixed one gap while leaving or reintroducing the
other, exhausting the revision budget and causing the run to terminate
without landing the chunk.
