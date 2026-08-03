(idle — nothing in flight)

M0127-P1.2 is CLOSED (loop #46, 2026-08-03). P0 + P1.1 + P1.2 done.

**NEXT LOOP: re-read the `## Current Priority` banner (it wins over this
note). It parks M-NIGHTLY below M0127, so the banner selects the next
unchecked M0127 item — `M0127-P1.3` (S1 A/B evidence run): Q3/Q10/Q18/Q7
≤ 1.2× R0 (8.46 / 6.04 / 27.58 / 25.13 s; R0 total 493.31 s), no other
query > 1.2× vs pre-S1 HEAD, filed as
`analysis/leftdeep-joins/<date>-s1-ab.txt`. NO CODE. The bar must be met
or attributed per 09 §6 **before P2 starts**.**

Carry-over facts a next loop should not re-derive:

- **P1.3 is a measurement loop, not an edit loop.** Needs a quiet host
  and constant server age (sweep-tail discipline: a server that just ran
  a timeout query sits at GOMEMLIMIT with GOGC=off and thrashes). Pre-S1
  HEAD = the commit before `b253f719` (M0127-P0.1).
- **`make race-gate` is GREEN again** (this loop). It had been red since
  M0126-0006 for `buildEnvInFlight`; that global is now a local in
  `buildWithEnv`, and `buildRec`'s reader is the always-nil
  `opTreeSlab.env`. Do NOT re-file it as a known-red baseline.
- **Fusion is unreachable from the simple-query slab path** — `BuildFast`
  is a top-level entry, so `buildRec`'s Join arm always saw a nil env.
  Only the extended-protocol `executor.Build` path can fuse. Relevant to
  P6.1/P6.2's deletion accounting.
- **`VirtualSlot.Row()` returns a POOLED row** (`acquireRow`), so any
  worker-side retention without `MaterializeForTransfer` corrupts every
  row but the last — that is why the P1.2 retention tests bite.
- **`pgrep -f <pattern>` SELF-MATCHES an `until` wait loop** whose command
  line contains the pattern; poll the LOG, not pgrep.
- **DS05 gate hazard:** after a goopg TIMEOUT the transient
  `goopg-tpcds-sf05.scope` may still be loaded → `systemd-run` fails →
  180 s readiness timeout kills the sweep. Recover with
  `QUERIES="$(seq <n> 99)" scripts/tpcds-sf05-regression.sh sweep`.
  Q47/Q72 are the known 300 s boundary pair, not a regression.
- **`TestPort_RegressSuite` leaks a ppid=1 goopg server** whose data dir
  is already removed — reap by PID before any timed gate.
- **Do NOT `git stash`** in this tree (9 unrelated entries).
- **Bundle discipline:** `docs/design/leftdeep-joins/**` is NEVER
  modified. Tracking = `docs/design/0127-pg-shaped-join-search.md` §6 +
  fix_plan checkbox + README index status.

Gates run this loop: RACE `make race-gate` EXIT=0 (all packages, first
green since M0126-0006); UNITS precommit PASS; SPOT PASS (Q12=2 / Q13=35,
17.8 s, peak 11,597 MB); new tests verified to bite (kill switch + shallow
`MaterializeForTransfer` stub); pgbench smoke via the commit hook;
`make ralph-state-guard` OK.

In-flight: none.
