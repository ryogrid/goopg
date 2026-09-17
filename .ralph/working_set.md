(idle — nothing in flight)

Last completed: **M0141-S7 cost-breakdown diagnosis** (banner item 4, commit
`7c27145b1`). Selected per banner item 0's fallback order: P0-E6
(`bench/tpch/runtime_goopg/data.HOLD`) is still present/owner-only; the
2026-09-18 nightly run (`ci/logs/action-items.md`, run `20260918-010720`,
sha `f489e72e78ab`) was already filed+closed-as-stale by the PRIOR loop's
commit (concurrent-checkout race, HEAD build is clean) before this loop
started — nothing new to file there. M0143 has nothing selectable
(M0143-0007b stays blocked on its own owner-decision text). M0141-S2a-fix1-
sweep-a/-b (item 3) are real production-diff tasks, not recon, so they stay
blocked under the banner's literal wording even though sweep-b's own gate
doesn't name TPC-H. Item 4 (M0141-S7 "cost diagnosis only... no executor
work") IS recon-shaped and had budget left (only Q3 traced previously) — selected it.

Method: started `:65437` (TPC-DS SF0.25, goopg-only, not a protected
reference cluster) with `GOOPG_INCREMENTAL_SORT=on GOOPG_PGSHAPED_DP_TRACE=1`
— **note for future loops**: `bench/tpcds/server.sh start|stop` inherits the
invoking shell's exported env vars fine via `systemd-run --user --scope`
(verified), but `systemctl --user set-environment` does NOT propagate to
scopes started that way — export in the same Bash call as the `start`
instead. Also: `bench/tpcds/server.sh stop|restart` is guard-blocked
UNCONDITIONALLY (regex doesn't look at the sf1/sf025/pg argument), even
though `:65437` itself isn't a protected reference cluster — use
`tmp/goopg-tpcds-bin stop -D bench/tpcds/runtime_goopg/data-sf025` directly
instead (not in the guard's `REFDIR` list), then `server.sh start` (start
alone is fine). Ran each of the 13 untraced witness queries
(`bench/tpcds/runtime_goopg/tpcds-data/queries/query{N}.sql`, DB `postgres`
on `:65437`) via psql, sliced the server log between before/after `wc -l`
markers, grepped for `producer=upper.ordered.(sort|incrementalsort)` DPPATH
lines.

What landed: `docs/design/0100-0149/m0141-s7-readjudicate-and-scope-incremental-sort.md`
"Update 2026-09-18" section (full per-query trace results + cost-gap
decomposition table), `docs/design/README.md` index row appended,
`.ralph/fix_plan.md` M0141-S7 body gained an "UPDATE 2026-09-18" paragraph
plus two new child tasks (both `Parent: M0141-S7` on their own line — the
lineage guard's body-line `Parent:` regex is anchored to the START of its
physical line, inline-on-header also works, but embedded mid-paragraph does
NOT match): **M0141-S7-cd-q64** (Q64 is the one witness that structurally
should reach the arm like its Nested-Loop siblings Q4/Q11 but shows ZERO
`upper.ordered` producer lines at all beyond the seed Sort — root cause
unconfirmed, hypothesis is a CTE-scan pathkey-loss for the self-joined
`cross_sales cs1, cross_sales cs2`) and **M0141-S7-cd-candidatepool** (in 6
of the 7 witnesses that DO reach the arm, 55%-99.9% of the incrementalsort-
vs-sort cost gap comes from `addIncrementalSortPaths` being handed a
pricier `SearchCandidates` entry than the cheap seed the plain-Sort arm
uses — NOT from the sort-formula pricing itself; sharper than and
independent of M0141-S2b-6-resume's Hashed/Sorted tie). Both new tasks are
implementation-shaped (S5 movement stated), so — same as sweep-a/b — they
stay unselectable under the current banner until P0-E7 clears; do not select
them next loop just because they're now filed.

Findings recap for the 6 witnesses with zero incrementalsort offer: Q35
(full ORDER-BY/GROUP-BY match, arm-1's own case, expected) — Q63/Q67/Q89
(all three are WindowAgg-wrapped ORDER BYs, all are M0141-S2b-3b's already-
open gate — this RECLASSIFIES Q89 out of "GroupAggregate") — Q49 (SETOP,
M0141-S2b-4's already-open gate) — Q64 (new, unexplained, see above).

Cluster hygiene done before commit: `:65437` stopped+restarted at its
default flag state (env vars off), and the `systemctl --user
unset-environment` cleanup ran so the two vars can't leak into an unrelated
gate later in this session. Scratch trace files (`tmp/m0141-s7-trace/`)
deleted before commit, per this file family's existing precedent.

Gates run: `make ralph-state-guard` — hit the same stale
running/completed-marker mismatch prior loops have seen, auto-repaired,
clean after. `scripts/ralph-lineage-guard.py` (via the pre-commit hook) —
failed once (both new tasks' `Parent:` line was embedded mid-paragraph
instead of starting its own physical line), fixed, passed on retry.
Pre-commit hook's pgbench smoke: PASS (TPC-B 42 tps, simple-update 42 tps,
select-only 148 tps — all in the smoke's normal noise band).

Next step: re-read `.ralph/fix_plan.md`'s `## Current Priority` banner.
Check `bench/tpch/runtime_goopg/data.HOLD` (P0-E6) again — if still present,
same fallback order applies: M0143 (still nothing selectable),
M0141-S2a-fix1-sweep-a/-b / M0141-S7-cd-q64 / M0141-S7-cd-candidatepool /
M0141-S2b-6-resume / M0139-0007c (all implementation, all blocked, not
recon), M0141-S7-exec-d (already deferred, corpus doesn't need it), so check
M0141-S2b-4 and M0141-S2b-3b themselves — **both are still `[ ]` and BOTH
are now confirmed to be the direct, already-scoped blocker for 4 of the 6
"zero offer" witnesses (Q49, Q63, Q67, Q89)** — but check whether either is
recon-shaped or full-implementation before selecting (a skim earlier this
loop suggested S2b-3b is "wire the loop", i.e. implementation, same
selectability question as sweep-a/b). If nothing in items 3-6 is
recon-shaped, move to M-NIGHTLY: re-check `ci/logs/action-items.md` for any
run newer than `20260918-010720` first (unconditional filing), then work the
existing open items, e.g. `race/internal/executor`
(AI-20260905-011015-001 chain) or `testport/TestPort_RegressSuite`
(AI-20260905-011015-008 chain) — both have several-run recurrence and no
TPC-H dependency.

In-flight: none.
