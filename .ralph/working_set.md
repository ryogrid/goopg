Loop #26: **M0118-0009 async-notify — engine-side LISTEN/NOTIFY subsystem LANDED**
(design 0118-0089). Probed all remaining M0118-0009 specs; each is a distinct
multi-loop subsystem. Chose async-notify (additive, bounded blast radius — no
MVCC/storage/WAL). Built the complete engine side:
- Parser: ListenStmt/NotifyStmt/UnlistenStmt (ident-led) + grammar test.
- protocol: 'A' MsgNotificationResponse + WriteNotificationResponse.
- server/notify.go: notifyHub (mutex; listeners[ch]→set<session>, per-session
  pending inbox keyed by *config.SessionRegistry). conn_tx.go: pendingNotify
  buffer (de-dup), published at COMMIT (autocommit batch end + explicit COMMIT),
  discarded on ROLLBACK/End(). dispatch.go: execNotifyStmt + publishPendingNotify
  + deliverNotifications (drain before ReadyForQuery). server.go: hub init +
  BackendPID/NotifySession + RemoveSession on teardown.
- executor: Context.QueueNotify + pg_notify() builtin (expr.go).
Verified: TestParseListenNotifyUnlisten (6), TestNotifyHub,
TestConnTxBufferNotifyDedup all PASS; -race server clean; build/vet clean;
async-notify.spec wire probe now executes EVERY statement PG-identically (was
syntax-error on first NOTIFY). gofmt: protocol.go/messages.go fail at HEAD too
(pre-existing go1.25/1.26 mismatch) — NOT touched.

**Next step (to PROMOTE async-notify.spec, deferred — ledger 2026-06-24):**
harness-only now. Enhance internal/testport/framework/IsolationRunner: wrap each
session connector with pq.ConnectorWithNotificationHandler (fires synchronously
during query-response reads → matches goopg's command-boundary delivery), record
per-session notifications WITH source PID, map srcPID→session-name, emit
isolationtester lines `<recv>: NOTIFY "<ch>" with payload "<p>" from <src>` with
byte-exact placement; then runIsoSpecStrict + flip CSV/inventory row.

Other M0118-0009 specs still untouched (each a subsystem): horizons (EXPLAIN
FORMAT json ANALYZE + json `->` ops + IOS pruning), intra-grant-inplace
(GRANT catalog-tuple-xmax lock-wait), temp-schema-cleanup (pg_my_temp_schema +
per-session temp namespace OID model), stats (pg_stat_force_next_flush + cumulative
counters), prepared-transactions{,-cic} (2PC). Other open: M0118-0002/0004/0005/0007.
