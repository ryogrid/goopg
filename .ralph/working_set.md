Task: M0142-0008a-3i-plumbing, item 1 of design doc §14.3 (probe landed this
loop) — item 2's "parallel type vs shared type" question is now DECIDED by
live evidence. Items 3-5 remain open and still not sized for one loop.

Files this loop: internal/optimizer/m0142_0008a_3i_plumbing_probe_test.go
(NEW — throwaway probe, no production diff), docs/design/0100-0149/
m0142-0008a-1-semi-anti-sji-design.md (§15 added), docs/design/README.md
(index row extended), .ralph/fix_plan.md (M0142-0008a-3i-plumbing bullet
updated in place, still unchecked), .ralph/deferral_ledger.md (new row
M0142-0008a-3i-plumbing-probe1).

Key symbols: `extractSearchLeavesAdmitSemiAnti` (the probe file's local copy
of `extractSearchLeaves`, joinsearchseam.go:1070 — copied not edited),
`outerChainLink`/`outerOnQualsOK` (joinsearchseam.go:1283/927 — the existing
consumer that DECLINES a Semi/Anti link with nullable=0, live-confirmed),
`deriveOuterLinkConstants` (joinsearchseam.go:832 — the reason nullable
cannot just be re-encoded to the RHS range: its whole correctness argument
is NULL-extension, which Semi/Anti has none of), `problemPairsOuterWithDerived`
(relfromjoinlist.go:563 — confirmed LIVE, not just re-read, to skip Semi/Anti
`SpecialJoinInfo` values via `default: continue`), `existsUnnestSJInfo`
(unnest.go:4397 — used in the probe to build a real SpecialJoinInfo rather
than a hand-rolled one).

Hypothesis/Findings: (1) leaf-list prediction from §14.3 item 1 held exactly
(2 leaves: t1, RHS *Project as one opaque leaf) — zero surprises. (2) On the
FINAL planned tree, a hash-keyed Semi/Anti's correlation lives in
(j.LeftKey, j.RightKey), not j.Predicate (came back nil) — a probe-only
artifact of inspecting the post-method-selection tree rather than predp.go's
real pre-search origChain; reconstructed via a BinaryOp for the probe only.
(3) Item 2 DECIDED: build a genuinely separate `semiAntiChainLink` type with
its own legality consumers, NOT a Jointype-discriminated outerChainLink.
Evidence: nullable=0 gets an unconditional `outerOnQualsOK` decline
(relids-subset check fails on a well-formed link); the alternative of
encoding nullable=RHS-range to satisfy that arithmetic would make
`deriveOuterLinkConstants` silently apply NULL-extension reasoning to a join
type that has none — a correctness trap, not a workaround. (4) NEW safety
finding, ledgered: `problemPairsOuterWithDerived` (Q78 firewall) has ZERO
Semi/Anti coverage today, live-confirmed with a real existsUnnestSJInfo
value. Items 3-5 (admitting a real Semi/Anti link into production search)
MUST add this firewall's Semi/Anti arm in the same change, not after —
`take3-C-04a-Q78-firewall-classifier` (deferral ledger) is the cautionary
precedent for what happens when that's deferred.

Next step: M0142-0008a-3i-plumbing items 3-5 (still open, still spans three
subsystems — extractSearchLeaves's PRODUCTION walk, existsUnnestSJInfo, and
predp.go's splice retirement — so still not sized for a single loop): (3)
define `semiAntiChainLink` (LHS/RHS RelSet, Jointype, pred — no
preserved/nullable fields) and its own `semiAntiOnQualsOK`/
`semiAntiLinksHaveSJInfos` consumers mirroring the outer ones' RelSet-subset
reasoning minus every NULL-extension branch; (4) actually extend PRODUCTION
`extractSearchLeaves` (joinsearchseam.go:1110) to admit JoinTypeSemi/Anti
using the probe's now-validated shape; (5) rebuild `existsUnnestSJInfo`'s
throwaway synL=1/synR=2 numbering with real leafRangeRelSet bits at
admission time, add the firewall's Semi/Anti arm in the SAME change, and
retire runJoinSearchBelowPinned's splice for admitted cases. Alternatives if
still judged too large: M0142-0008c (create_unique_path scoping, independent,
concrete PG-source resume point) or M0142-0005 (per-worker Memoize scoping,
independent) are both open and unblocked. M0142-0003i/0003k remain BLOCKED
on a human-authorized shared `:65433` cluster reload — do not attempt.

Gates run: `go build ./...` (clean), `go test ./internal/optimizer/...`
(PASS, 2.8s, includes the new probe test), `gofmt -l` on the new file (clean,
no diff). `make ralph-state-guard` run before finishing — found a stale
status/progress mismatch from the previous loop's clean-exit marker,
auto-repaired to in_progress, then consistent (see status block).

In-flight: none.
