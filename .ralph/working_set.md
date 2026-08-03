(idle — nothing in flight)

M0127-P4.3 is CLOSED, committed and pushed (`8dfbb9a5`). **S4 is OPEN; P4.4 is
next.**

**NEXT LOOP: re-read the `## Current Priority` banner (it wins over this note).
It parks M-NIGHTLY below M0127, so the banner selects the next unchecked M0127
item — `M0127-P4.4` (lateral: outer streams, per-outer re-execution stays,
output no longer accumulates into `o.rows`). IMPLEMENTATION-TODO P4.4; 07 §4.
Bar: UNITS + DS05.**

Carry-over facts a next loop should not re-derive:

- **`openLateral` is now the ONLY `o.rows` producer left in `joinOp`** (and the
  only reason `Next` still has its array arm). P4.4 deletes both.
- **`materializeOp` / `materialBuffer` (`operators_material.go`) is the reusable
  substrate**: lazy fill with PG's `eof_underlying` resume, `Rescan`, work_mem
  prefix + one replayed overflow file, `setUnbounded()`, `openCached`/
  `releaseCache` for a consumer that owns its children. `rescannable` is the
  capability interface.
- **The NL inner cache's work_mem bound ships OFF** behind
  `GOOPG_NL_MATERIALIZE_WORK_MEM=1`. Do NOT "fix" it: DS05 **Q54** is a nested
  loop over a 1.44M-row `store_sales` seq scan, the bounded cache spills, and
  replaying it per outer tuple went 144 s → TIMEOUT. Unbounded it is 95 s —
  faster than the drain-both path. The flip needs `cost_rescan` in
  `costInnerNestLoop` (`internal/planner/joincost.go:115`) = P5.7.
- **P4.1's ledger row #3 is STILL OPEN**: `mergeJoinStream.bufferGroup` keeps
  its hand-rolled twin of `materialBuffer` (`groupMem`/`groupWriter`/
  `groupReader`/`groupRowAt`). The extraction that makes the swap mechanical
  landed with P4.3; the swap itself did not.
- **`GOOPG_HASH_OUTER_JOIN=1`** still gates P4.2's hash RIGHT/FULL planning.
  The P4.3 DS05 sweep ran WITHOUT it (default merge), so the timing comparison
  against the P4.2 sweep has that one confound; row/checksum results are
  identical either way.
- **Repo gofmt baseline is go1.25; local gofmt is 1.26** — never `gofmt -w`.
  `operators_join_agg.go` is gofmt-dirty AT HEAD under the local tool; check
  your own lines with `gofmt -d <file> | grep <your symbol>`.
- **Do NOT `git stash`** in this tree (9+ unrelated entries).
- **Bundle discipline:** `docs/design/leftdeep-joins/**` is NEVER modified
  except its IMPLEMENTATION-TODO checkboxes. The M0127 tracker/progress log is
  `docs/design/0127-pg-shaped-join-search.md` §6.

Gates run this loop: UNITS PASS (twice — once mid-loop, once at final code
state); SPOT PASS (Q12=2 / 15.2 s, Q13=35 / 11.5 s, query phase 27.9 s, peak
10,840 MB); DS05 PASS=94 MISMATCH=0 CKMISMATCH=0 ERROR=0 TIMEOUT=1 (Q72,
pre-existing) SKIP=4 — identical to the P4.2 baseline, no query slower, Q54
36% faster, total 2058 s → 1997 s
(`analysis/leftdeep-joins/2026-08-04-p43-ds05-sweep.txt`); SMOKE via the commit
hook. REGRESS not run — no plan surface changed (no new plan node, no EXPLAIN
line, no planner edit). RACE not run — no new shared state (the cache and the
bitmap are per-joinOp).

In-flight: none.
