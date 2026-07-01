(idle — nothing in flight)

Loop #44 landed and committed clean (doc-only, no design doc needed —
mirrors the M0119-0001/0119-0003/0119-0008 triage-closure pattern):

Closed **M0118-0004** (deadlock detection) in fix_plan.md — the entire
M0118 milestone (0001-0009) is now `[x]`. All specs named in 0118-0004's
own title were already passing (deadlock-{hard,simple,soft,soft-2},
multixact-no-deadlock, tuplelock-upgrade-no-deadlock); deadlock-parallel
stays infeasible (no parallel-query lock-group abstraction) and is
already tracked with zero actionable backlog under M0119-0008. The one
loose end left dangling in 0118-0004's own prose — "UPDATE/DELETE
conflict-wait on a conflicting lock-only locker" (goopg's producer only
*preserves* non-conflicting lockers into a MultiXactId; it never makes
the writer *wait* on a still-active *conflicting* one, unlike PG's
heap_update/heap_delete MultiXactIdWait) — was promoted to its own open
deferral-ledger row (**M0119-0009**, appended, status `-`) instead of
staying buried under a now-closed checkbox. No isolation spec in scope
currently fails because of that gap (pre-existing, no regression), so it
is backlog, not a blocker.

Updated the "Current Priority" banner: M0117 → M0118 (now DONE) → next up
is **M0110** (M0119-0004/0005/0006/0007 are its active spinoff form).

Gates run: `make ralph-state-guard` (auto-repaired a transient
running/completed timestamp mismatch, then OK); pre-commit pgbench smoke
hook PASS (TPC-B/-N/-S, 0 failed, all three workloads) — mandatory on
every commit regardless of file type per Hard-won Rule #3. No code
changed, so no unit/race/TPC-H spotcheck gates were applicable. Pushed to
origin/align-data-structure-with-pg (68ea3934).

Next candidates (per M0110 being newly "up"; also see the still-open
M0119-0004 backlog from loop #43's carry, which remains valid):
(1) M0119-0004 DU-002: per-AM amadjustmembers dependency-strength policy
(gist/spgist soft deps for CLASS-attributed opclass members) — largest
structural gap in the create-operator-roundtrip design doc chain, needed
for any real GiST/SP-GiST opclass to round-trip through pg_dump.
(2) M0119-0004 DU-002: extend the builtin-operator catalog incrementally
only when a new fixture needs a different builtin operator (still just
the loop #39 6-row int8 slice) — smallest filler, but don't do it
speculatively without a forcing fixture.
(3) M0119-0009 (new, this loop): UPDATE/DELETE conflict-wait on a
conflicting lock-only locker — needs a purpose-built isolation fixture
(none exists upstream) plus the full row-lock/multixact/-race + pgbench
gate suite; highest blast radius of the three, do in a dedicated session.
(4) M0119-0002 (CLOG store swap Part B) — flagged repeatedly as highest
blast radius in this codebase; needs its own dedicated full-gate session
(-race mvcc+wal, xlog_replay, heterogeneous PG-standby E2E, fresh-server
TPC-H Q12/Q13).
(5) M0119-0005/0006/0007 (pg_waldump/pg_amcheck/pg_basebackup server
tiers) — still open, lower priority per the Current Priority banner.

Recommendation for next loop: pick up M0110's live form — continue
M0119-0004 DU-002 slices (candidate 1 is the biggest structural gap;
candidate 2 only if a concrete fixture forces it). If none of those look
tractable in one loop, M0119-0009 is a well-scoped, independently
resumable slice with its own ledger row and resume point.
