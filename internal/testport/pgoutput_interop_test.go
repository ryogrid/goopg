// M0103-0004: pgoutput wire-byte interop verification.
//
// Two subtests, each crossing a goopg ↔ PG boundary:
//
//   (a) TestPort_PgoutputInteropPGToGoopg — goopg's `wal.DecodeMessage`
//       consumes raw pgoutput bytes produced by an upstream PG primary.
//       The producer is `pg_logical_slot_get_binary_changes`, which
//       returns one row per CopyData payload the walsender would have
//       shipped; concatenating the rows yields the exact byte stream a
//       libpq subscriber would consume off the wire. Pure byte-level
//       verification of goopg's decoder against PG's encoder.
//
//   (b) TestPort_PgoutputInteropGoopgToPG — symmetric direction. The
//       pubsubcluster harness spawns goopg as publisher + upstream PG
//       as subscriber. CREATE SUBSCRIPTION on PG dials goopg's
//       walsender (replication=database), issues CREATE_REPLICATION_SLOT
//       LOGICAL pgoutput, then START_REPLICATION SLOT … LOGICAL. The
//       PG apply worker decodes the goopg-emitted pgoutput stream and
//       writes to the PG heap; assertions read the PG side directly to
//       prove the encode-and-stream path is libpq-compatible end-to-end.
//
// See `docs/design/0103-0003-pgoutput-wire-interop.md`.

package testport

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/goopg/goopg/internal/testutil/pgcluster"
	"github.com/goopg/goopg/internal/testutil/pubsubcluster"
	"github.com/goopg/goopg/internal/wal"
)

// freeTCPPort returns an OS-assigned ephemeral port. Race-safe enough
// for tests: the listener is closed immediately, so a subsequent bind
// to the same port is the standard pattern across this codebase.
func freeTCPPort() (int, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port, nil
}

func TestPort_PgoutputInteropPGToGoopg(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping in short mode")
	}
	pg := newInteropPG(t)
	if pg == nil {
		return // newInteropPG calls t.Skip when binaries are missing
	}
	defer pg.stop()
	if err := pg.start(); err != nil {
		t.Fatalf("pg start: %v", err)
	}

	// Schema + publication. Logical decoding requires REPLICA IDENTITY
	// FULL or a primary key to ship UPDATE/DELETE OldTuple; the PK
	// on `id` satisfies that.
	pg.mustExec(t, `CREATE TABLE t (id int PRIMARY KEY, v text);`)
	pg.mustExec(t, `CREATE PUBLICATION p FOR ALL TABLES;`)

	// Pre-create the logical slot so the changes generated below are
	// captured from this LSN onward. The slot uses pgoutput as the
	// output plugin; the proto_version + publication_names options
	// are passed to pg_logical_slot_get_binary_changes when we drain
	// the slot below.
	pg.mustExec(t, `SELECT pg_create_logical_replication_slot('goopg_interop', 'pgoutput');`)

	// Drive a deterministic set of changes. Each statement is its own
	// transaction, producing four Begin/Commit pairs in the slot.
	pg.mustExec(t, `INSERT INTO t VALUES (1, 'hello');`)
	pg.mustExec(t, `INSERT INTO t VALUES (2, 'world');`)
	pg.mustExec(t, `UPDATE t SET v = 'updated' WHERE id = 2;`)
	pg.mustExec(t, `DELETE FROM t WHERE id = 1;`)

	// Drain the slot's accumulated changes as a single bytea via
	// `pg_logical_slot_get_binary_changes`. This is the in-database
	// equivalent of `pg_recvlogical` writing CopyData payloads to a
	// file, but keeps the test independent of any client tool's
	// buffering / exit-condition semantics. Each row is one pgoutput
	// message; concatenating them gives the same byte stream
	// `pg_recvlogical` would write.
	raw := pg.queryBytea(t, `SELECT string_agg(data, ''::bytea ORDER BY lsn) FROM pg_logical_slot_get_binary_changes('goopg_interop', NULL, NULL, 'proto_version', '1', 'publication_names', 'p');`)
	if len(raw) == 0 {
		slotInfo := pg.queryScalar(t, `SELECT slot_name||':'||restart_lsn::text||':'||confirmed_flush_lsn::text FROM pg_replication_slots WHERE slot_name='goopg_interop';`)
		curLSN := pg.queryScalar(t, `SELECT pg_current_wal_lsn();`)
		t.Fatalf("slot drained 0 bytes; slot=%s cur_lsn=%s", slotInfo, curLSN)
	}

	// `pg_logical_slot_get_binary_changes` returns one row per pgoutput
	// message. Concatenated, the bytea blob is a stream of
	// shape-defined messages: a kind byte followed by per-kind
	// fixed-or-prefix-delimited fields. Walk them in order and route
	// each through goopg's `wal.DecodeMessage`.
	msgs, err := splitPgoutputMessages(raw)
	if err != nil {
		t.Fatalf("split pgoutput bytes: %v\n%x", err, raw)
	}

	// Group into transactions. Expect exactly one B/C pair.
	var (
		begins   int
		commits  int
		relMsgs  int
		inserts  int
		updates  int
		deletes  int
		gotRel   *wal.DecodedRelation
		insertVs []string
		updateVs []string
		deleteVs []string
	)
	for _, payload := range msgs {
		m, err := wal.DecodeMessage(payload)
		if err != nil {
			t.Fatalf("DecodeMessage kind=%q: %v (bytes=%x)", payload[0], err, payload)
		}
		switch m.Kind {
		case 'B':
			begins++
		case 'C':
			commits++
		case 'R':
			relMsgs++
			gotRel = m.Relation
		case 'I':
			inserts++
			insertVs = append(insertVs, tupleSummary(m.NewTuple))
		case 'U':
			updates++
			updateVs = append(updateVs, tupleSummary(m.NewTuple))
		case 'D':
			deletes++
			deleteVs = append(deleteVs, tupleSummary(m.OldTuple))
		}
	}

	if begins < 1 || commits < 1 {
		t.Fatalf("missing Begin/Commit: begins=%d commits=%d", begins, commits)
	}
	if relMsgs < 1 || gotRel == nil {
		t.Fatal("no Relation message decoded")
	}
	if got, want := gotRel.Name, "t"; got != want {
		t.Errorf("rel name=%q want %q", got, want)
	}
	if got, want := len(gotRel.Columns), 2; got != want {
		t.Fatalf("rel ncols=%d want %d", got, want)
	}
	// Column 0: int4 (OID 23). Column 1: text (OID 25).
	if gotRel.Columns[0].TypeOID != 23 {
		t.Errorf("col0 typoid=%d want 23 (int4)", gotRel.Columns[0].TypeOID)
	}
	if gotRel.Columns[1].TypeOID != 25 {
		t.Errorf("col1 typoid=%d want 25 (text)", gotRel.Columns[1].TypeOID)
	}
	if inserts != 2 || updates != 1 || deletes != 1 {
		t.Fatalf("DML counts: inserts=%d updates=%d deletes=%d want 2/1/1",
			inserts, updates, deletes)
	}
	// Cross-check at least one tuple's text value to catch obvious
	// decoder regressions.
	if !containsTuple(insertVs, "1", "hello") || !containsTuple(insertVs, "2", "world") {
		t.Errorf("insert tuple values mismatch: %v", insertVs)
	}
	if !containsTuple(updateVs, "2", "updated") {
		t.Errorf("update new-tuple values mismatch: %v", updateVs)
	}
	// REPLICA IDENTITY DEFAULT: delete carries the 'K' marker (key
	// columns only), so non-key columns come through as NULL.
	if !containsTuple(deleteVs, "1", "<null>") {
		t.Errorf("delete old-tuple values mismatch: %v", deleteVs)
	}
}

func TestPort_PgoutputInteropGoopgToPG(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping in short mode")
	}
	// Subtest (b) wiring against the pubsubcluster harness uncovered
	// two distinct gaps in goopg's publisher-side libpq surface that
	// PG's CREATE SUBSCRIPTION exercises before it ever reaches
	// START_REPLICATION:
	//
	//   1) Per-query context premature cancellation in
	//      `runPostStartupLoop`'s replication-mode fall-through.
	//      FIXED 2026-05-14 alongside this commit
	//      (regression test:
	//      `internal/server/replication_test.go::TestReplicationFallthroughQueryNotCancelled`).
	//
	//   2) Parser does not yet accept the `VARIADIC` keyword used by
	//      PG's libpqrcv `fetch_table_list` probe — the query is
	//      `SELECT … FROM (SELECT pg_get_publication_tables(VARIADIC
	//      array(...))) …`. The error returned is the parser-level
	//      "syntax error at or near …(got variadic)" (verified
	//      empirically on 2026-05-14 after gap 1 was closed).
	//
	// Gap 2 + the apply-worker / heap-write surface on the PG side
	// (which exercises the same goopg publisher surface as M0103-0008's
	// Scenario B) are the natural scope of M0103-0008. Closing them
	// there automatically closes subtest (b) as a sub-case, so the
	// deferral is bounded and the alternative path is explicit per
	// the M0103 "DO NOT BYPASS" policy: subtest (b) becomes a thin
	// wrapper once M0103-0008 lands the publisher-side probe-survival
	// work.
	// Probe survival ladder (M0103-0008). Each rung uncovered by
	// dropping this t.Skip and running the live test:
	//   - rung 1 (loop 1, CLOSED): per-query context cancellation in
	//     `runPostStartupLoop`'s replication-mode fall-through.
	//   - rung 2 (loop 2, CLOSED): VARIADIC keyword + FROM-clause
	//     `pg_get_publication_tables` SRF.
	//   - rung 3 (loop 3, CLOSED): `(srf(...)).*` indirection-star
	//     rewrite + `array_agg(text)`.
	//   - rung 4 (loop 4, CLOSED): derived-subquery SRF column-schema
	//     propagation through `__irs_0.*`.
	//   - rung 5 (loop 5, CLOSED): `ProjectSet` lowering for
	//     aggregate-arg SRFs — the `fetch_table_list` shape now plans
	//     and executes against an in-memory fixture.
	//   - rung 6 (loops 6+7, CLOSED): LATERAL FROM-clause SRF arg
	//     resolution. Loop 6 threaded a per-FROM-item LATERAL
	//     `resolveContext` through the planner so `p.pubname`
	//     resolves at the SRF arg site. Loop 7 (this loop) closed
	//     the executor side: `planner.Join` gained a `Lateral` flag,
	//     `joinOp.openLateral` drives a per-outer-row Open/drain on
	//     the right child, and `pgGetPublicationTablesOp` accepts an
	//     outer slot via `BindLateralOuter` and evaluates its Args
	//     through `evalExprSlot` against that slot. Pinned by
	//     `TestLateralPgGetPublicationTablesFromOuterRef` and
	//     `TestLateralPgGetPublicationTablesUnknownYieldsZero` in the
	//     executor package.
	//   - rung 7 (loop 8, CLOSED): analyzer-side IndirectionStar
	//     expansion in derived subqueries for SRFs whose arg list
	//     contains an aggregate. Closed by
	//     `analyzer.compositeFuncColumns` + the new
	//     `synthesizeSubqueryTable` IndirectionStar branch in
	//     `0103-0012-derived-subquery-srf-composite-expansion.md`.
	//     Pinned by `TestPlanFetchTableListAggDerivedSubquery` in the
	//     planner package.
	//   - rung 8 (loop 9, CLOSED): CREATE_REPLICATION_SLOT
	//     parenthesised options list (PG14+ shape). libpqwalreceiver
	//     sends `CREATE_REPLICATION_SLOT "<name>" LOGICAL pgoutput
	//     (SNAPSHOT 'nothing')` from the CREATE SUBSCRIPTION path,
	//     but `replyCreateReplicationSlot` tokenised args via
	//     `strings.Fields` and rejected the `(SNAPSHOT` token with
	//     `unexpected token "(SNAPSHOT" after LOGICAL pgoutput`,
	//     short-circuiting subscription creation. Closed by splitting
	//     off the `(...)` block before whitespace tokenisation
	//     (`splitReplicationSlotOptionsBlock`) and adding
	//     `parseReplicationSlotOptions` which acknowledges SNAPSHOT
	//     'export'|'use'|'nothing', TWO_PHASE, RESERVE_WAL, FAILOVER
	//     as no-ops in v0 and rejects unknown options with a syntax
	//     error so future probe rungs surface loudly. Design:
	//     `docs/design/0103-0013-create-replication-slot-options-list.md`.
	//     Pinned by `TestReplicationCreateLogicalSlotWithOptionsList`
	//     and `TestReplicationCreateLogicalSlotOptionsListMultiple` in
	//     `internal/server/replication_test.go`.
	//   - rung 9 (loop 10, CLOSED): logical walsender stream stability.
	//     Two distinct bugs were keeping the rung-9 surface broken in
	//     concert. (a) `replyCreateReplicationSlot` set
	//     `slot.RestartLSN = WrittenLSN()` (last appended byte's LSN)
	//     instead of `WrittenLSN()+1` (the next record's first-byte
	//     LSN). `NewRecordIterator`'s `pos = startLSN-1` then landed
	//     inside the previous record, and the very first `readOneAt`
	//     decoded garbage payload bytes as an XLogRecord header,
	//     reporting `wal: invalid record header: unknown rmid=240`
	//     (the rmid byte is just whatever random payload happens to
	//     sit at offset 17 from the misaligned position). dec.Run
	//     returned the error and the walsender idled until PG's
	//     `wal_receiver_timeout` fired. Same off-by-one M0094-0005
	//     fixed for `startStandbyReplayer`/`startWalreceiver`. (b)
	//     `runLogicalWalsender` had no keepalive emission, so even if
	//     the decoder had stayed alive, the next 60 s of quiet WAL
	//     would re-trip `wal_receiver_timeout`. The physical-walsender
	//     path in `replyStartReplication` runs a 10 s `time.Ticker`
	//     emitting `protocol.EncodeKeepalive` frames; the LOGICAL path
	//     was missing the symmetric loop. Closed by adding
	//     `walsenderPgoutputAdapter.WriteKeepalive(sendTime)` (shares
	//     the adapter's write mutex so the `'k'` frame never
	//     interleaves with an in-flight `'w'` frame; advertises
	//     `walEnd = last-emitted synthetic LSN`) plus a keepalive
	//     goroutine in `runLogicalWalsender` matching the physical
	//     cadence. Design:
	//     `docs/design/0103-0014-logical-walsender-keepalive-and-slot-restart-lsn-fix.md`.
	//     Pinned by `TestReplicationCreateLogicalSlotRestartLSNIsNextRecord`,
	//     `TestWalsenderPgoutputAdapterKeepalive`, and
	//     `TestWalsenderPgoutputAdapterKeepaliveBeforeFirstWrite` in
	//     `internal/server/`.
	//   - rung 10 (NEW, OPEN): pgoutput emission for goopg-publisher
	//     DML. With rungs 1–9 closed, the live interop test's failure
	//     mode shifts from "60 s timeout error" to "stable
	//     connection, no DML rows propagate" — the apply worker
	//     stays connected past 60 s (no timeout), the decoder runs
	//     without errors, but no `'w'` frame carrying pgoutput
	//     Begin/Relation/Insert reaches the subscriber. Candidate
	//     causes: publication-filter rejection (the
	//     `buildPublicationFilter` may not match `public.t`
	//     registered via the harness), missing Begin/Commit emission
	//     for in-snapshot transactions, or the catalog snapshot
	//     taken at session start not seeing the published table
	//     because of catalog timing. Each is a separate diagnostic
	//     step; the rung-10 fix will land with its own design doc.
	// t.Skip restored — rung 10 deferred to its own M0103-0008 loop so
	// each rung lands with its own design doc + targeted pin.
	//   - rung 11 (NEW, CLOSED): publication_names quoted-identifier
	//     unquoting (PG SplitIdentifierString-equivalent). libpqwalreceiver
	//     sends `publication_names '"p"'` (each name wrapped in
	//     double-quotes to keep names with commas safe). goopg's
	//     `splitPublicationNames` used `strings.Split(raw, ",")` +
	//     `TrimSpace`, so the lookup key became literal `"p"` (with
	//     quotes), missing the stored publication entry — every
	//     decoded change was rejected by `publicationFilter.Allows`
	//     with `byTable=map[]` and `allTablesAllowed={false,false,false}`.
	//     Closed by porting `SplitIdentifierString(rawstring, ',', …)`
	//     semantics (doubled `""` collapses, unquoted identifiers
	//     are downcased to match `downcase_truncate_identifier`).
	//     Design: `docs/design/0103-0016-publication-names-splitidentifier.md`.
	//     Pinned by `TestSplitPublicationNamesQuotedIdentifiers` in
	//     `internal/server/replication_test.go` (or
	//     `internal/server/logicalwalsender_test.go`).
	//   - rung 12 (NEW, OPEN): logical-decoding classifier coverage
	//     for `RecordKindHeapHotUpdate` (13) and `RecordKindPageImage`
	//     (1). With rung 11 closed, the live probe's Insert (kind=4)
	//     and Delete (kind=6) records flow through `pgoutput.Change`
	//     and reach the apply worker; UPDATE records (`kind=13`,
	//     `HeapHotUpdate`) and the first INSERT into a freshly-
	//     allocated page (emitted as `PageImage` kind=1 + `BtreeInsert`
	//     kind=5 by the heap-writer) are silently dropped because
	//     `Classify` has no case for either kind. The test still
	//     fails because the apply worker never sees the UPDATE that
	//     would set `v='updated'`. Closing rung 12 requires either:
	//     (a) extending `Classify` to decode `HeapHotUpdate` /
	//     `HeapUpdate` and `PageImage` into `ChangeUpdate` /
	//     `ChangeInsert` events; or (b) changing the executor's
	//     page-image emission path so a first INSERT into a new page
	//     produces a plain `HeapInsert` record (matching upstream's
	//     pre-image behaviour). The classifier-side fix is the
	//     principled match for upstream's `DecodeHeap2*` family.
	//   - rung 13 (NEW, CLOSED): LATERAL pg_catalog-qualified SRF
	//     parser dispatch.  PG's CREATE SUBSCRIPTION runs
	//     `fetch_table_list_from_publisher` to learn which tables a
	//     publication covers; the probe shape is
	//     `FROM pg_catalog.pg_publication_tables t JOIN pg_catalog.pg_class c
	//      ON (c.oid = ...), LATERAL pg_catalog.pg_get_publication_tables(t.pubname) AS gpt`.
	//     goopg's `parseRangeVar` only recognised SRFs by their
	//     unqualified name (`obj.Schema == ""` gate), so the
	//     `pg_catalog.pg_get_publication_tables(...)` form fell into the
	//     derived-subquery branch and choked with "expected ')' after
	//     subquery in FROM (got ()" at the LATERAL function's opening
	//     paren. CREATE SUBSCRIPTION caught the parse error, registered
	//     ZERO tables in `pg_subscription_rel`, and the apply worker
	//     thereafter silently skipped every Insert/Update/Delete via
	//     `should_apply_changes_for_rel` → false (no rel state → never
	//     READY → not applied). Net symptom: 'w' frames flow,
	//     CONTEXT lines for INSERT/COMMIT appear in the apply worker's
	//     debug log, but `pg_replication_origin_status.remote_lsn` stays
	//     at 0/0 and `count(*)` on the subscriber stays at 0.
	//     Closed by extending `parseRangeVar`'s SRF dispatch to accept
	//     both unqualified and `pg_catalog`-qualified shapes for
	//     `generate_series` / `pg_input_error_info` / `parse_ident` /
	//     `pg_get_publication_tables`. Design:
	//     `docs/design/0103-0019-lateral-pg-catalog-qualified-srf.md`.
	//     Pinned by `TestParseLateralPgCatalogQualifiedSRF` in
	//     `internal/parser/select_test.go`.
	//   - rung 14 (loop 16, CLOSED): `pg_class.relnatts` column missing.
	//     With rung 13 closed, the live probe's parse succeeded and
	//     reached execution, but goopg's `pg_class` virtual table did
	//     not expose `relnatts`. The CASE expression
	//     `array_length(gpt.attrs, 1) = c.relnatts` therefore raised
	//     SQLSTATE 42703 `column "relnatts" does not exist`. Closed
	//     by adding a 9th column `relnatts int4` at ordinal 8 to the
	//     virtual `pg_catalog.pg_class` view, populated as
	//     `len(t.Columns)` per row (user-column count, since v0 has
	//     no system columns). Design:
	//     `docs/design/0103-0020-pg-class-relnatts-column.md`. Pinned
	//     by `TestPgClassExposesRelNatts` in
	//     `internal/catalog/catalog_test.go`.
	//   - rung 15 (loop 17, CLOSED): `pg_get_publication_tables.relid`
	//     vs `pg_class.oid` shape mismatch.  Lifting the `t.Skip`
	//     after rung 14 produced a new failure mode: the apply worker
	//     connected, decoded every `'w'` frame (acks `recv=0/146`
	//     for all four transactions), but `count(*)` on the subscriber
	//     stayed at 0. Query-trace logging in `handleQuery` showed
	//     that CREATE SUBSCRIPTION sent the PG18 `fetch_table_list`
	//     query
	//       `SELECT DISTINCT n.nspname, c.relname, gpt.attrs
	//          FROM pg_class c
	//            JOIN pg_namespace n ON n.oid = c.relnamespace
	//            JOIN ( SELECT (pg_get_publication_tables(
	//                              VARIADIC array_agg(pubname::text))).*
	//                   FROM pg_publication
	//                   WHERE pubname IN ( 'p' )) AS gpt
	//                ON gpt.relid = c.oid`
	//     and the result was zero rows (no SQL error). Root cause:
	//     `buildPgGetPublicationTablesRows` emitted `relid` as
	//     `NewIntDatum(int64(t.OID))`, while goopg's virtual
	//     `pg_catalog.pg_class.oid` stores the relation NAME as text
	//     (the v0 "regclass cast is a no-op" convention — see
	//     `catalog.go:707-712`). `compareDatum(KindInt, KindString)`
	//     falls back to `strings.Compare(a.Format(), b.Format())`, so
	//     the join evaluated `"16384" = "t"` and never matched.
	//     `pg_subscription_rel` stayed empty, tablesync never
	//     launched, and the apply worker's
	//     `should_apply_changes_for_rel` returned false for every
	//     relation.  Closed by emitting `relid` as
	//     `NewStringDatum(t.Name)` so the SRF matches the established
	//     `pg_class.oid` shape. Design:
	//     `docs/design/0103-0021-pg-get-publication-tables-relid-matches-pg-class-oid.md`.
	//     Pinned by `TestPgGetPublicationTablesRelidMatchesPgClassOid`
	//     in
	//     `internal/executor/operators_pg_get_publication_tables_test.go`.
	//   - rung 16 (closed in 0103-0022): added `relreplident` column to
	//     pg_class (default 'd'), flipped pg_class.oid from text-name
	//     to numeric OID (decimal text under wire type 26), flipped
	//     pg_get_publication_tables.relid to KindString decimal OID,
	//     and made `::regclass` cast catalog-aware. Pinned by
	//     TestPgClassExposesRelReplident + TestPgClassOidIsNumericOID
	//     (internal/catalog/catalog_test.go) and the updated
	//     TestPgGetPublicationTablesRelidMatchesPgClassOid
	//     (internal/executor/operators_pg_get_publication_tables_test.go).
	//   - rung 17 (loop 19, CLOSED — M0103-0008 closure): with rung 16's
	//     catalog flip in place, the live `t.Skip` lift produced a fully
	//     passing end-to-end run on first try. The libpqrcv ladder
	//     completes: `fetch_table_list` returns `public.t`, the
	//     column-types LATERAL probe over `pg_attribute` /
	//     `pg_get_replica_identity_index` resolves (goopg's pg_attribute
	//     view already exposed attnum/attname/atttypid and the
	//     `pg_get_replica_identity_index` builtin returns 0 which makes
	//     the LEFT JOIN match-everything path go through cleanly), the
	//     apply worker starts, and PG's `pg_subscription_rel` populates.
	//     Replication of INSERT(1)+INSERT(2)+UPDATE+DELETE arrives at the
	//     PG subscriber within ~10 ms; the final subscriber state is
	//     `id=2 v='updated'` and `pg_replication_origin_status.remote_lsn`
	//     is non-zero. Verified stable over 5 consecutive runs (1.6–1.8 s
	//     each). Design doc:
	//     `docs/design/0103-0023-m0103-0008-scenario-b-closure.md`.

	repo := repoRoot(t)
	pgcluster.Available(t, filepath.Join(repo, "postgres", "local_install", "bin"))

	baseDir := filepath.Join(repo, "tmp", "pgoutput-interop-g2pg")
	_ = os.RemoveAll(baseDir)

	psc := pubsubcluster.NewMixed(t, "pgoutput_g2pg", pubsubcluster.Options{
		RepoRoot:         repo,
		BaseDir:          baseDir,
		PublisherKind:    pubsubcluster.ClusterKindGoopg,
		SubscriberKind:   pubsubcluster.ClusterKindPG,
		SyncMode:         pubsubcluster.SyncModeAsync,
		ApplicationName:  "g2pg_sub",
		PublicationName:  "p",
		SubscriptionName: "g2pg_sub",
		StartupWait:      30 * time.Second,
		ShutdownWait:     10 * time.Second,
	})
	defer func() { _ = psc.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := psc.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	psc.Publisher.Exec(t, "CREATE TABLE public.t (id int PRIMARY KEY, v text)")
	psc.Subscriber.Exec(t, "CREATE TABLE public.t (id int PRIMARY KEY, v text)")
	psc.CreatePublication(t, "t")
	psc.CreateSubscription(t)

	psc.Publisher.Exec(t, "INSERT INTO public.t VALUES (1, 'hello')")
	psc.Publisher.Exec(t, "INSERT INTO public.t VALUES (2, 'world')")
	psc.Publisher.Exec(t, "UPDATE public.t SET v = 'updated' WHERE id = 2")
	psc.Publisher.Exec(t, "DELETE FROM public.t WHERE id = 1")

	psc.WaitForRow(t, "public.t", "id = 2 AND v = 'updated'", 1, 60*time.Second)
	psc.WaitForRow(t, "public.t", "id = 1", 0, 60*time.Second)
	if got := psc.Subscriber.QueryScalar(t, "SELECT count(*) FROM public.t"); got != "1" {
		t.Fatalf("subscriber row count after replication: got %q want 1", got)
	}
}


// TestPort_PgoutputInteropPGToGoopgFullDML is the symmetric counterpart
// to TestPort_PgoutputInteropGoopgToPG: PG publishes, goopg subscribes,
// and the test drives the same four-statement
// INSERT/INSERT/UPDATE/DELETE round-trip used to close M0103-0008 — but
// in the PG→goopg direction (Scenario A). It exercises the apply-worker
// DML paths (`applyInsert` / `applyUpdate` / `applyDelete`) end-to-end
// and verifies fresh-session visibility against goopg's IndexScan path,
// pinning the rung-1 (M0103-0024) caveat that orphan PK entries left
// behind by UPDATE/DELETE are tolerated via heap re-fetch + MVCC
// re-visibility-check.
//
// Design doc: docs/design/0103-0025-m0103-0007-rung-2-pg-to-goopg-full-dml.md.
func TestPort_PgoutputInteropPGToGoopgFullDML(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping in short mode")
	}

	repo := repoRoot(t)
	pgcluster.Available(t, filepath.Join(repo, "postgres", "local_install", "bin"))

	baseDir := filepath.Join(repo, "tmp", "pgoutput-interop-pg2g-fulldml")
	_ = os.RemoveAll(baseDir)

	slotName := "pg2g_full_dml"
	psc := pubsubcluster.NewMixed(t, "pgoutput_pg2g_fulldml", pubsubcluster.Options{
		RepoRoot:         repo,
		BaseDir:          baseDir,
		PublisherKind:    pubsubcluster.ClusterKindPG,
		SubscriberKind:   pubsubcluster.ClusterKindGoopg,
		SyncMode:         pubsubcluster.SyncModeAsync,
		ApplicationName:  slotName,
		PublicationName:  "p",
		SubscriptionName: slotName,
		StartupWait:      30 * time.Second,
		ShutdownWait:     10 * time.Second,
	})
	defer func() { _ = psc.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := psc.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Schema on both ends. goopg has no COPY-into-subscription path; the
	// local relation must exist before CREATE SUBSCRIPTION so the apply
	// worker has a target for the decoded Insert/Update/Delete events.
	psc.Publisher.Exec(t, "CREATE TABLE public.t (id int PRIMARY KEY, v text)")
	psc.Subscriber.Exec(t, "CREATE TABLE public.t (id int PRIMARY KEY, v text)")
	psc.CreatePublication(t, "t")

	// Pre-create the logical slot on the PG publisher. goopg's CREATE
	// SUBSCRIPTION does not yet dial the publisher to issue
	// CREATE_REPLICATION_SLOT (M0103 follow-up), so the slot must exist
	// before CREATE SUBSCRIPTION runs.
	psc.Publisher.Exec(t, fmt.Sprintf(
		"SELECT pg_create_logical_replication_slot('%s', 'pgoutput')", slotName))

	conn := psc.Publisher.Conninfo(slotName)
	psc.Subscriber.Exec(t, fmt.Sprintf(
		"CREATE SUBSCRIPTION %s CONNECTION '%s' PUBLICATION p WITH (enabled = true, copy_data = false, slot_name = '%s', create_slot = false)",
		slotName, conn, slotName))

	psc.Publisher.Exec(t, "INSERT INTO public.t VALUES (1, 'hello')")
	psc.Publisher.Exec(t, "INSERT INTO public.t VALUES (2, 'world')")
	psc.Publisher.Exec(t, "UPDATE public.t SET v = 'updated' WHERE id = 2")
	psc.Publisher.Exec(t, "DELETE FROM public.t WHERE id = 1")

	// Each WaitForRow opens a fresh database/sql connection. The
	// equality predicate exercises the PK IndexScan path — the same
	// path the rung-1 (0103-0024) fix made apply-worker INSERTs visible
	// to.  UPDATE's PK entry is added on the new tuple by
	// applyUpdateByKey; DELETE leaves the PK entry orphaned, and
	// IndexScan's heap re-fetch + MVCC re-visibility-check filters the
	// dead tuple.
	psc.WaitForRow(t, "public.t", "id = 2 AND v = 'updated'", 1, 60*time.Second)
	psc.WaitForRow(t, "public.t", "id = 1", 0, 60*time.Second)
	if got := psc.Subscriber.QueryScalar(t, "SELECT count(*) FROM public.t"); got != "1" {
		t.Fatalf("subscriber row count after replication: got %q want 1", got)
	}
}

// containsTuple looks for an "(a,b)"-shaped summary inside a list.
func containsTuple(have []string, want ...string) bool {
	needle := "(" + strings.Join(want, ",") + ")"
	for _, h := range have {
		if h == needle {
			return true
		}
	}
	return false
}

// tupleSummary renders a `[]wal.DecodedColumn` as "(v0,v1,…)" for
// diagnostic output. NULL → "<null>", unchanged TOAST → "<u>".
func tupleSummary(cols []wal.DecodedColumn) string {
	parts := make([]string, len(cols))
	for i, c := range cols {
		switch c.Status {
		case 'n':
			parts[i] = "<null>"
		case 'u':
			parts[i] = "<u>"
		default:
			parts[i] = string(c.Bytes)
		}
	}
	return "(" + strings.Join(parts, ",") + ")"
}

// splitPgoutputMessages walks one or more pgoutput messages packed
// back-to-back inside `buf` and returns each message's bytes (kind +
// payload). Mirrors the per-kind shape table in
// docs/design/0103-0003-pgoutput-wire-interop.md.
//
// We don't fully decode each message here; we only need to know each
// message's length so we can hand a complete payload to
// `wal.DecodeMessage`. For tuple-carrying messages we delegate to a
// tuple-skipping helper.
func splitPgoutputMessages(buf []byte) ([][]byte, error) {
	var out [][]byte
	off := 0
	for off < len(buf) {
		start := off
		kind := buf[off]
		off++
		var err error
		switch kind {
		case 'B':
			off, err = skipExact(buf, off, 20) // final_lsn(8)+commit_ts(8)+xid(4)
		case 'C':
			off, err = skipExact(buf, off, 25) // flags(1)+commit_lsn(8)+end_lsn(8)+commit_ts(8)
		case 'R':
			off, err = skipRelation(buf, off)
		case 'I':
			// rel_oid(4) | 'N' | tuple
			off, err = skipExact(buf, off, 4)
			if err == nil {
				off, err = skipTupleAfterMarker(buf, off, 'N')
			}
		case 'U':
			// rel_oid(4) | ['O'|'K' tuple]? | 'N' tuple
			// (old tuple is omitted entirely under REPLICA IDENTITY
			// DEFAULT when the update doesn't touch key columns)
			off, err = skipExact(buf, off, 4)
			if err == nil {
				if off >= len(buf) {
					return nil, fmt.Errorf("split: truncated 'U'")
				}
				marker := buf[off]
				if marker == 'O' || marker == 'K' {
					off++
					off, err = skipTuple(buf, off)
				}
				if err == nil {
					off, err = skipTupleAfterMarker(buf, off, 'N')
				}
			}
		case 'D':
			// rel_oid(4) | 'O'|'K' tuple
			off, err = skipExact(buf, off, 4)
			if err == nil {
				if off >= len(buf) {
					return nil, fmt.Errorf("split: truncated 'D'")
				}
				marker := buf[off]
				off++
				if marker != 'O' && marker != 'K' {
					return nil, fmt.Errorf("split: 'D' bad marker %q", marker)
				}
				off, err = skipTuple(buf, off)
			}
		case 'T':
			// truncate: nrels(4) + flags(1) + relid[nrels]*4
			if off+5 > len(buf) {
				return nil, fmt.Errorf("split: truncated 'T'")
			}
			nrels := beUint32(buf[off:])
			off += 5
			off, err = skipExact(buf, off, int(nrels)*4)
		case 'M':
			// message: flags(1) + lsn(8) + prefix(cstring) + len(4) + bytes
			off, err = skipExact(buf, off, 9)
			if err == nil {
				off, err = skipCString(buf, off)
			}
			if err == nil {
				if off+4 > len(buf) {
					return nil, fmt.Errorf("split: truncated 'M' len")
				}
				bodyLen := beUint32(buf[off:])
				off += 4
				off, err = skipExact(buf, off, int(bodyLen))
			}
		case 'O':
			// origin: origin_lsn(8) + name(cstring)
			off, err = skipExact(buf, off, 8)
			if err == nil {
				off, err = skipCString(buf, off)
			}
		case 'Y':
			// type: typid(4) + ns(cstring) + name(cstring)
			off, err = skipExact(buf, off, 4)
			if err == nil {
				off, err = skipCString(buf, off)
			}
			if err == nil {
				off, err = skipCString(buf, off)
			}
		default:
			return nil, fmt.Errorf("split: unknown kind %q at offset %d", kind, start)
		}
		if err != nil {
			return nil, fmt.Errorf("split kind=%q at offset %d: %w", kind, start, err)
		}
		out = append(out, buf[start:off])
	}
	return out, nil
}

func skipRelation(buf []byte, off int) (int, error) {
	// rel_oid(4) | ns(cstring) | name(cstring) | replident(1) | ncols(2) |
	//   per-col [ flags(1) | name(cstring) | typoid(4) | typmod(4) ]
	off, err := skipExact(buf, off, 4)
	if err != nil {
		return 0, err
	}
	if off, err = skipCString(buf, off); err != nil {
		return 0, err
	}
	if off, err = skipCString(buf, off); err != nil {
		return 0, err
	}
	if off, err = skipExact(buf, off, 1); err != nil {
		return 0, err
	}
	if off+2 > len(buf) {
		return 0, fmt.Errorf("relation: short ncols")
	}
	ncols := int(beUint16(buf[off:]))
	off += 2
	for i := 0; i < ncols; i++ {
		if off+1 > len(buf) {
			return 0, fmt.Errorf("relation col %d: short flags", i)
		}
		off++
		if off, err = skipCString(buf, off); err != nil {
			return 0, err
		}
		if off, err = skipExact(buf, off, 4); err != nil {
			return 0, err
		}
		if off, err = skipExact(buf, off, 4); err != nil {
			return 0, err
		}
	}
	return off, nil
}

func skipTupleAfterMarker(buf []byte, off int, want byte) (int, error) {
	if off >= len(buf) {
		return 0, fmt.Errorf("tuple: missing marker %q", want)
	}
	if buf[off] != want {
		return 0, fmt.Errorf("tuple: marker=%q want %q", buf[off], want)
	}
	return skipTuple(buf, off+1)
}

func skipTuple(buf []byte, off int) (int, error) {
	if off+2 > len(buf) {
		return 0, fmt.Errorf("tuple: short natts")
	}
	natts := int(beUint16(buf[off:]))
	off += 2
	for i := 0; i < natts; i++ {
		if off >= len(buf) {
			return 0, fmt.Errorf("tuple col %d: short status", i)
		}
		status := buf[off]
		off++
		switch status {
		case 'n', 'u':
			// no body
		case 't', 'b':
			if off+4 > len(buf) {
				return 0, fmt.Errorf("tuple col %d: short length", i)
			}
			ln := int(beUint32(buf[off:]))
			off += 4
			if off+ln > len(buf) {
				return 0, fmt.Errorf("tuple col %d: short body", i)
			}
			off += ln
		default:
			return 0, fmt.Errorf("tuple col %d: bad status %q", i, status)
		}
	}
	return off, nil
}

func skipCString(buf []byte, off int) (int, error) {
	for i := off; i < len(buf); i++ {
		if buf[i] == 0 {
			return i + 1, nil
		}
	}
	return 0, fmt.Errorf("cstring: unterminated")
}

func skipExact(buf []byte, off, n int) (int, error) {
	if off+n > len(buf) {
		return 0, fmt.Errorf("need %d bytes at %d, have %d", n, off, len(buf)-off)
	}
	return off + n, nil
}

func beUint16(b []byte) uint16 { return uint16(b[0])<<8 | uint16(b[1]) }
func beUint32(b []byte) uint32 {
	return uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3])
}

// ---- interop-PG harness ----
//
// A small self-contained spawner for upstream PG, modelled on
// internal/testutil/tpch/upstreampg_test.go but inlined here so the
// testport package doesn't take a test-only dependency on tpch. Skip
// when local_install/bin/{initdb,pg_ctl,psql,pg_recvlogical} aren't
// present.

type interopPG struct {
	bin     string
	dataDir string
	logPath string
	port    int
	user    string
	dbName  string
}

func newInteropPG(t *testing.T) *interopPG {
	t.Helper()
	bin := filepath.Join(repoRoot(t), "postgres", "local_install", "bin")
	for _, tool := range []string{"initdb", "pg_ctl", "psql", "pg_recvlogical"} {
		if _, err := os.Stat(filepath.Join(bin, tool)); err != nil {
			t.Skipf("upstream PG tool %q not found at %s: %v", tool, bin, err)
			return nil
		}
	}
	port, err := freeTCPPort()
	if err != nil {
		t.Fatalf("freeTCPPort: %v", err)
	}
	user := os.Getenv("USER")
	if user == "" {
		user = "postgres"
	}
	dataDir := filepath.Join(t.TempDir(), "pgdata")
	pg := &interopPG{
		bin:     bin,
		dataDir: dataDir,
		logPath: filepath.Join(t.TempDir(), "pg.log"),
		port:    port,
		user:    user,
		dbName:  "postgres",
	}
	if err := pg.initdb(); err != nil {
		t.Fatalf("initdb: %v", err)
	}
	t.Cleanup(func() { _ = pg.stop() })
	return pg
}

func (p *interopPG) initdb() error {
	cmd := exec.Command(filepath.Join(p.bin, "initdb"),
		"-D", p.dataDir,
		"-U", p.user,
		"--auth-local=trust", "--auth-host=trust",
		"--no-sync",
	)
	cmd.Env = append(os.Environ(), "LC_ALL=C", "LANG=C")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("initdb: %v\n%s", err, out)
	}
	pgConf := filepath.Join(p.dataDir, "postgresql.conf")
	f, err := os.OpenFile(pgConf, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		return fmt.Errorf("open postgresql.conf: %w", err)
	}
	defer f.Close()
	// wal_level=logical is required for pgoutput / pg_create_logical_replication_slot.
	// Note: synchronous_commit must be ON so the WAL-written LSN
	// advances synchronously with commits. With synchronous_commit
	// off, pg_logical_slot_*_changes can't see records the WAL
	// writer hasn't flushed yet, even though they're in shared
	// memory.
	fmt.Fprintf(f, "\nlisten_addresses = '127.0.0.1'\nport = %d\nfsync = off\nwal_level = logical\nmax_replication_slots = 4\nmax_wal_senders = 4\n", p.port)
	return nil
}

func (p *interopPG) start() error {
	cmd := exec.Command(filepath.Join(p.bin, "pg_ctl"),
		"-D", p.dataDir, "-l", p.logPath, "-w",
		"-o", fmt.Sprintf("-p %d -h 127.0.0.1", p.port),
		"start")
	cmd.Env = append(os.Environ(), "LC_ALL=C", "LANG=C")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("pg_ctl start: %v\n%s", err, out)
	}
	return nil
}

func (p *interopPG) stop() error {
	cmd := exec.Command(filepath.Join(p.bin, "pg_ctl"),
		"-D", p.dataDir, "-m", "immediate", "-w", "stop")
	cmd.Env = append(os.Environ(), "LC_ALL=C", "LANG=C")
	_ = cmd.Run()
	return nil
}

func (p *interopPG) mustExec(t *testing.T, sql string) {
	t.Helper()
	cmd := exec.Command(filepath.Join(p.bin, "psql"),
		"-h", "127.0.0.1", "-p", fmt.Sprintf("%d", p.port),
		"-U", p.user, "-d", p.dbName,
		"-v", "ON_ERROR_STOP=1",
		"-c", sql,
	)
	cmd.Env = append(os.Environ(), "LC_ALL=C", "LANG=C", "PGPASSWORD=",
		"LD_LIBRARY_PATH="+filepath.Join(filepath.Dir(p.bin), "lib"))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("psql exec %q: %v\n%s", sql, err, out)
	}
}

func (p *interopPG) queryScalar(t *testing.T, sql string) string {
	t.Helper()
	cmd := exec.Command(filepath.Join(p.bin, "psql"),
		"-h", "127.0.0.1", "-p", fmt.Sprintf("%d", p.port),
		"-U", p.user, "-d", p.dbName,
		"-tA", "-c", sql,
	)
	cmd.Env = append(os.Environ(), "LC_ALL=C", "LANG=C",
		"LD_LIBRARY_PATH="+filepath.Join(filepath.Dir(p.bin), "lib"))
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("psql query %q: %v\n%s", sql, err, out)
	}
	return strings.TrimSpace(string(out))
}

// queryBytea reads a single `bytea` column from a one-row query. The
// returned hex-escape (PG18's default `bytea_output = hex`) is decoded
// to raw bytes. Used to slurp pgoutput payloads accumulated in a
// logical slot via `pg_logical_slot_get_binary_changes`.
func (p *interopPG) queryBytea(t *testing.T, sql string) []byte {
	t.Helper()
	s := p.queryScalar(t, sql)
	if s == "" {
		return nil
	}
	if !strings.HasPrefix(s, "\\x") {
		t.Fatalf("queryBytea: expected hex-prefixed bytea, got %q", s[:min(len(s), 32)])
	}
	hexBody := s[2:]
	if len(hexBody)%2 != 0 {
		t.Fatalf("queryBytea: odd hex length %d", len(hexBody))
	}
	out := make([]byte, len(hexBody)/2)
	for i := 0; i < len(out); i++ {
		hi := hexNibble(hexBody[2*i])
		lo := hexNibble(hexBody[2*i+1])
		if hi < 0 || lo < 0 {
			t.Fatalf("queryBytea: bad hex at byte %d", i)
		}
		out[i] = byte(hi<<4 | lo)
	}
	return out
}

func hexNibble(c byte) int {
	switch {
	case c >= '0' && c <= '9':
		return int(c - '0')
	case c >= 'a' && c <= 'f':
		return int(c-'a') + 10
	case c >= 'A' && c <= 'F':
		return int(c-'A') + 10
	}
	return -1
}
