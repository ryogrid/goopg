(idle — nothing in flight)

## Loop #7 (2026-09-01) result — M0134-0186 (`without_overlaps.sql`) PARKED

**Nightly triage:** `ci/logs/action-items.md` still shows run `20260901-010436`
(same sha as loops #4-#6) — already filed (rows -005/-006/-007). No new items
to file this loop.

**Task:** M0134-0186 — `without_overlaps.sql`. **PARKED** (CSV `not-tried` →
`failed`, `pass_required=no`). Sized live for the first time: 0/1 PASS,
3572-line diff, 445 `^+ERROR`, 0% parity. No code fix shipped — every root
cause traces to one of two REFACTOR-tier prerequisites, neither completable
in one loop (see reasoning below). Design
`docs/design/0100-0149/m0134-0186-without-overlaps-sizing.md`.

**Why PARKED, not fixed:** whole file exercises PG's SQL:2011 temporal
`PRIMARY KEY`/`UNIQUE`/`FOREIGN KEY` (`WITHOUT OVERLAPS`/`PERIOD`).
Four buckets, all interdependent: (1) `PRIMARY KEY (... WITHOUT OVERLAPS)`
doesn't parse (`pk_cols` in `grammar/goopg_ext.y` has no such alternative);
(2) `FOREIGN KEY (col, PERIOD ref_col)` doesn't parse either (separate
grammar production); (3) no GiST access method to back the exclusion
constraint even if (1)/(2) parsed (`btree v0 only supports int4/numeric
keys`); (4) no range/multirange operator family (`@>`/`<@`/`&&`/`+`) —
confirmed to be exactly the already-fully-ledgered **M0134-0173** gap
(`internal/executor/expr.go`'s `evalBinaryOp` dispatches `OpContains`/
`OpContainedBy`/`OpOverlap` on *textual shape* not static type, so a range
operand falls into the box-operand branch and errors). Landing grammar
support for (1)/(2) alone moves **zero** diff lines since every statement
still needs (3) or (4) to execute — unlike `vacuum_parallel.sql`
(M0134-0185), there's no grammar-only win here. `regressExcluded`'s
pre-existing "out of scope for goopg v0" policy note confirmed accurate,
left unchanged. New deferral ledger row filed for buckets (1)/(2)
specifically (the WITHOUT OVERLAPS/PERIOD grammar gap — (3)/(4) already
covered by other ledger rows, notably M0134-0173).

**Gates run:** `scripts/pg-regress-runner.sh -v without_overlaps` (sizing,
0/1 PASS as expected — case stays out of the pass-required set); `make
regen-testport` clean 5-file regen; `make check-testport-inventory` PASS;
`make ralph-state-guard` PASS (one auto-repair: progress.json stale
"completed" marker reconciled to in_progress — same benign pre-existing
pattern as loops #4-#6); pre-commit pgbench smoke PASS (487/627/10997 TPS,
0 failed). No unit/build gates needed — this loop changed docs/CSV/ledger
only, no Go code.

**NEXT LOOP:** Re-check banner (M0134 priority as of writing). Next
unclaimed M0134 case per ordering is **M0134-0187** (`generated_stored.sql`,
`failed`, never sized this milestone) — pick that up unless the banner
changes. The two M-NIGHTLY `pg_stat_activity` failures filed loop #4
(AI-20260901-010436-005/-007) remain untriaged — not selected this loop
either since M0134 stays next-priority per banner.

**In-flight:** none.
