Task: M0142-0008c-1 — `RelOptInfo.CheapestUnique` cache field +
`createUniquePath` producer (PG's `create_unique_path`, item 1 of the
M0142-0008c 4-item decomposition for Q10/Q35 parity). DONE and committed
this loop, SORT method only, no live caller yet (that's M0142-0008c-2).

Files this loop: internal/optimizer/path.go (CheapestUnique field, PathUnique
kind, Path.UniqueKeyCols field), internal/optimizer/createuniquepath.go (NEW
— createUniquePath), internal/optimizer/createplansimple.go
(createUniquePlan arm), internal/optimizer/createplan.go (PathUnique
dispatch), internal/optimizer/unnest.go (existsUnnestSJInfo now populates
SemiRhsExprs), internal/optimizer/createuniquepath_test.go (NEW, 4 tests),
internal/optimizer/exists_unnest_sjinfo_test.go (SemiRhsExprs assertions
added to 2 existing tests), docs/design/0100-0149/m0142-0008a-1-semi-anti-sji-design.md
(§17 added), docs/design/README.md (index row extended),
.ralph/fix_plan.md (M0142-0008c-1 marked [x], M0142-0008c-1a filed),
.ralph/deferral_ledger.md (row appended).

Key symbols: `RelOptInfo.CheapestUnique`/`PathUnique`/`Path.UniqueKeyCols`
(path.go), `createUniquePath` (createuniquepath.go — gates on
`sjinfo.Jointype==JoinSemi`, `SemiCanBtree`, non-empty `SemiRhsExprs`,
`subpath.Kind==PathPrebuilt`; requires cr.Index to still name the same
column in subpath.node.Output()), `createUniquePlan` (createplansimple.go —
always emits *DistinctOn, never *Distinct), `existsUnnestSJInfo`
(unnest.go — now sets `sj.SemiRhsExprs` from each `unnestParam.SubCol`).

Hypothesis/Findings: two things recon (M0142-0008c's own §16) did not
predict, both found only while writing the producer, not by static
reading: (1) `SpecialJoinInfo.SemiRhsExprs` was declared since M0128-P1.4
but had ZERO writers anywhere before this loop — fixed. (2) PG's HASH
method needs a hash-keyed-SUBSET dedup with ungrouped passthrough columns
that NO goopg executor node can express (`*Distinct` is full-row-only,
`*DistinctOn` is sorted-only) — filed as M0142-0008c-1a, NOT on the
critical path (the one live SemiRhsExprs producer always sets
SemiCanBtree/SemiCanHash together, so the SemiCanBtree-only gate never
actually declines a real query today). `createUniquePath` requires
`subpath.Kind==PathPrebuilt` — confirmed (not just assumed) this is the
WHOLE reachable domain today, not a temporary restriction: SemiRhsExprs is
only ever populated by the EXISTS/IN-unnest atomic-RHS wrapping, and
ordinary FROM-clause SEMI never reaches deconstruction (specialjoin.go's
own comment). Zero behavior change to any existing plan this loop:
grep-confirmed createUniquePath/PathUnique have no caller outside my new
files, and SemiRhsExprs had zero readers before this loop either.

Next step: per banner order (M0137-M0143 group, item 4), the natural
continuation is **M0142-0008c-2** (`joinIsLegal`'s missing SEMI
unique-ify admission arm, joinsearchlevel.go:198 — a direct ~20-line port
of `joinrels.c:445-489`, now unblocked since -0008c-1 supplies
`createUniquePath` to call). That is the item that would first give
Q10/Q35 a *reachable* unique-ified join candidate, though -0008c-3
(threading JoinTypeUniqueInner/Outer through every join-path builder)
is still needed after it before any plan can actually USE the result —
size that dependency before assuming -0008c-2 alone moves the metric.
Alternatives if this group is judged not worth continuing: M0141-S2b-2
(base join/scan Pathlist-across-search-boundary surgery — flagged by its
own filing as needing "its OWN further scoping pass before writing code",
not yet done), M0142-0008a-3i-plumbing items 3-5 (still 3-subsystem-
spanning). M0142-0003i/0003k remain BLOCKED on a human-authorized shared
`:65433` cluster reload — do not attempt.

Gates run: `go build ./...` (clean), `go test ./internal/optimizer/...`
(PASS, includes 4 new createUniquePath/createUniquePlan tests + 2 extended
existsUnnestSJInfo tests). `RALPH_PRECOMMIT_SCOPE=units
scripts/ralph-precommit-test.sh` (PASS except the ALREADY-KNOWN, unrelated,
pre-existing `internal/parser` TestLockingClauseParity AST-drift failure
filed 2026-09-15 in fix_plan.md's "Manually discovered" section —
GroupedJoinUnaliased field added by a different commit, not touched this
loop). `scripts/tpch-spotcheck.sh` SKIPPED (shared `:65433` tpch DB still
empty per M0142-0003k, pre-existing/documented blocker, exit 0 — expected
and low-risk since this loop's code has no live caller). `make
ralph-state-guard` — to run before finishing.

In-flight: none.
