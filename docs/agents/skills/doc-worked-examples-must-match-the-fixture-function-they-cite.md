# A doc's worked-example payload naming a specific fixture function must be copied from that function's actual output, not composed from its general shape

**When it applies:** Writing or reviewing a `docs/*.md` "worked example"
section that shows a JSON/text payload and attributes it to a specific
named test fixture or helper function (e.g. "the same host with `updex
list` failing" describing what `cannedExtensionsStateUpdexFailed()`
produces) — as opposed to an illustrative example not tied to any
particular function.

**What to do:** Once a worked example is attributed to a named function,
its row count, field set, and prose claims about the payload are all
checkable facts, not narrative color — a reviewer (or future reader) can
open that function and diff it against the doc. Composing the payload from
"what this fixture is roughly supposed to represent" instead of reading the
function's actual construction produces exactly the kind of doc that looks
right and is wrong: rows the function still populates get silently dropped
from the shown JSON, or the prose describes a row as "elided" while the
payload right above it still shows that row present. Before finalizing such
a section: open the named function, list every field/row it actually
constructs (including ones inherited by projecting a shared base fixture,
e.g. `extensionsStateFromSources(false, true)` applied to
`cannedExtensionsState()`), and make the shown payload and the prose match
that construction exactly — then re-read the prose sentence-by-sentence
against the payload it sits next to, since a self-contradiction ("present
in the first payload" next to a payload that doesn't show it) is a
different, cheaper bug to catch than a row miscount.

**Learned from:** mill run for issue #52, chunk 4 (`docs/capabilities.md`
worked examples for `QueryExtensionsState`). Round 1 objected that the
doc's "updex failed" JSON example showed only one row
(`contract-managed-merged`) while `cannedExtensionsStateUpdexFailed()` —
the function the prose explicitly named — projects
`extensionsStateFromSources(false, true)` over `cannedExtensionsState()`,
which keeps every systemd-sysext-contributed row (four of them). Round 2,
after the row count was fixed, objected that the surrounding prose still
said an elided definition "is present in the first payload" when the
shown first payload had been reduced to two rows and did not include it —
a contradiction between adjacent prose and payload introduced while fixing
round 1's objection.
