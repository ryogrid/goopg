Task: **M0124-0001** — TPC-DS SF=1 dual-engine re-sweep at HEAD, chunked.
Status: once-per-sweep prerequisites DONE + **chunks 1–8 DONE**.

Files: `scripts/tpcds-bench-compare.sh` (reap_pg_orphans port + provenance
guard), `docs/design/0124-0001-tpcds-sf1-head-resweep-protocol.md` (D4 landed,
D4a new, D5 corrected), `docs/design/README.md`, `.ralph/deferral_ledger.md`,
`.ralph/fix_plan.md`, `analysis/tpcds-sf1-resweep-20260728/{s-cold-proof.txt,
chunk-1-4.txt,chunk-5-8.txt,RESULTS.md}`.

Key symbols: `reap_pg_orphans`, `engine_id`, `running_engine_sha`,
`restart_goopg` (all in `scripts/tpcds-bench-compare.sh`).

Findings:
- **Sweep baseline (must be reprinted unchanged by every later chunk):**
  `engine-id bba744a817f7ebdec31fd47edfed40362641dd0c
  c47d4ed683a0ac63d56c7f755e70892a635f3a42 diff=e3b0c44298fc`,
  `TIMEOUT_SEC=600`, `ENGINES="goopg pg"`, `RESTART_AFTER_TIMEOUT=1`, S-cold.
- Q1–Q8 reproduce set A cell-for-cell at the same 600 s budget (Q4 TIMEOUTs on
  BOTH engines; Q5 goopg-only TIMEOUT; Q8 ERROR `column ref ca_zip/57 out of
  MaterializedSlot range 1`, server survives). Table: `RESULTS.md`.
- The `*** SWEEP VOID ***` line in `chunk-1-4.txt` is a FALSE POSITIVE — do not
  re-run that chunk. `go build` stamps `vcs.revision/modified`, so a docs commit
  moves the binary sha; the guard now keys on `engine-id`. Proof in RESULTS.md.
- D5's original S-cold query was vacuous (`'public'::regnamespace` matches
  nothing on goopg); ledger row 2026-07-28 filed for missing `regnamespacein`.
- The reap is not decorative: PG's Q4 timeout left one live backend, reaped.

NEXT LOOP — continue M0124-0001, **chunk `9-16`** (banner: M0124 → M0125;
M-NIGHTLY stays PARKED — keep FILING `## AI-` items, do not select them):
  `ENGINES="goopg pg" TIMEOUT_SEC=600 RESTART_AFTER_TIMEOUT=1 \
   scripts/tpcds-bench-compare.sh 9-16 > \
   analysis/tpcds-sf1-resweep-20260728/chunk-9-16.txt 2>&1`
Foreground, Bash `timeout` 55 min; SPLIT the range across two Bash calls if the
set-A estimate exceeds ~45 min (set A: Q11 and Q14 are the expensive ones).
Then append rows to `RESULTS.md` and move the cursor. No engine commit may land
until the sweep reaches Q99 — a docs/tracker commit is fine (engine-id unmoved).

Gates run: `bash -n` + live header/dry-run of the harness; reap SQL smoke on
65438 (0 victims, exit 0) and a real reap during chunk 1-4;
`RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` PASS; pgbench
smoke via the commit hook (13.2k TPS); `make ralph-state-guard` OK.
In-flight: none.
