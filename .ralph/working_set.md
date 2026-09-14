Task: M0139-0004 — "re-measure the duplicate hash build map premise, then
act on what you find" (plan-parity milestone group, recon task).
**COMPLETE and committed** (`20af1c187`) and pushed this loop, branch
`plan-parity-with-pg-take2-ralph`.

Files: `docs/design/0100-0149/m0139-0004-duplicate-hash-map-refutation.md`
(new, full method + verdict), `docs/design/README.md` (+index row),
`docs/design/not_ralph/minimize_datum/05-work-estimate.md` (§1.5 table row
struck through + marked REFUTED, "two further observations" bullet
annotated), `docs/design/not_ralph/minimize_datum/02-goopg-current-representation.md`
(§3's originating claim struck through + marked REFUTED), `.ralph/fix_plan.md`
(M0139-0004 checked off + DONE summary). No production `.go` file changed —
recon task per the plan-parity harness carve-out (measurement + a design
note + doc corrections, no production diff). No new test written — the
premise was already refuted by PRE-EXISTING, already-green unit tests this
loop only had to read and connect to the stale doc claim.

What was done: `minimize_datum/05-work-estimate.md` §1.5 priced "delete the
duplicate build map" as a one-commit ~2x peak-build-memory win, on the claim
that `operators_join_agg.go` maintains BOTH `lazyHash` and `lazyIntHash` for
the whole int-key hash-join build. Read `buildLazyHashTable`
(`:598-716`): `o.lazyHashIsInt` is decided ONCE, before the first row, from
the plan's STATIC key types (`:651`, `!o.multiKey() &&
o.plan.HashKeysAreInt64()`). Read `presizeLazyHash` (`:822-855`): allocates
exactly ONE of the two maps, selected by that same flag — confirmed both by
the code and by pre-existing `join_presize_test.go` tests
(`TestPresizeLazyHashChoosesTheLaneTheBuildCommittedTo`,
`TestPresizeLazyHashKeepsAnExistingTable`). Read `lazyHashInsertDatum`
(`:1238-1254`)/`lazyHashInsertKeyed` (`:1285-1309`): each insert commits to
ONE lane per call — int-lane insert returns immediately, or falls through to
the string lane. The ONLY place both maps are ever simultaneously non-nil is
`demoteIntHash` (`:1325-1338`)'s own transient copy loop — a rare defensive
fallback for a static "these keys are int64" promise turning out false
mid-build (bounded by rows-inserted-so-far, not the full build), NOT a
standing property of the routine int-key path — confirmed by
`TestPresizedIntTableStillDemotes` and `dense_build_cut2_test.go`'s
demotion tests (both pre-existing and green at HEAD). The parallel/coop
build path (`parallel_hash_build.go:588-800`) reuses the SAME
`presizeLazyHash`/`buildLoopLeft`/`buildLoopRight` rather than re-deriving
lane logic, so it inherits the same mutual exclusion. A multi-column key
never touches `lazyIntHash` at all (`join_composite_key_test.go`). Ran all
four cited test functions live this loop (`go test ./internal/executor/ -run
'TestPresizeLazyHash...|TestPresizedIntTableStillDemotes'`) — all PASS. `go
build ./...` clean.

Key symbols: `joinOp.buildLazyHashTable`/`presizeLazyHash`/
`lazyHashInsertDatum`/`lazyHashInsertKeyed`/`demoteIntHash`
(`internal/executor/operators_join_agg.go:598,822,1238,1285,1325`),
`parallelBuildLazyHashTable` (`internal/executor/parallel_hash_build.go:588`,
confirms the parallel path shares the same lane logic, not a second
implementation). Pre-existing test files that already pinned this before the
loop started: `join_presize_test.go`, `dense_build_cut2_test.go`,
`join_composite_key_test.go`.

Hypothesis/Findings: **verdict — the premise is REFUTED at HEAD.**
`lazyHash` and `lazyIntHash` are two mutually-exclusive representations of
ONE logical table, chosen once from static plan information, with no
steady-state double retention on any build that completes without a
key-type surprise (the overwhelmingly common case). "Delete the duplicate
build map" prices a bug that does not exist in the current tree — nothing to
delete; the fields already behave as a single table dispatched by
`o.lazyHashIsInt`. This does NOT decide or advance `minimize_datum` (still
NOT APPROVED TO START) — it removes one stale input from a document that
feeds a FUTURE owner decision (M0139-0006's job, not this task's). Per the
harness's browsing restriction, did NOT open `take2 07 §6`
(`plan_parity_fix_take2/` round doc cited by 02 §3 as recording the same
claim "separately") — no task line names it and it's not reached via the
mechanism index; flagged in the design doc as a cheap separate follow-up if
a future reader finds it repeats the same stale claim.

Gates run: recon task, no production diff, so the heavy practice-card gates
(tpch-spotcheck, tpcds sweep) do not apply per the same M0139-S1/S2/S3
precedent. `go build ./...` clean. The four cited pre-existing unit tests
run live and PASS (`TestPresizeLazyHashChoosesTheLaneTheBuildCommittedTo`,
`TestPresizeLazyHashSkipsWhenSizeIsUnknownOrTiny`,
`TestPresizeLazyHashKeepsAnExistingTable`, `TestPresizedIntTableStillDemotes`
— `go test ./internal/executor/ -run '...' -v`, all PASS). `make
ralph-state-guard`: same recurring stale status/progress.json pattern as
every prior loop (status="running"/progress="completed" from the previous
loop's clean exit), auto-repaired to consistent, then confirmed consistent.

In-flight: none. Nightly triage for this loop: `ci/logs/action-items.md`'s
newest run (`20260914-235643`, 14 items) was already fully filed as of
2026-09-15 by a PRIOR loop — re-verified this loop (every AI-id either has
its own task line or is appended to an existing open task); no new filing
needed.

Next step: select the next M0139 slice per the banner order. **M0139-0005**
("re-measure Q4's grouping election ratio") is now the last open M0139
recon slice besides M0139-0006 (which is gated on wanting to consolidate
BOTH -0004's and S3's findings before writing the owner packet — could now
be taken too, since -0004's refutation and S3's residue number are both
banked). For -0005: R81 located the divergence in `electOrderedGrouping`
(`upperorderedgrouping.go:148`) at a startup ratio vs `stdFuzzFactor=1.01`
(goopg 1.0086 inside fuzz vs PG's 1.0118 outside); report whether S1/S2's
narrowed widths move that ratio across the band — the private-cluster probe
pattern from M0139-S3 (`internal/testutil/cluster` + `scaleLoader`, never
touching the shared `:65433` bench server) is directly reusable there.
Alternatively M0140's still-open M0140-0003 remains valid per the banner
("M0140 does not wait on M0139"). Do not re-open S1/S2/S3/-0004 — all four
fully done, gated, and pushed.
