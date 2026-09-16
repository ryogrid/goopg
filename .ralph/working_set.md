Task: M0142-0008c-2 — `joinIsLegal`'s missing SEMI unique-ify admission arm
(PG's `create_unique_path` mechanism, item 2 of 4, M0142-0008c group for
TPC-DS Q10/Q35 parity). DONE and committed this loop.

Files this loop: internal/optimizer/joinsearchlevel.go (two new `else if`
arms in `joinIsLegal`, ported joinrels.c:445-489), internal/optimizer/
specialjoin_test.go (3 new tests: TestJoinIsLegalSemiUniqueIfyAdmitsNonRHSPair
/...ReversedPair/TestJoinIsLegalSemiRejectsWhenNotUniqueIfiable, plus
semiUniqueIfyFixture helper reusing createuniquepath_test.go's
uniquePathFixture), docs/design/0100-0149/m0142-0008a-1-semi-anti-sji-design.md
(§18 added), docs/design/README.md (index row extended), .ralph/fix_plan.md
(M0142-0008c-2 marked [x]).

Key symbols: `(*searchCtx).joinIsLegal` (joinsearchlevel.go:198 — the two new
arms sit between the reversed ordinary-match branch and the "both overlap
RHS" fallback, exactly PG's own order), `createUniquePath` (called as
`createUniquePath(relX, relX.CheapestTotal, sj, s.cp)`), `jointypeForDirection`
(joinpaths.go:161 — the downstream consumer whose MinLefthand-subset check is
what makes this arm currently inert), `joinOrderRestricted`/
`makeRelsByClauseJoins` (joinsearchlevel.go — the pair-admission heuristics
that never offer a unique-ify-only pair to joinIsLegal today).

Hypothesis/Findings: this loop's main risk was NOT "does the port match PG"
(it does, mechanically) but "does admitting a pair -0008c-3 can't yet finish
building turn a previously-harmless decline into a hard planner failure?" —
traced precisely: `makeJoinRel` registers the joinrel via `s.addRel` BEFORE
calling `addPaths`, so if both orientations decline (which `jointypeForDirection`
does for any pair reachable only via this arm, since MinLefthand isn't a
subset of either single input by construction), the joinrel is left with an
EMPTY Pathlist, and `joinSearch`'s per-level loop hard-fails the WHOLE search
on ANY empty-Pathlist rel. Confirmed this is NOT triggered today: the search's
own pair-admission gate (`joinOrderRestricted`) returns false for exactly this
shape (`FROM a,b WHERE (a.x,b.y) IN (SELECT c1 FROM c)`) because it requires
BOTH inputs to overlap MinLefthand/MinRighthand, and a unique-ify-only pair by
definition has only one input overlapping — so such a pair is never proposed
to `joinIsLegal` in the first place. Empirically confirmed via a full TPC-DS
SF0.25 sweep on the dirty tree: PASS=96 MISMATCH=0 ERROR=0 TIMEOUT=0, Q10/Q35
plan shape unchanged (expected — matches -0008c-1's own "no live caller"
finding; -0008c-3 is what will make both pieces live).

Next step: per banner order (M0137-M0143 group), the natural continuation is
**M0142-0008c-3** (thread `JoinTypeUniqueInner`/`JoinTypeUniqueOuter` through
every join-path builder — hash/merge/NLI producers under internal/optimizer/,
PG oracle joinpath.c:113-121 + the 7 call sites design doc §16.1 cites). This
is the WIDEST-blast-radius piece of the four (comparable to -0008a-3(iii)'s
MERGE-decline split, design doc §8, which rippled across multiple builders
for a much narrower change) and is now the SOLE remaining blocker before
either -0008c-1 or -0008c-2 affects a real plan — recon it as its own
sub-task before implementing, given this project's track record on
similarly-scoped joinrels.c/joinpath.c ports (every prior -0008a/-0008c piece
needed a recon-then-implement split). Alternatives if this group is judged
not worth continuing: M0141-S2b-2 (base join/scan Pathlist-across-search-
boundary surgery — flagged by its own filing as needing "its OWN further
scoping pass before writing code", not yet done), M0142-0008a-3i-plumbing
items 3-5 (still 3-subsystem-spanning). M0142-0003i/0003k remain BLOCKED on a
human-authorized shared `:65433` cluster reload — do not attempt.

Gates run: `go build ./...` (clean). `go test ./internal/optimizer/...`
(PASS, full package including 3 new joinIsLegal tests). `RALPH_PRECOMMIT_SCOPE=units
scripts/ralph-precommit-test.sh` (PASS except the ALREADY-KNOWN, unrelated,
pre-existing `internal/parser` TestLockingClauseParity AST-drift failure filed
2026-09-15, GroupedJoinUnaliased field added by a different commit, not
touched this loop). `scripts/tpcds-sf025-regression.sh sweep` (PASS=96
MISMATCH=0 CKMISMATCH=0 ERROR=0 TIMEOUT=0 SKIP=3 — ran against the dirty tree
with this loop's change, foreground, ~3 min). `scripts/tpch-spotcheck.sh`
SKIPPED (shared `:65433` tpch DB still empty per M0142-0003k, pre-existing/
documented blocker, exit 0 — expected). `make ralph-state-guard` — to run
before finishing.

In-flight: none.
