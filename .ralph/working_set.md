Task: M0142-0008a-3i-plumbing-c22 (DONE, about to commit) — rechecked
whether closing the c1-c21 plumbing chain makes M0142-0008c-3c/-3d/-4's
create_unique_path unique-ify substitution reachable in production.
Answer: NO, for a structural reason independent of c1-c21 and independent
of Q78's outer-over-derived firewall. Design-doc-only change, no
production code touched (temporary debug instrumentation added then fully
reverted before commit).

Files: docs/design/0100-0149/m0142-0008a-1-semi-anti-sji-design.md (new
§58), docs/design/README.md (m0142-0008a-1 row: appended c22 summary after
c21's), .ralph/fix_plan.md (new c22 entry after c21; -3d/-4 entries
corrected with the real resume point).

Key symbols: `jointypeForDirection` (joinpaths.go:179-249 — the ONLY entry
point -3a/-3b/-3c/-3d/-4 share; its unique-ify fallback at line ~235 gates
on `sjinfo.Jointype == parser.JoinSemi`, never ANTI), `reduce_outer_joins`'s
ANTI-demotion producer (specialjoin.go, c19's `demotedAntiSJInfo` —
unconditionally `parser.JoinAnti`, confirmed the only DP-search-reachable
SJInfo producer today), `unnestExistsExpr`/S5a (pins EXISTS/IN family
Semi/Anti joins OUTSIDE the DP search entirely — c21 §57 — so they never
call `jointypeForDirection` at all).

Findings: live-traced (temporary `GOOPG_C22DEBUG=1` stderr prints at the
`case parser.JoinRight, parser.JoinSemi, parser.JoinAnti:` entry and at the
unique-ify fallback itself, in joinpaths.go, fully reverted before commit)
against the FULL TPC-DS SF0.25 corpus (`scripts/tpcds-sf025-regression.sh
sweep`, private `GOOPG_BIN=tmp/goopg-c22-bin` — the shared
`tmp/goopg-bench-bin` is in active use by the running nightly TPC-H lane,
see `goopg_bench_bin_shared_with_nightly_lane` memory). Result: the arm is
entered **zero times** across all 96 completing queries —
`grep -c C22DEBUG goopg.sf025.log` = 0. Root cause, confirmed by direct
observation not just code-reading: (1) EXISTS/IN family (Q10/16/35/69/94
and everything else that unnests cleanly) never reaches this arm — pinned
pre-DP by S5a, per c21. (2) IN-unnesting (unnest.go:3453/3588/4726) never
sets `.SJInfo` at all. (3) The one confirmed DP-search-reachable producer
(reduce_outer_joins ANTI demotion, exercised only by Q78) is
unconditionally ANTI, and the unique-ify fallback's own
`sjinfo.Jointype == parser.JoinSemi` gate excludes ANTI outright — so even
if Q78's `outer-over-derived` firewall (blocked on TODO_ALL B-06,
CTE-output stats) were fully lifted today, `-3d`/`-4` would STILL be
unreachable, because Q78 is the wrong jointype for the fallback that
exists. This corrects `-3c`'s original "blocked on plumbing-c" framing
(now resolved and no longer the limiting factor) and `-3d`/`-4`'s
inherited "shares -3c's blocker" framing (now updated to name the real
gap). Sweep: `PASS=96 MISMATCH=0 CKMISMATCH=0 ERROR=0 TIMEOUT=0 SKIP=3`,
`PLAN-SHAPE: queries=99 same=99 changed=0` — zero regression risk (the
temporary print sits on a decline/no-op path either way). No
deferral-ledger row: nothing NEW left unimplemented — `-3d`/`-4` were
already unchecked/deferred; this recon only corrects their resume point.

Next step: re-read `.ralph/fix_plan.md`'s `## Current Priority` banner
fresh before picking the next M0137-M0143 task. With M0142-0008c-3d/-4 now
confirmed unreachable until a NEW, unfiled mechanism lands (teach an
IN-unnesting path to set `.SJInfo` on a DP-search-visible `JoinSemi` link
— not sized for blind pickup, needs its own scoping recon first), do NOT
implement -3d/-4 next. Remaining open, explicitly named resume points in
this area: (a) file a scoping recon for the IN-unnesting `.SJInfo` gap
just found (new work, unfiled, would need its own design-doc section) —
candidate but unscoped; (b) Q78's `outer-over-derived` firewall itself
(relfromjoinlist.go:654-678), still gated on CTE-output statistics
(TODO_ALL B-06 step 4) — out of scope until that prerequisite lands.
Otherwise fall through to M0141's remaining slices (S2b/S3-S7) or
M0142-0004 onward per the banner's item 4, or M0143 (7/7 untouched) if
those are blocked. Read AGENT.md's plan-parity harness section again
before selecting (required every loop touching M0137-M0143).

Gates run: `go build ./...` clean, `go test ./internal/optimizer/...`
PASS. TPC-DS SF0.25 sweep run TWICE this loop (once for the recheck,
report already captured) — `PASS=96 MISMATCH=0`, plan-shape `changed=0`.
`make ralph-state-guard` self-repaired the same stale running/completed
mismatch seen in prior loops, then passed. No production code changed
(instrumentation fully reverted), so tpch-spotcheck does not apply; commit
still goes through the pre-commit hook's mandatory pgbench smoke.

In-flight: none. Temporary `GOOPG_C22DEBUG=1` instrumentation (2 fmt/os-
gated Fprintf calls plus the `os` import in internal/optimizer/joinpaths.go)
was reverted via `git checkout --` before commit, confirmed by empty `git
diff --stat` on that file. Private binary `tmp/goopg-c22-bin` removed. The
shared SF0.25 goopg cluster (port 65437, `bench/tpcds/runtime_goopg/data-sf025`)
was left running — it is a semi-persistent bench resource per
`bench/tpcds/env_tpcds.sh`, not a throwaway, unlike c20/c21's disposable
schema-only clusters (which were torn down).
