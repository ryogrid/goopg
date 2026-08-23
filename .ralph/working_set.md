Task just completed: M0134-0105 (compression.sql) — sized live against PG 18.3
oracle: **PARKED** (case genuinely `failed`, 336-line diff / 0% parity, one
contained validation fix shipped).

Landed: goopg's `COMPRESSION <method>` column clause validated nothing —
`normalizeCompressionMethod` (internal/parser/ddl.go) silently discarded any
unrecognized method name to "", and no code path checked the target column's
type against PG's toastability rule. Ported real PG's `GetAttributeCompression`
(postgres/src/backend/commands/tablecmds.c:22043-22076) as new
`validateColumnCompression` (internal/executor/operators_ddl.go): raises 0A000
"column data type %s does not support compression" for a non-toastable type,
22023 "invalid compression method \"%s\"" for an unrecognized method name.
Wired into three sites mirroring validateColumnStorage's existing wiring:
execCreateTable's addCol closure (BodyOrder path), the no-BodyOrder fallback
column loop, and AlterTableSetCompression's handler. Parser change: unrecognized
method now passes through lowercased (lexer already lowercases unquoted idents)
instead of being discarded, so the executor has the original text to validate.
336 -> 307 diff lines. Design doc
docs/design/m0134-0105-column-compression-validation.md, README.md indexed.
Ledger row appended (.ralph/deferral_ledger.md, 2026-08-24, M0134-0105) — the
dominant remaining bucket is `pg_column_compression()`, a builtin entirely
absent from goopg needing NEW per-datum TOAST-compression-method tracking
(REFACTOR-tier: goopg's Column.Compression is catalog metadata only, never
consulted to actually compress a stored value). Three more independent,
unconfirmed buckets ledgered: multi-inheritance compression-method-conflict
detection, materialized-view SET COMPRESSION view-definition propagation, a
one-byte fipshash() length drift. CSV row flipped not-tried -> failed via
`make regen-testport`. fix_plan.md M0134-0105 marked [x].

Committed and pushed to origin/regress-renumbering — see git log for the
commit sha (committed at end of this loop).

Nightly filing: checked ci/logs/action-items.md at loop start — same run
(20260824-013441, sha e7495e712dda) as prior 11 loops, both AI- items already
filed (fix_plan.md lines ~1286/~1312: AdvisoryLock repeat regression + units/
internal/executor regression). No new filing needed this loop.

NEXT LOOP: per the Current Priority banner in .ralph/fix_plan.md, continue
M0134 top-to-bottom — next unworked item is **M0134-0106 (conversion.sql)**.
Standing recommendation (carried across several loops, still open, still not
selected because the banner's straight top-to-bottom order wins absent
explicit re-prioritization):
1. brin_summarize_range/brin_desummarize_range unimplemented, blocks 3 files
   (M0134-0095/-0096/-0097 PARKs).
2. A collation-execution-registry gap recurs across FIVE parked files
   (M0134-0099/-0100/-0101/-0102, ICU/libc/builtin providers all share "no
   locale-aware comparator wired into comparison/sort") — a strong unifying
   candidate if a loop gets reassigned to infra work.
3. bucket (5) from M0134-0102: internal/executor/expr.go length/upper/lower/
   octet_length/etc. swallow a nested function-not-found error into NULL
   instead of propagating 42883 (systemic, cross-file, needs its own
   verification pass across the whole regress-port suite before editing).
4. The ctid/tableoid system-column pattern (CTIDExpr, 13-file wiring, from
   M0134-0104) is a template a future loop could generalize to
   cmin/cmax/xmin/xmax — the MVCC storage side is already 100% done/verified.
5. NEW this loop: pg_column_compression() (M0134-0105) needs a new per-datum
   TOAST-compression-method side-channel — same "new storage concept" shape
   as bucket 4, likely a smaller standalone slice than the collation gaps.

Gates run: go build ./... clean; RALPH_PRECOMMIT_SCOPE=units
scripts/ralph-precommit-test.sh PASS (all unit packages, cache mostly warm);
make regen-testport clean; make check-testport-inventory PASS; make
ralph-state-guard PASS (auto-repaired the same benign stale status/progress
running-vs-completed mismatch seen in prior loops, then confirmed
consistent). Pre-commit hook's pgbench smoke runs automatically at commit
time (mandatory, never bypassed).

In-flight: none. No throwaway server was left running — pg-regress-runner.sh
manages its own goopg instance and stopped cleanly after each invocation.
