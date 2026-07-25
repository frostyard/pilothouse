# A drift guard that checks "every expected item is present" must also check "nothing extra is present"

**When it applies:** Writing or reviewing a drift-guard/contract test that
derives an "expected" set from one source (e.g. `.goreleaser.yaml`'s
`builds[]`/`nfpms[]`) and a "declared" set from another (e.g. a hand-written
contract table's requirements), where the acceptance criterion is phrased as
a guarantee that editing the declared side to disagree with the live source
must fail the test — not just that the two named/expected members are
findable.

**What to do:** A loop that walks the expected set and asserts each member
has a matching entry in the declared set (`require.Equal(t, 1, count)` for
each known binary/path) only proves a subset relation: expected ⊆ declared.
It does not catch the declared set gaining an unauthorized extra member
(e.g. a stray `/usr/bin/extra` requirement row), because nothing ever counts
or enumerates the declared side independently. Whenever the guarantee is
"the two sides must match" (not just "the known members must be covered"),
assert the converse too: count or collect every declared-side item that
falls in the relevant category (e.g. every requirement destination under
`/usr/bin`) and require that count/set equals the expected set exactly —
multiset equality, not one-directional membership. Before submitting,
mentally add one bogus extra row to the declared side and confirm the test
would actually fail.

**Learned from:** mill run for issue #70, chunk 7 (`packaging/drift_test.go`,
`TestBinaryDestinationsMatchBuilds`) — three consecutive review rounds
rejected the same defect: the test asserted each of the two live binaries
had exactly one matching `/usr/bin` requirement, but never rejected a
contract table that declared a third, unauthorized `/usr/bin` requirement,
since guard 1 skips all `/usr/bin` rows and this test only counted the two
expected destinations. The run failed out (exhausted review rounds) with
the defect still unfixed.
