(idle — nothing in flight)

Last landed (loop #2): M0118-0009 doc reconciliation — flipped the lagging
per-spec inventory row for `stats.spec` failed→pass + regenerated coverage docs.
NOT an engine change. M0118-0009 checkbox ticked [x] (group fully closed).

What happened: the stats promotion commit 998b9e97 (design 0118-0133) updated only
the suite-level `postgres-oracle-port-status.csv`; the per-spec
`postgres-oracle-target-inventory.csv` `stats.spec` row was still `failed`, so the
two generated md files under-counted (117 pass / 4 failed). Re-verified
`TestPort_IsolationStats` strict PASS (3.0 s), flipped the CSV row (comma-free
rationale — inventory rationale field is UNQUOTED, commas break the parser; see
memory iso_test_harness note), regenerated `upstream-isolation-coverage.md` +
`postgres-oracle-target-inventory.md` via `gen-isolation-coverage` /
`gen-oracle-inventory`. Tally now 118 pass / 3 failed.

Files: docs/test-port/postgres-oracle-target-inventory.csv (+ regenerated .md and
upstream-isolation-coverage.md), .ralph/fix_plan.md (checkbox + note).

REMAINING failed isolation specs (3, all genuinely Effort-L — pick one next loop):
- predicate-gist (M0118-0002): GENUINE SSI over-detection. goopg's coarse
  relation/tuple-grain SIREAD raises spurious 40001 where PG's GiST PAGE-level
  predicate locks see disjoint spatial regions. Fix = GiST spatial page-grain /
  bounding-box / grid-cell SIREAD — same granularity class predicate-hash solved
  with bucket-grain SIREAD (FNV→PageLockTag, design 0118-0099 /
  goopg_hash_index_ssi_bucket_locking). Read-step support already landed (0118-0135:
  point subscript p[0], <<,>> operators) + float output fixed (0118-0136). Most
  tractable of the three.
- predicate-gin (M0118-0002): needs int4[]-column array typing (array[1]→int4[]
  collapses to int4 today) + a real GIN AM.
- deadlock-parallel (M0118-0004): parallel-worker lock groups — goopg has no
  parallel query; not feasible without that subsystem.

Gates this loop: go build ./... clean; TestPort_IsolationStats strict PASS (3.0s);
gen-isolation-coverage + gen-oracle-inventory regenerated clean; make
ralph-state-guard OK (self-repaired progress marker). pgbench smoke = pre-commit
hook (no engine change — docs/CSV only).
