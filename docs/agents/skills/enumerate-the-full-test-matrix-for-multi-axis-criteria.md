# Enumerate the full test matrix before writing tests for a multi-axis acceptance criterion

**When it applies:** A plan or spec acceptance criterion packs several
methods/entry points times several conditions into one dense sentence —
e.g. "Query/Execute/StreamAction/StreamQuery marking down on
`ErrUnavailable` and leaving the cache untouched on other errors (e.g.
`ErrUnauthorized` or a domain error)" plus a side constraint like "never
call `QueryCapabilities` themselves." That reads as one requirement but is
actually a cross-product: N methods x M conditions x any extra invariants,
and every cell needs its own assertion.

**What to do:** Before writing tests, write out the matrix explicitly as a
checklist (rows = methods/entry points, columns = conditions/invariants) and
confirm every cell has a corresponding test assertion. Fixing the objection
by adding coverage for whichever cells the reviewer just named, one revision
round at a time, tends to leave other cells still uncovered — the reviewer
re-raises what looks like "the same objection" each round because it
technically is, just against a different missing cell, and this burns
`review_rounds` until the run fails. Treat any reviewer objection that lists
more than one missing method/condition pair as a signal to re-derive the
full matrix from the criterion's prose, not to patch the specific examples
quoted in the objection text.

**Learned from:** mill run for issue #54, chunk 0 (`platform.Host`
capability wrapper methods) — the criterion required
Query/Execute/StreamAction/StreamQuery to each be tested for
"marks down on `ErrUnavailable`," "untouched on `ErrUnauthorized`,"
"untouched on an arbitrary domain error," and "never calls
`QueryCapabilities`." Three successive revisions each patched only the
specific gaps the previous review round had named, leaving other
method/condition cells uncovered every time, exhausting the 2-round review
budget and failing the run.
