# When a reviewer says "make no claim about X," delete the sentence — rewording it will fail identically again

**When it applies:** A reviewer objection says a doc or comment must make
*no claim* (positive or negative) about some not-yet-implemented or
out-of-scope behavior — e.g. "acceptance requires making no claim about
reinstall or removal coverage." This differs from an objection that a claim
is *false*; here the objection is that the claim's mere existence,
regardless of wording or accuracy, violates the acceptance criterion.

**What to do:** The only fix is to remove the sentence (or the specific
clause) entirely, not to rephrase it, narrow its scope, or soften its
wording. A revision that keeps asserting the same thing in different words
— e.g. changing "PAM stacks, systemd-analyze, linkage, reinstall, and
removal are not implemented here" to "reinstall and removal are not
implemented here" — still makes the forbidden claim and will be flagged
again with the same objection text. Before resubmitting, grep the diff for
the flagged line and confirm the assertion is gone, not merely trimmed or
reworded. If the surrounding sentence needs to say *something* about scope,
state only what the chunk *does* cover and stop there — do not add a
trailing clause about what it doesn't.

**Learned from:** mill run for issue #77, chunk 1
(`yeti/OVERVIEW.md`, "Nothing invokes the script yet" section). The
reviewer objected in round 2 that a sentence claimed reinstall/removal
support was not implemented, when the acceptance criterion required no
claim about that coverage at all. Round 3's revision only shortened the
sentence to name fewer unimplemented items while still asserting
"reinstall and removal are not implemented here either" — the identical
objection recurred verbatim, exhausting `review_rounds` (limit 2, actually
used 3) and failing the run.
