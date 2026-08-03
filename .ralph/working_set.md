(idle — nothing in flight)

M0125-0002 is CLOSED (loop #39, 2026-08-03): commit 7 of 8,
`visitColumnRefsByName` re-based onto `walkExprRefs`, completes the
eight-walker series. Banner order is now M0124 → M0125 → M0127; re-read
the `## Current Priority` banner before selecting.

Carry-over facts a next loop should not re-derive:

- The plan-snapshot instrument has an UNQUANTIFIED nondeterminism floor.
  TPC-DS Q85's alias tie-break flips between sweeps of the SAME binary
  (proved with 4 sweeps: before/before2/after/after2). Commits 2–6 were
  each accepted on ONE sweep per arm. Before any future plan-shape
  commit trusts a single A/B, sweep 3× on one binary and diff pairwise.
  Ledger row 2026-08-03; existing task is `M0125-0047`.
- The whole name-in-scope guard family (`extraInScans`,
  `allColumnRefNamesInScope`) is a goopg-only construct. PG uses
  `Relids` bitmapsets + `bms_is_subset` (`initsplan.c:
  distribute_qual_to_rels`). goopg's version still has a
  same-column-name-in-two-tables fail-open that commit 7 did NOT close
  (TPC-H's `p_`/`ps_` prefixes hide it; aliased `date_dim` copies in
  TPC-DS do not). Ledger row 2026-08-03 carries the resume point.
- The cumulative timed 22-query TPC-H EXECUTION power run is still owed
  at M0125-0002's milestone close (not per commit). A planning-time A/B
  was run instead and found planning cost unchanged within resolution.
  `tmp/goopg-c7-before` is rebuildable from `900990a2`.
- Instrument gotchas worth keeping: `make plan-diff` needs a LIVE server
  (diff the two snapshot files directly for an offline A/B); a per-query
  `date +%s%N` around `psql` measures psql's ~4 ms fork+connect floor,
  not planning — use one session with `\timing`; and `tpch.Queries()`
  carries no trailing semicolon, without which `psql` never terminates
  the statement and the whole sweep arrives as one query.

Gates run this loop: units precommit PASS; full `internal/planner`
green (census gate included); 18 pins proved to FAIL against the old
body first; TPC-H plan A/B 22/22 byte-identical (and vs
`post-mhj-retire`); SF0.5 EXPLAIN A/B 96/96 across 4 sweeps;
divergence probe 0 deltas; planning-time A/B 4.41 → 4.54 ms;
`tpch-spotcheck.sh` RESULT=PASS (Q12=2 / 21.6 s, Q13=35 / 11.2 s);
pgbench smoke via the commit hook; `make ralph-state-guard` OK
(auto-repaired the previous loop's completed marker).

In-flight: none.
