# Injected fakes must cover the whole success path, not just the connector

**When it applies:** Any spec/plan that asks for a unit test proving a probe
or manager function behaves correctly "when the connection succeeds" — for a
system D-Bus (`github.com/coreos/go-systemd/v22/dbus`) or any other external
SDK where the client type is a concrete struct, not an interface.

**What to do:** If the success path passes the concrete SDK value (e.g.
`*dbus.Conn`) on to further calls (e.g. `ListUnitFilesContext`), a fake that
only satisfies the *connector* signature (`func(ctx) (*dbus.Conn, error)`)
cannot produce a working success-path test — a zero-value `&dbus.Conn{}`
panics or hangs when its methods are actually invoked. Decompose the
dependency injection point further so the *whole* success path is reachable
through an interface a test can fake end-to-end (e.g. accept a
`unitFileLister` factory, not just a `DBusConnector`, and have the real
production probe build one from the live connection). Splitting out a
`dialSystemd`-style helper that only proves "did connect() error" is not
sufficient if the acceptance criteria describe "systemd present" (which
requires exercising the code after a successful connection) — reviewers will
correctly keep rejecting that as not proving the described behavior, and
repeating the same shallow test across revision rounds will not resolve the
objection. When the concrete type genuinely can't be faked, negotiate a
narrower acceptance criterion up front instead of iterating on a test that
cannot satisfy it.

**Learned from:** mill run for issue #50 — `probeSystemd`'s success path
handed a real `*dbus.Conn` from the injected connector straight into
`probeAutoupdatePairs`, so the added test could only exercise the
`dialSystemd` connect-decision helper, never `probeSystemd` itself under
marker-exists-and-connect-succeeds. The same reviewer objection recurred
across 3 revision rounds without the underlying testability gap being fixed,
and the chunk exhausted `review_rounds` and failed the run.
