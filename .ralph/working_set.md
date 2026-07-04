(idle — nothing in flight)

M0110-0001 DU-002 slice 444 deferral CLOSED this loop (2026-07-04): fixed
simple-query multi-statement batch atomicity for CREATE TABLE/CREATE INDEX.
Root cause: `dispatchSimpleQueryViaExecutor` (internal/server/dispatch.go)
begins ONE mvcc.Transaction per Query message, so a later statement's failure
must roll back an earlier successful CREATE TABLE too — but
`catalog.InMemory.RegisterTable` is non-transactional, and the existing undo
machinery (`sess.RecordDDLCreate`/`executor.ProcessRollbackUndos`) never
engaged for autocommit batches (`ectx.Session` stayed nil outside an explicit
transaction). Fix: wire a message-scoped throwaway `*executor.BasicSession`
for autocommit batches + call `ProcessRollbackUndos` in the abort defer before
`TxnMgr.Rollback`; removed `RecordDDLCreate`'s dead-code `inTx` guard (was
unreachable pre-fix, all prior callers already had inTx==true).

Files touched: internal/server/dispatch.go (predeclare ectx above the abort
defer; throwaway BasicSession wiring; ProcessRollbackUndos call),
internal/executor/session.go (RecordDDLCreate guard removed),
internal/server/dispatch_batch_atomicity_test.go (new regression, confirmed
RED pre-fix), docs/design/root-0024-simple-query-batch-ddl-create-atomicity.md
(new design doc) + docs/design/README.md (indexed), .ralph/deferral_ledger.md
(M0110-0001 slice 444 row flipped resolved), .ralph/fix_plan.md (new [x] entry
+ Current Priority banner updated).

Gates run: go build ./... clean; internal/server + internal/executor +
internal/catalog + internal/mvcc + internal/wal + internal/initdb full suites
PASS (no regression); -race on internal/mvcc + internal/wal + internal/server
PASS; scripts/tpch-spotcheck.sh PASS (Q12=2/Q13=33); gofmt -l clean on all
touched files (2 pre-existing unrelated drift files noted, not touched, per
the known go1.25-vs-go1.26.3 gofmt-version-mismatch memory); pgbench smoke
enforced by .githooks/pre-commit at commit time; make ralph-state-guard
self-repaired the recurring benign status/progress mismatch, clean after.

Deliberately deferred (documented in the design doc's own Deferred section,
not separately ledgered): (1) enum/composite-type creation and
TRUNCATE/DROP-in-savepoint tracking stay explicit-transaction-only (gated on
BasicSession.inTx, which the throwaway session deliberately keeps false to
avoid touching ~20 unrelated InExplicitTransaction() call sites — deferred
UNIQUE/EXCLUDE/FK constraint timing, stats scoping); (2) a
`CREATE TABLE t1(...); BEGIN; CREATE TABLE t2(...); ROLLBACK;` compound batch
still loses t1's undo entry when the throwaway session is replaced by the real
one mid-batch (not a regression — was never undoable before either).

Next step: not yet committed as of end of this turn's work — commit + push
this change set (triggers the pgbench-smoke pre-commit hook). Then resume
M0110/M0119 per fix_plan.md's Current Priority banner: continue the
M0119-0004 pg_dump catalog-view parity battery (next gap via
`TestPort_PgDumpConnectionSetup`), or pick up one of the two residuals above
if a fresh discovery motivates it.
