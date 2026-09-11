# REPORT — TPC-DS dev gate migration: SF0.5 → SF0.25 (2026-09-11)

## 1. Mapping (SCOPE §1, all mechanical)

`git mv scripts/tpcds-sf05-regression.sh → scripts/tpcds-sf025-regression.sh`,
then `s/SF05/SF025/g; s/sf05/sf025/g; s/tpcds05/tpcds025/g` across the 17 live
files, plus descriptive `SF=0.5→SF=0.25` / `SF0.5→SF0.25` / `SF 0.5→SF 0.25`
rewordings. Sampling `% 2 == 0 → % 4 == 0` on the SF=1 TSVs (same shared-key
parity argument: kept key keeps all lines, sales↔returns pairs intact; dims
whole). Port 65437 retained (fast-gate port; only its meaning changed).
Historical lines reworded without live tokens (review-note-2 bare-`0.5`
false-positive on `P0.5 base_yylex` avoided by scoping P3 to scale-qualified
forms). `docs/`, `analysis/`, session files untouched. Memory renamed
(`tpcds_sf05_regression_gate.md` → `tpcds_sf025_regression_gate.md`,
`sf05-sweep-must-run-in-foreground.md` → `sf025-…`) + `MEMORY.md` index lines.

## 2. Oracle capture (P0 LANDS)

- `build-data` ~12 s → `load-pg` (COPY + ANALYZE) ~20 s → `oracle` **~3 min**
  (was ~20 min at half scale) → `load-goopg` ~2 min.
- `tpcds-results-sf025/oracle.txt`: **112 lines = 13 header + 99 data** —
  96 OK (**60 ck-verified**, 36 ck=n/a), 3 SKIP_QUERYGEN (Q36/Q70/Q86).
  17 zero-row queries (Q4 Q8 Q10 Q11 Q17 Q24 Q25 Q29 Q37 Q54 Q58 Q73 Q74 Q82
  Q85 Q91 Q93), printed by the oracle step as the weak-signal list.
- Scale-halving prediction held with the documented LIMIT exception: ~a dozen
  ck=n/a LIMIT-window queries pinned at the 100-row bound, dim-only queries
  unchanged. No `PG_ERROR` (no schema/COPY diagnose needed).
- Sampling skew noted: facts landed 24–25% but `inventory` 11745000→2355000
  = 20% (key-distribution skew in `% 4`, not a bug — same parity proof).
  goopg reltuples 24/25.

## 3. Validation sweep (P1 LANDS with adjudicated baseline amendment)

`sweep-20260911-172555.txt`:
`SUMMARY: PASS=96 (60 ck-verified, 36 ck=n/a) MISMATCH=0 CKMISMATCH=0 ERROR=0
TIMEOUT=0 SKIP=3`, exit 0. Plus `plans-20260911-172555.txt` (goopg-side plan
channel). ~5 min (was ~30 min expected at half scale).

Timeout-set amendment (predicted by SCOPE review note 9, adjudicated not
pre-fixed): the R63 baseline set (PASS≈94 + Q72 TIMEOUT) did not survive —
**Q72 now completes on both engines** (PG 100 rows, goopg 100 rows, checksums
agree) and **Q4 completes PG-side** (`4|OK|0|…`, was PG-TIMEOUT at SF0.5;
goopg agrees at 0 rows). Legitimate scale effect, goopg-vs-PG agreement on
identical data holds on every row — the amendment is to the baseline, not a
fix to either engine.

## 4. Plan-parity fixtures (deviation found and fixed during migration)

The first `plans-pg/` re-capture was defective: psql ran without `-qAt`
(header/border framing polluted all 96 files) and Q36/70/86 captured a psql
syntax ERROR instead of the SKIP marker. Re-captured properly
(`-qAt`, every statement EXPLAIN-prefixed, SKIP marker referencing the new
script `:128` PG_SKIP line): no ERROR/framing artefacts, 99 files.
Diff vs HEAD fixtures: costs/rows rescaled to the new dataset (e.g. Q72
catalog_sales 232470→116257, exactly halved); 47/96 queries show node-shape
deltas (parallel↔serial flips across worker thresholds — a reference-moved
effect, committed as the new reference per the fixtures README rule).

## 5. P2 / P3

- P2: the script has no `--help` path (a bare `--help` falls into the main
  gate, so the run was stopped), smoke done equivalently: `bash -n` syntax OK
  + `source bench/tpcds/env_tpcds.sh` clean with renamed vars
  (`SF025_PORT=65437`, `SF025_PG_DB=tpcds025`). No orphan servers left behind.
- P3: scope grep over the 11 SCOPE paths returns zero lines. One deliberate
  exception outside P3 scope: `scripts/tpcds-result-checksum.py:11` still
  names the historical design doc `docs/design/0124-0005-sf05-oracle-checksum-column.md`
  — the doc itself is history (untouched per user order), so the pointer keeps
  its filename.
- Two late-caught stale lines fixed post-P3: script oracle-header template
  `:500` + captured `oracle.txt` header (`~20 min` → `~3 min`); README
  mechanism/format/weak-signal/timeout-set prose (§3 of this report's work).
- Review BLOCK B1 (post-review fix): `bench/tpcds/timings/README.md` was found
  emptied (0 bytes) — beyond SCOPE, which allowed only oracle/db-name mention
  updates. Restored in full with retokening: title → half-scale-era archive +
  archive-status banner, live pointers → new script/oracle paths; protocol,
  both caveats, and the 128 MB shared_buffers historical note preserved
  verbatim. The SF0.5 timing data files stay in the tree as the archive the
  restored README describes.
- Review notes N3/N4 (post-review fix): `CLAUDE.md` gate timing `~1 h` →
  `~5 min` (matches README-measured sweep); `env_tpcds.sh:74` anecdote →
  scale-agnostic "fast-gate harness" (on 2026-07-29 it was the SF0.5 harness).

## 6. Teardown / reclaim

`DROP DATABASE tpcds05`; `rm -rf data-sf05/ tpcds-data-sf05/ goopg.sf05.log`
(~2.8 GiB reclaim: 372 M TSVs + 1.4 G loaded + log). PG dbs now `tpcds`,
`tpcds025`. Archived `tpcds-results-sf05/` + pipeline log stay on disk as
untracked history. Golden swap staged as `git rm` old oracle + `git add` new.

## 7. Commit set (explicit pathspec, no `-A`, no `--no-verify`)

Renamed script, `env_tpcds.sh`, `server.sh`, `AGENT.md`, `CLAUDE.md`,
`bench/tpcds/README.md`, both `timings/` + `plans-pg/` READMEs,
`bench/tpch/plans-pg/README.md`, 9 script-comment files,
`docs/design/not_ralph/{04-testing-and-gates.md,TODO.md}`,
`bench/tpcds/plans-pg/Q*.txt` (99), new `tpcds-results-sf025/oracle.txt`,
old `tpcds-results-sf05/oracle.txt` removal, SCOPE.md + this REPORT.
NOT in set: `internal/` (R64 tree already pushed), `analysis/`, memory files
(outside repo).
