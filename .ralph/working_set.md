(idle — nothing in flight)

M0127-P3.5 is CLOSED and committed. **S3 (P3.1–P3.5) is CLOSED. P4 is next.**

**NEXT LOOP: re-read the `## Current Priority` banner (it wins over this
note). It parks M-NIGHTLY below M0127, so the banner selects the next
unchecked M0127 item — `M0127-P4.1` (streaming merge join: duplicate-group
buffering + overflow file; delete the full-drain `runMergeJoin` /
`buildMergeSide` accumulation). IMPLEMENTATION-TODO P4.1; 07 §2.
Bar: UNITS + REGRESS + DS05.**

Carry-over facts a next loop should not re-derive:

- **goopg's 512 MB `work_mem` default is NOT a no-spill setting.** Measured
  this loop at TPC-H SF1: Q3's lineitem-side build reports `Batches: 8
  (originally 4)  Memory Usage: 475137kB` at the default and needs **6 GB**
  to reach nbatch 1 (2 GB still gives nbatch 2 / 1,998 MB peak). Cause: a
  build row is `[]Datum` (48 B/column) vs PG's packed MinimalTuple. This is
  the number **P5.7's nbatch-aware `hashJoinCost`** must price.
- **Q21 at SF1 completes capped**: rc=0, 132 s, 405 rows, peak VmHWM 16.7 GB
  inside GOOPG_MEM_HIGH=20G/MAX=24G with GOMEMLIMIT=12GiB, GOGC=off. The old
  host-OOM is retired. Driver: `tmp/m0127-s3-spill.sh <bin> <out>`.
- **EXPLAIN reads the spill out** now: `Buckets: N (originally M)  Batches: N
  (originally M)  Memory Usage: NkB` on the **Hash Join** node (goopg has no
  Hash node). Counters: `ctx.HashJoinStats[*planner.Join]` ← `hashBatchState.
  publish()`. TEXT format only; parallel-worker counters are lost (ledgered).
- **`buildGeometry` (operators_join_agg.go) is still the ONE sizing
  derivation** — presize, batch state, shared-build decline and now the
  EXPLAIN nbuckets all read it.
- **P3.4's SPOT timing regression stands and is deliberate** (15.7→28.3 s;
  Q12 = N private builds, Q13 = LEFT honouring work_mem). Ledgered — do not
  "fix" it; it is input to S1/S5 acceptance and to P5.7.
- **Repo gofmt baseline is go1.25; local gofmt is 1.26** — never `gofmt -w`.
  `operators_join_agg.go` is already unformatted at HEAD under local gofmt.
- **Do NOT `git stash`** in this tree (9+ unrelated entries).
- **Bundle discipline:** `docs/design/leftdeep-joins/**` is NEVER modified
  except its IMPLEMENTATION-TODO checkboxes.

Gates run this loop: UNITS PASS; RACE PASS (`make race-gate`, all packages);
DS05 slices 1-50 / 51-99 MISMATCH=0 CKMISMATCH=0 ERROR=0 over all 99 (Q72
TIMEOUT pre-existing); S3 evidence run PASS both clauses; SMOKE via the commit
hook; `make ralph-state-guard` OK. SPOT not re-run (P3.4 measured it one
commit ago and this slice adds only instrumentation); PLAN not run — no plan
surface touched.

In-flight: none.
