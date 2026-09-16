Task: M0142-0008c-3 — recon + decomposition (per the previous loop's own
plan: "recon it as its own sub-task before implementing"). DONE and
committed this loop, NO production code changed (pure recon/decomposition,
matching the M0140-0006 precedent).

Files this loop: docs/design/0100-0149/m0142-0008a-1-semi-anti-sji-design.md
(§19 added — full recon writeup), docs/design/README.md (index row extended
with a §19 summary sentence), .ralph/fix_plan.md (M0142-0008c-3 marked [x]
as closed-as-decomposition; four new sub-tasks filed: M0142-0008c-3a/3b/3c/3d).

Key symbols (all read live this loop, not from memory): `jointypeForDirection`
(joinpaths.go:160 — the ONE production caller is `addPathsToJoinrel`,
confirmed via find_referencing_symbols, so its signature change is cheap),
`addPathsToJoinrel` (joinpaths.go:249 — calls 8 builder functions with `jt`),
`addNLIPaths` (joinpathsnli.go:269 — already collapses outer to a single
`outer.CheapestTotal` candidate, so PG's "restrict UNIQUE_OUTER to
cheapest-total outer" guard is already structurally true here), `addNestLoopPath`
(pathgen.go:149), `createUniquePath`/`RelOptInfo.CheapestUnique`
(createuniquepath.go:49, path.go:568 — landed by -0008c-1, cache-backed,
callable directly). PG oracle read live: `match_unsorted_outer` and
`sort_inner_and_outer` (postgres/src/backend/optimizer/path/joinpath.c:1403-1441,
1811-1957) — each substitutes-and-demotes LOCALLY, not via shared dispatch;
`make_join_rel` (joinrels.c:960-1013) — the actual PG call site that invokes
JOIN_UNIQUE_INNER/OUTER (goopg's analogue is `makeJoinRel`, joinsearchlevel.go:572,
already unconditionally tries both directions via `jointypeForDirection`, so
no new goopg dispatch site is needed there — confirmed while tracing this).

Hypothesis/Findings: the key result this loop is that reading Q10/Q35's
ACTUAL committed PG plans (bench/tpcds/plans-pg/Q10.txt, Q35.txt) sharply
narrows -0008c-3's real scope versus how §16.3 originally sized it.
(1) Neither witness exercises hash-join or merge-join AT ALL — both use
JOIN_UNIQUE_OUTER + an INDEXED nested loop: store_sales deduped via
HashAggregate(ss_customer_sk) as the outer, customer probed by customer_pkey
as the indexed inner. So the "thread through every builder" framing was too
broad; only addNestLoopPath (JoinTypeUniqueInner) and addNLIPaths
(JoinTypeUniqueOuter) are actually needed for these two witnesses — hash/merge
threading (3c/3d) is deferred as unexercised. (2) Only ONE of Q10/Q35's three
OR-connected EXISTS clauses is even eligible for create_unique_path — the
other two (inside the OR) stay as correlated "hashed SubPlan" filters by SQL
legality (an OR'd EXISTS cannot decorrelate to a semijoin), which is a
SEPARATE, unrelated divergence from any SubLink-pullup-eligibility gap, out
of scope for -0008c entirely. Even after 3a-3d land in full, goopg's Q10/Q35
plans will NOT byte-match PG's unless that separate gap is also closed —
recorded explicitly so a future loop doesn't mistake it for a -0008c-3 bug.
(3) -0008c-1a's HASH-method blocker (goopg's *Distinct being full-row-only)
does NOT apply to this specific witness, since the HashAggregate's only
surviving column is the group key itself (no ungrouped passthrough columns) —
narrows -0008c-1a's relevance but doesn't resolve it generally.
Decomposition rationale: 3a (dispatch — jointypeForDirection's signature
change + new admission arm + a NEW internal/optimizer-private synthetic type,
explicitly NOT new parser.JoinType consts since PG's own comment says these
must never propagate outside the join-path module) is a pure-plumbing slice
expected to produce ZERO plan-shape change on its own (explicit acceptance
check for whoever implements it); 3b (the two builder substitutions) is what
should actually move Q10/Q35; 3c/3d (hash/merge/partial-NL) are deferred,
unexercised by any known witness.

Next step: per banner order (M0137-M0143 group, item 4), select
**M0142-0008c-3a** next (the dispatch layer — jointypeForDirection signature
change + admission arm + synthetic type). It is the smallest, most
self-contained of the four new sub-tasks (single production caller, no
builder changes, explicit "zero plan-shape change" acceptance bar makes it
easy to verify), and unblocks 3b. Read design doc §19.3-19.4 item 3a first.
Alternatives if this group is judged not worth continuing: M0141-S2b-2 (base
join/scan Pathlist-across-search-boundary surgery — still needs its own
scoping pass, not done), M0142-0008a-3i-plumbing items 3-5 (still
3-subsystem-spanning). M0142-0003i/0003k remain BLOCKED on a human-authorized
shared `:65433` cluster reload — do not attempt.

Gates run: `go build ./...` (clean — this loop touched only docs/markdown,
no .go files). `make ralph-state-guard` — found and safely auto-repaired a
stale status/progress mismatch from the previous loop's clean exit (status
still said "running"/"executing" while progress said "completed"; guard
reconciled progress to "in_progress" since the completed marker was the
prior loop's own exit marker, not a real project completion), then passed
clean. `go test ./internal/optimizer/...` NOT re-run this loop (no .go files
changed — nothing to regress). `scripts/tpch-spotcheck.sh`/`tpcds-sf025-regression.sh`
NOT run this loop (docs-only change, no plan/row-count risk).

In-flight: none.
