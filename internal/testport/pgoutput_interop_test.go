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

// TestPort_PgoutputInteropPGToGoopgBatchDML extends the rung-2 single-
// row round-trip into a sustained-workload pattern: 50 INSERTs followed
// by 25 UPDATEs over the same PK range and 10 DELETEs of distinct
// rows, all on a PG publisher with a goopg subscriber. It exercises
// the same apply-worker DML paths under load — many rows per pgoutput
// session, many fresh heap pages, multiple xact boundaries — and pins
// fresh-session visibility of every surviving row via the goopg PK
// IndexScan path. This is the next rung toward the M0103-0007 DoD
// (pgbench-shape sustained workload on PG, replicating into goopg);
// the kill -9 + libpq multi-host reconnect plumbing remains future
// work.
//
// Design doc: docs/design/0103-0026-m0103-0007-rung-3-pg-to-goopg-batch-dml.md.
func TestPort_PgoutputInteropPGToGoopgBatchDML(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping in short mode")
	}

	repo := repoRoot(t)
	pgcluster.Available(t, filepath.Join(repo, "postgres", "local_install", "bin"))

	baseDir := filepath.Join(repo, "tmp", "pgoutput-interop-pg2g-batchdml")
	_ = os.RemoveAll(baseDir)

	slotName := "pg2g_batch_dml"
	psc := pubsubcluster.NewMixed(t, "pgoutput_pg2g_batchdml", pubsubcluster.Options{
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

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	if err := psc.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	psc.Publisher.Exec(t, "CREATE TABLE public.t (id int PRIMARY KEY, v text)")
	psc.Subscriber.Exec(t, "CREATE TABLE public.t (id int PRIMARY KEY, v text)")
	psc.CreatePublication(t, "t")

	psc.Publisher.Exec(t, fmt.Sprintf(
		"SELECT pg_create_logical_replication_slot('%s', 'pgoutput')", slotName))
	conn := psc.Publisher.Conninfo(slotName)
	psc.Subscriber.Exec(t, fmt.Sprintf(
		"CREATE SUBSCRIPTION %s CONNECTION '%s' PUBLICATION p WITH (enabled = true, copy_data = false, slot_name = '%s', create_slot = false)",
		slotName, conn, slotName))

	// Phase 1: 50 single-row INSERTs (ids 1..50). Each is its own xact,
	// so the apply worker sees 50 distinct Begin/Insert/Commit triples.
	const inserted = 50
	for i := 1; i <= inserted; i++ {
		psc.Publisher.Exec(t, fmt.Sprintf(
			"INSERT INTO public.t VALUES (%d, 'row-%d')", i, i))
	}

	// Phase 2: 25 UPDATEs over ids 1..25, no key column touched — the
	// rung-2 no-old-tuple path. Each rewrites v to a distinctive marker.
	const updated = 25
	for i := 1; i <= updated; i++ {
		psc.Publisher.Exec(t, fmt.Sprintf(
			"UPDATE public.t SET v = 'updated-%d' WHERE id = %d", i, i))
	}

	// Phase 3: 10 DELETEs of ids 41..50 — the highest range, distinct
	// from the UPDATE range, exercises the DELETE path against rows
	// that have only ever seen the INSERT side.
	const deleted = 10
	for i := inserted - deleted + 1; i <= inserted; i++ {
		psc.Publisher.Exec(t, fmt.Sprintf(
			"DELETE FROM public.t WHERE id = %d", i))
	}

	surviving := inserted - deleted
	psc.WaitForRow(t, "public.t", "1=1", surviving, 90*time.Second)

	// Updated rows should carry the rewritten marker, fresh-session via
	// PK IndexScan.
	for i := 1; i <= updated; i++ {
		psc.WaitForRow(t,
			"public.t",
			fmt.Sprintf("id = %d AND v = 'updated-%d'", i, i),
			1, 30*time.Second)
	}
	// Non-updated, non-deleted rows keep their original v.
	for i := updated + 1; i <= inserted-deleted; i++ {
		psc.WaitForRow(t,
			"public.t",
			fmt.Sprintf("id = %d AND v = 'row-%d'", i, i),
			1, 30*time.Second)
	}
	// Deleted rows are gone (heap re-fetch + MVCC dead-tuple filtering
	// drops the orphan PK entry — same path the rung-1/rung-2 caveats
	// rely on).
	for i := inserted - deleted + 1; i <= inserted; i++ {
		psc.WaitForRow(t,
			"public.t",
			fmt.Sprintf("id = %d", i),
			0, 30*time.Second)
	}
}

// TestPort_PgoutputInteropPGToGoopgReplicaIdentityFull exercises the
// PG-publisher → goopg-subscriber apply path under REPLICA IDENTITY
// FULL on a table without a primary key. The previous rungs covered
// REPLICA IDENTITY DEFAULT (pgoutput omits the old-tuple section or
// emits a 'K' key-only block); this rung pins the 'O' full-pre-image
// branch in `wal.DecodeMessage` and `ApplyWorker.applyUpdate` /
// `applyDelete`. Without a PK the rung-2 `primaryKeyOnlyRow`
// synthesis path is unreachable, so every UPDATE/DELETE must travel
// through `decodePgoutputTupleAsRow(m.OldTuple)` + the full-row
// equality match in `rowMatchesKey`.
//
// Design doc: docs/design/0103-0027-m0103-0007-rung-4-pg-to-goopg-replica-identity-full.md.
func TestPort_PgoutputInteropPGToGoopgReplicaIdentityFull(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping in short mode")
	}

	repo := repoRoot(t)
	pgcluster.Available(t, filepath.Join(repo, "postgres", "local_install", "bin"))

	baseDir := filepath.Join(repo, "tmp", "pg2g-rid-full")
	_ = os.RemoveAll(baseDir)

	slotName := "pg2g_rid_full"
	psc := pubsubcluster.NewMixed(t, "pg2g_ridfull", pubsubcluster.Options{
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

	// No PRIMARY KEY — REPLICA IDENTITY FULL is the only way for a
	// no-PK table to publish UPDATE/DELETE under upstream's rules.
	psc.Publisher.Exec(t, "CREATE TABLE public.t (a int, v text)")
	psc.Subscriber.Exec(t, "CREATE TABLE public.t (a int, v text)")
	psc.Publisher.Exec(t, "ALTER TABLE public.t REPLICA IDENTITY FULL")
	psc.CreatePublication(t, "t")

	psc.Publisher.Exec(t, fmt.Sprintf(
		"SELECT pg_create_logical_replication_slot('%s', 'pgoutput')", slotName))
	conn := psc.Publisher.Conninfo(slotName)
	psc.Subscriber.Exec(t, fmt.Sprintf(
		"CREATE SUBSCRIPTION %s CONNECTION '%s' PUBLICATION p WITH (enabled = true, copy_data = false, slot_name = '%s', create_slot = false)",
		slotName, conn, slotName))

	psc.Publisher.Exec(t, "INSERT INTO public.t VALUES (1, 'a')")
	psc.Publisher.Exec(t, "INSERT INTO public.t VALUES (2, 'b')")
	psc.Publisher.Exec(t, "INSERT INTO public.t VALUES (3, 'c')")
	// UPDATE doesn't touch a key column; under REPLICA IDENTITY FULL
	// pgoutput emits 'O' + full pre-image regardless. Exercises the
	// `len(m.OldTuple) > 0` branch in applyUpdate.
	psc.Publisher.Exec(t, "UPDATE public.t SET v = 'bb' WHERE a = 2")
	// DELETE under FULL carries 'O' + full pre-image too; rowMatchesKey
	// must do a full-row sequential-scan match (no PK index).
	psc.Publisher.Exec(t, "DELETE FROM public.t WHERE a = 1")

	// Fresh sessions throughout. Predicates use only non-indexed
	// columns so the SeqScan path is exercised (no PK to fall back on).
	psc.WaitForRow(t, "public.t", "1=1", 2, 60*time.Second)
	psc.WaitForRow(t, "public.t", "a = 2 AND v = 'bb'", 1, 30*time.Second)
	psc.WaitForRow(t, "public.t", "a = 1", 0, 30*time.Second)
	psc.WaitForRow(t, "public.t", "a = 3 AND v = 'c'", 1, 30*time.Second)
}

// TestPort_PgoutputInteropPGToGoopgUnchangedToast pins rung 5 of
// M0103-0007: the apply-worker's handling of pgoutput's `'u'`
// (unchanged TOAST) per-column status. The publisher's column has
// EXTERNAL storage so every UPDATE that doesn't touch it surfaces
// as `'U' relOid 'N' (t,t,u)` on the wire. The subscriber must
// preserve the existing heap value for the `'u'` slot rather than
// installing NULL.
//
// Design doc: docs/design/0103-0028-m0103-0007-rung-5-pg-to-goopg-toast-unchanged.md.
func TestPort_PgoutputInteropPGToGoopgUnchangedToast(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping in short mode")
	}

	repo := repoRoot(t)
	pgcluster.Available(t, filepath.Join(repo, "postgres", "local_install", "bin"))

	baseDir := filepath.Join(repo, "tmp", "pg2g-toast-u")
	_ = os.RemoveAll(baseDir)

	slotName := "pg2g_toast_u"
	psc := pubsubcluster.NewMixed(t, "pg2g_toastu", pubsubcluster.Options{
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

	// `payload` SET STORAGE EXTERNAL forces the column out-of-line into
	// the TOAST relation. Any UPDATE that doesn't modify it will surface
	// as `'u'` on the wire, not as an inline `'t'`-typed payload.
	psc.Publisher.Exec(t, "CREATE TABLE public.t (id int PRIMARY KEY, name text, payload text)")
	psc.Publisher.Exec(t, "ALTER TABLE public.t ALTER COLUMN payload SET STORAGE EXTERNAL")
	psc.Subscriber.Exec(t, "CREATE TABLE public.t (id int PRIMARY KEY, name text, payload text)")
	psc.CreatePublication(t, "t")

	psc.Publisher.Exec(t, fmt.Sprintf(
		"SELECT pg_create_logical_replication_slot('%s', 'pgoutput')", slotName))
	conn := psc.Publisher.Conninfo(slotName)
	psc.Subscriber.Exec(t, fmt.Sprintf(
		"CREATE SUBSCRIPTION %s CONNECTION '%s' PUBLICATION p WITH (enabled = true, copy_data = false, slot_name = '%s', create_slot = false)",
		slotName, conn, slotName))

	// 4 KiB payload — well past the upstream TOAST threshold (~2 KB)
	// so the value goes out-of-line and pgoutput will short-circuit
	// the column on any update that doesn't touch it.
	psc.Publisher.Exec(t, "INSERT INTO public.t VALUES (1, 'alpha', repeat('X', 4096))")
	// UPDATE only the `name` column. Replication wire shape:
	// `'U' relOid 'N' (t,t,u)`. The apply worker must read the
	// existing heap row, copy `payload` from it into the new row,
	// then delete+insert.
	psc.Publisher.Exec(t, "UPDATE public.t SET name = 'alpha-updated' WHERE id = 1")

	// Fresh `database/sql` sessions throughout; the IndexScan path
	// is exercised through the PK predicate.
	psc.WaitForRow(t, "public.t", "id = 1", 1, 60*time.Second)
	psc.WaitForRow(t, "public.t", "id = 1 AND name = 'alpha-updated'", 1, 30*time.Second)
	// TOAST preservation: length and first-character checks both
	// fail-fast on the NULL-fill bug a missing 'u' code path would
	// introduce, and on the wrong-cell bug a misaligned mask would.
	psc.WaitForRow(t, "public.t", "id = 1 AND length(payload) = 4096", 1, 30*time.Second)
	psc.WaitForRow(t, "public.t", "id = 1 AND substr(payload, 1, 1) = 'X'", 1, 30*time.Second)
}


// TestPort_PgoutputInteropPGToGoopgMultiDMLXact pins rung 6 of
// M0103-0007: PG-publisher → goopg-subscriber multi-DML single
// transaction. A single explicit BEGIN/...COMMIT block on the publisher
// produces one pgoutput xact (one B, multiple I/U/D, one C). The apply
// worker opens one TxnMgr transaction at B, replays every change
// against it, and commits at C. UPDATE/DELETE statements inside the
// xact must see rows inserted earlier in the same xact — that's the
// "own-xact write visibility" property carried by
// mvcc.TupleVisibleSubxact's currentXID short-circuit.
//
// Design doc: docs/design/0103-0029-m0103-0007-rung-6-pg-to-goopg-multi-dml-xact.md.
func TestPort_PgoutputInteropPGToGoopgMultiDMLXact(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping in short mode")
	}

	repo := repoRoot(t)
	pgcluster.Available(t, filepath.Join(repo, "postgres", "local_install", "bin"))

	baseDir := filepath.Join(repo, "tmp", "pg2g-mxact")
	_ = os.RemoveAll(baseDir)

	slotName := "pg2g_mxact"
	psc := pubsubcluster.NewMixed(t, "pg2g_mxact", pubsubcluster.Options{
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

	psc.Publisher.Exec(t, "CREATE TABLE public.t (id int PRIMARY KEY, v text)")
	psc.Subscriber.Exec(t, "CREATE TABLE public.t (id int PRIMARY KEY, v text)")
	psc.CreatePublication(t, "t")

	psc.Publisher.Exec(t, fmt.Sprintf(
		"SELECT pg_create_logical_replication_slot('%s', 'pgoutput')", slotName))
	conn := psc.Publisher.Conninfo(slotName)
	psc.Subscriber.Exec(t, fmt.Sprintf(
		"CREATE SUBSCRIPTION %s CONNECTION '%s' PUBLICATION p WITH (enabled = true, copy_data = false, slot_name = '%s', create_slot = false)",
		slotName, conn, slotName))

	// One explicit BEGIN/COMMIT block — psql -c sends the whole string
	// as one simple-query, so PostgreSQL groups every statement into a
	// single transaction. pgoutput emits one B / I / I / I / U / D / C
	// sequence anchored at one XID; the apply worker replays them
	// inside one TxnMgr transaction. The UPDATE on id=2 and the DELETE
	// on id=3 must locate rows the earlier INSERTs wrote within the
	// same xact (xmin == currentTx.XID), exercising
	// mvcc.TupleVisibleSubxact's own-xact short-circuit.
	psc.Publisher.Exec(t, `
BEGIN;
INSERT INTO public.t VALUES (1, 'one');
INSERT INTO public.t VALUES (2, 'two');
INSERT INTO public.t VALUES (3, 'three');
UPDATE public.t SET v = 'two-prime' WHERE id = 2;
DELETE FROM public.t WHERE id = 3;
COMMIT;`)

	// All assertions use fresh database/sql sessions (WaitForRow opens
	// a new conn each call), so each query traverses the goopg PK
	// IndexScan path against committed state.

	// Total surviving row count fail-fasts the "DELETE didn't see own
	// INSERT" regression: a no-op delete would leave count=3.
	psc.WaitForRow(t, "public.t", "1=1", 2, 60*time.Second)

	// Untouched INSERT survives unchanged.
	psc.WaitForRow(t, "public.t", "id = 1 AND v = 'one'", 1, 30*time.Second)

	// INSERT-then-UPDATE within same xact: UPDATE must locate the
	// previously-inserted heap tuple via own-xact write visibility,
	// xmax it, and install the new tuple with v='two-prime'.
	psc.WaitForRow(t, "public.t", "id = 2 AND v = 'two-prime'", 1, 30*time.Second)

	// INSERT-then-DELETE within same xact: DELETE must locate the
	// previously-inserted heap tuple via own-xact write visibility
	// and xmax it. Post-commit, both xmin and xmax are committed →
	// invisible. The PK IndexScan re-fetches the heap and MVCC drops
	// the tuple, matching the rung-1/rung-2 "IndexScan tolerates
	// orphan PK entries" caveat.
	psc.WaitForRow(t, "public.t", "id = 3", 0, 30*time.Second)
}

// TestPort_PgoutputInteropPGToGoopgSavepointXact pins rung 7 of
// M0103-0007: PG-publisher → goopg-subscriber SAVEPOINT subxacts at
// proto_version=1. Subxact boundaries are NOT streamed at v1; the
// publisher's reorder buffer filters rolled-back subxact rows before
// emission, so the subscriber sees only the committed net effect of
// the top-level transaction. This rung verifies that contract end-to-
// end: a SAVEPOINT-heavy workload with both ROLLBACK TO and RELEASE
// frames produces exactly the expected post-commit state.
//
// Design doc: docs/design/0103-0030-m0103-0007-rung-7-pg-to-goopg-savepoint-xact.md.
func TestPort_PgoutputInteropPGToGoopgSavepointXact(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping in short mode")
	}

	repo := repoRoot(t)
	pgcluster.Available(t, filepath.Join(repo, "postgres", "local_install", "bin"))

	baseDir := filepath.Join(repo, "tmp", "pg2g-svp")
	_ = os.RemoveAll(baseDir)

	slotName := "pg2g_svp"
	psc := pubsubcluster.NewMixed(t, "pg2g_svp", pubsubcluster.Options{
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

	psc.Publisher.Exec(t, "CREATE TABLE public.t (id int PRIMARY KEY, v text)")
	psc.Subscriber.Exec(t, "CREATE TABLE public.t (id int PRIMARY KEY, v text)")
	psc.CreatePublication(t, "t")

	psc.Publisher.Exec(t, fmt.Sprintf(
		"SELECT pg_create_logical_replication_slot('%s', 'pgoutput')", slotName))
	conn := psc.Publisher.Conninfo(slotName)
	psc.Subscriber.Exec(t, fmt.Sprintf(
		"CREATE SUBSCRIPTION %s CONNECTION '%s' PUBLICATION p WITH (enabled = true, copy_data = false, slot_name = '%s', create_slot = false)",
		slotName, conn, slotName))

	// One top-level transaction containing three SAVEPOINTs:
	//   - s1: INSERT(2,'two-rolled') + UPDATE id=1 → rolled back
	//   - s2: INSERT(3,'three') → released (merged into parent)
	//   - s3: DELETE id=3 → rolled back (restores id=3)
	// At proto_version=1 the publisher's reorder buffer drops rolled-
	// back subxact rows before emission; the wire carries exactly:
	//   B, I(1,'one'), I(3,'three'), I(4,'four'), C.
	// The apply worker replays them inside a single TxnMgr xact.
	psc.Publisher.Exec(t, `
BEGIN;
INSERT INTO public.t VALUES (1, 'one');
SAVEPOINT s1;
INSERT INTO public.t VALUES (2, 'two-rolled');
UPDATE public.t SET v = 'one-rolled' WHERE id = 1;
ROLLBACK TO SAVEPOINT s1;
SAVEPOINT s2;
INSERT INTO public.t VALUES (3, 'three');
RELEASE SAVEPOINT s2;
INSERT INTO public.t VALUES (4, 'four');
SAVEPOINT s3;
DELETE FROM public.t WHERE id = 3;
ROLLBACK TO SAVEPOINT s3;
COMMIT;`)

	// All assertions use fresh database/sql sessions (WaitForRow opens
	// a new conn each call), so each query traverses the goopg PK
	// IndexScan path against committed state.

	// Total surviving row count fail-fasts the "publisher leaked
	// rolled-back inserts" regression (would yield 4 or 5) or the
	// "apply re-applied a rolled-back DELETE" regression (would
	// yield 2).
	psc.WaitForRow(t, "public.t", "1=1", 3, 60*time.Second)

	// Top-level INSERT survived; the s1 UPDATE that would have rewritten
	// v='one-rolled' was rolled back.
	psc.WaitForRow(t, "public.t", "id = 1 AND v = 'one'", 1, 30*time.Second)

	// s1 INSERT was rolled back — id=2 must not exist.
	psc.WaitForRow(t, "public.t", "id = 2", 0, 30*time.Second)

	// s2 RELEASE merged the INSERT into the parent xact; s3 ROLLBACK TO
	// restored the row by aborting the DELETE. Either failure mode
	// (release leaked → no row; rollback failed → 0 visible) shows up
	// here.
	psc.WaitForRow(t, "public.t", "id = 3 AND v = 'three'", 1, 30*time.Second)

	// Top-level INSERT after the s2 RELEASE survives unchanged. Catches
	// the post-RELEASE accidentally tying to an aborted subxact at the
	// publisher.
	psc.WaitForRow(t, "public.t", "id = 4 AND v = 'four'", 1, 30*time.Second)
}


// TestPort_PgoutputInteropPGToGoopgMultiTable pins rung 8 of
// M0103-0007: PG-publisher → goopg-subscriber multi-table interleaved
// DML. All prior rungs (1–7) used a single published table; this rung
// publishes two tables with different column shapes and verifies that
// interleaved INSERT/UPDATE/DELETE against both inside one top-level
// transaction lands at the correct per-table state on the subscriber.
//
// The load-bearing property is the multi-relation dispatch contract:
// the apply worker's relation cache must hold both Relation messages
// live across the B...C block, every change must route to the matching
// catalog.Table on the subscriber, and per-table primary-key index
// maintenance (maintainUniqueIndexesForInsert) must run against the
// right table so subsequent fresh-session PK IndexScans find each row.
//
// Cross-relation dispatch leaks, stale-relation overwrites after the
// second 'R' frame, and column-index drift between the wire tuple and
// the subscriber's catalog.Table.Columns are all caught by the per-
// table count + identity assertions below.
//
// Design doc: docs/design/0103-0031-m0103-0007-rung-8-pg-to-goopg-multi-table.md.
func TestPort_PgoutputInteropPGToGoopgMultiTable(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping in short mode")
	}

	repo := repoRoot(t)
	pgcluster.Available(t, filepath.Join(repo, "postgres", "local_install", "bin"))

	baseDir := filepath.Join(repo, "tmp", "pg2g-multi")
	_ = os.RemoveAll(baseDir)

	slotName := "pg2g_multi"
	psc := pubsubcluster.NewMixed(t, "pg2g_multi", pubsubcluster.Options{
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

	// Two tables with deliberately different column shapes:
	//   users:  2 cols (id int PK, name text)
	//   orders: 3 cols (id int PK, user_id int, amount int)
	// Column-index drift between the wire tuple and the subscriber's
	// catalog.Table.Columns would either surface as a parse error
	// (text into int4) or as the per-row identity assertions failing.
	psc.Publisher.Exec(t, "CREATE TABLE public.users  (id int PRIMARY KEY, name text)")
	psc.Publisher.Exec(t, "CREATE TABLE public.orders (id int PRIMARY KEY, user_id int, amount int)")
	psc.Subscriber.Exec(t, "CREATE TABLE public.users  (id int PRIMARY KEY, name text)")
	psc.Subscriber.Exec(t, "CREATE TABLE public.orders (id int PRIMARY KEY, user_id int, amount int)")
	psc.CreatePublication(t, "users", "orders")

	psc.Publisher.Exec(t, fmt.Sprintf(
		"SELECT pg_create_logical_replication_slot('%s', 'pgoutput')", slotName))
	conn := psc.Publisher.Conninfo(slotName)
	psc.Subscriber.Exec(t, fmt.Sprintf(
		"CREATE SUBSCRIPTION %s CONNECTION '%s' PUBLICATION p WITH (enabled = true, copy_data = false, slot_name = '%s', create_slot = false)",
		slotName, conn, slotName))

	// Single top-level transaction interleaving DML against both
	// tables. The publisher's reorder buffer emits frames in arrival
	// order; on the wire this becomes:
	//   B, R(users), I(users), R(orders), I(orders), I(users),
	//   I(orders), I(orders), U(orders), U(users), D(users),
	//   D(orders), C.
	// The apply worker's relation cache must keep both R entries
	// live and dispatch each change to the matching catalog.Table.
	psc.Publisher.Exec(t, `
BEGIN;
INSERT INTO public.users  VALUES (1, 'alice');
INSERT INTO public.orders VALUES (10, 1, 100);
INSERT INTO public.users  VALUES (2, 'bob');
INSERT INTO public.orders VALUES (11, 2, 200);
INSERT INTO public.orders VALUES (12, 1, 50);
UPDATE public.orders SET amount = 99 WHERE id = 10;
UPDATE public.users  SET name = 'alice-updated' WHERE id = 1;
DELETE FROM public.users  WHERE id = 2;
DELETE FROM public.orders WHERE id = 11;
COMMIT;`)

	// Second autocommit phase: verify post-xact multi-relation routing
	// still works (each statement is its own B...C block on the wire).
	psc.Publisher.Exec(t, "INSERT INTO public.users  VALUES (3, 'carol')")
	psc.Publisher.Exec(t, "INSERT INTO public.orders VALUES (13, 3, 75)")

	// All assertions use fresh database/sql sessions (WaitForRow opens
	// a new conn per call) so each query traverses the goopg PK
	// IndexScan path against committed state.

	// Per-table count fail-fasts a cross-relation dispatch leak:
	//   - users at 3 would mean 'bob' (id=2) wasn't deleted, or an
	//     orders row was misrouted into users.
	//   - users at 1 would mean 'carol' (id=3) was routed to orders
	//     instead of users.
	psc.WaitForRow(t, "public.users", "1=1", 2, 60*time.Second)
	psc.WaitForRow(t, "public.orders", "1=1", 3, 60*time.Second)

	// users state: top-level INSERT + same-xact UPDATE both survive,
	// the matching DELETE of id=2 fired, and the post-xact id=3
	// autocommit INSERT lands. Identity check on id=1 catches:
	//   - missing UPDATE (would leave name='alice')
	//   - wrong-relation UPDATE (would leave name='alice')
	psc.WaitForRow(t, "public.users", "id = 1 AND name = 'alice-updated'", 1, 30*time.Second)
	psc.WaitForRow(t, "public.users", "id = 2", 0, 30*time.Second)
	psc.WaitForRow(t, "public.users", "id = 3 AND name = 'carol'", 1, 30*time.Second)

	// orders state: id=10 INSERT followed by same-xact UPDATE to
	// amount=99; id=12 untouched in xact at amount=50; id=13 added
	// post-xact at amount=75. id=11 deleted. Identity check on id=10
	// fail-fasts an UPDATE-routing bug — if the orders UPDATE leaked
	// into users (or vice versa), id=10's amount would stay 100.
	psc.WaitForRow(t, "public.orders", "id = 10 AND user_id = 1 AND amount = 99", 1, 30*time.Second)
	psc.WaitForRow(t, "public.orders", "id = 11", 0, 30*time.Second)
	psc.WaitForRow(t, "public.orders", "id = 12 AND user_id = 1 AND amount = 50", 1, 30*time.Second)
	psc.WaitForRow(t, "public.orders", "id = 13 AND user_id = 3 AND amount = 75", 1, 30*time.Second)
}


// TestPort_PgoutputInteropPGToGoopgTruncate pins M0103-0007 rung 9:
// the apply worker handles pgoutput 'T' (TRUNCATE) frames end-to-end
// against a real upstream PG publisher. Before rung 9 the apply
// worker dispatched only on B/R/I/D/U/C, so a publisher TRUNCATE
// crashed the slot with `unsupported pgoutput kind "T"`. Rung 9 adds
// the decoder case + apply-side `applyTruncate` that stamps xmax on
// every visible tuple in each named relation, transactional with the
// surrounding pgoutput xact.
//
// Workload — three phases against a published `public.t (id int
// PRIMARY KEY, v text)`:
//   1. INSERT (1,'a'), (2,'b'), (3,'c')
//   2. TRUNCATE TABLE public.t
//   3. INSERT (10,'x'), (11,'y')
//
// Expected subscriber state after apply convergence: exactly two
// rows, `(10,'x')` and `(11,'y')`. Each assertion goes through a
// fresh `database/sql` session via `psc.WaitForRow`, so the goopg
// PK IndexScan path is exercised (TRUNCATE must also leave the
// btree consistent with the heap — IndexScan tolerates orphaned
// entries via heap re-fetch + MVCC dead-tuple filtering, so a stale
// index entry pointing at a now-dead tuple does not produce a
// false positive). Fail-fasts:
//   - `count(*)=2` catches TRUNCATE no-op (would yield 3 or 5).
//   - `WHERE id IN (1,2,3)` returning 0 catches the inverse
//     (TRUNCATE that fired but somehow left pre-truncate rows
//     visible).
//   - per-id identity catches TRUNCATE wiping post-truncate inserts.
func TestPort_PgoutputInteropPGToGoopgTruncate(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping in short mode")
	}

	repo := repoRoot(t)
	pgcluster.Available(t, filepath.Join(repo, "postgres", "local_install", "bin"))

	baseDir := filepath.Join(repo, "tmp", "pg2g-truncate")
	_ = os.RemoveAll(baseDir)

	slotName := "pg2g_truncate"
	psc := pubsubcluster.NewMixed(t, "pg2g_truncate", pubsubcluster.Options{
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

	psc.Publisher.Exec(t, "CREATE TABLE public.t (id int PRIMARY KEY, v text)")
	psc.Subscriber.Exec(t, "CREATE TABLE public.t (id int PRIMARY KEY, v text)")
	psc.CreatePublication(t, "t")

	psc.Publisher.Exec(t, fmt.Sprintf(
		"SELECT pg_create_logical_replication_slot('%s', 'pgoutput')", slotName))
	conn := psc.Publisher.Conninfo(slotName)
	psc.Subscriber.Exec(t, fmt.Sprintf(
		"CREATE SUBSCRIPTION %s CONNECTION '%s' PUBLICATION p WITH (enabled = true, copy_data = false, slot_name = '%s', create_slot = false)",
		slotName, conn, slotName))

	// Phase 1 — three pre-truncate INSERTs. Each is its own
	// autocommit B...C block on the wire.
	psc.Publisher.Exec(t, "INSERT INTO public.t VALUES (1, 'a')")
	psc.Publisher.Exec(t, "INSERT INTO public.t VALUES (2, 'b')")
	psc.Publisher.Exec(t, "INSERT INTO public.t VALUES (3, 'c')")

	// Wait for the three rows before issuing the TRUNCATE so the
	// assertion below can tell apart "TRUNCATE didn't fire" from
	// "TRUNCATE ran but pre-truncate INSERTs hadn't applied yet".
	psc.WaitForRow(t, "public.t", "1=1", 3, 30*time.Second)

	// Phase 2 — TRUNCATE. Publisher emits one B...T...C block.
	psc.Publisher.Exec(t, "TRUNCATE TABLE public.t")

	// Phase 3 — two post-truncate INSERTs.
	psc.Publisher.Exec(t, "INSERT INTO public.t VALUES (10, 'x')")
	psc.Publisher.Exec(t, "INSERT INTO public.t VALUES (11, 'y')")

	// Apply convergence — subscriber must see exactly the two
	// post-truncate rows. count(*)=2 fail-fasts TRUNCATE no-op
	// (3 or 5 rows) and silent data loss (0 or 1 rows).
	psc.WaitForRow(t, "public.t", "1=1", 2, 60*time.Second)
	psc.WaitForRow(t, "public.t", "id IN (1, 2, 3)", 0, 30*time.Second)
	psc.WaitForRow(t, "public.t", "id = 10 AND v = 'x'", 1, 30*time.Second)
	psc.WaitForRow(t, "public.t", "id = 11 AND v = 'y'", 1, 30*time.Second)
}


// TestPort_PgoutputInteropPGToGoopgColumnOrderMismatch pins M0103-0007
// rung 10: the apply worker must map remote pgoutput columns onto the
// local table's columns BY NAME rather than by physical ordinal. The
// publisher declares `(id int, v text)` and the subscriber declares
// `(v text, id int)` — same logical columns, swapped order. Without the
// rung-10 fix, the wire tuple [id=1, v='alice'] would be written into
// the subscriber positionally, landing "alice" into the int4 column
// (parse error) or `1` into the text column (silent corruption).
// Workload exercises INSERT + UPDATE (no-key-touch synthesised
// primaryKeyOnlyRow path) + DELETE so all three decode call sites get
// covered. Fresh database/sql sessions assert via the PK IndexScan
// path on the subscriber.
func TestPort_PgoutputInteropPGToGoopgColumnOrderMismatch(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping in short mode")
	}

	repo := repoRoot(t)
	pgcluster.Available(t, filepath.Join(repo, "postgres", "local_install", "bin"))

	baseDir := filepath.Join(repo, "tmp", "pg2g-colorder")
	_ = os.RemoveAll(baseDir)

	slotName := "pg2g_colorder"
	psc := pubsubcluster.NewMixed(t, "pg2g_colorder", pubsubcluster.Options{
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

	// Publisher: id BEFORE v. Subscriber: v BEFORE id. Same column
	// names, types, and PK semantics — only the physical ordinal
	// differs. Type sniffing alone would not catch a wrong mapping
	// because both columns are scalar primitives; the name-based
	// remap is the only thing that can produce a correct result.
	psc.Publisher.Exec(t, "CREATE TABLE public.t (id int PRIMARY KEY, v text)")
	psc.Subscriber.Exec(t, "CREATE TABLE public.t (v text, id int PRIMARY KEY)")
	psc.CreatePublication(t, "t")

	psc.Publisher.Exec(t, fmt.Sprintf(
		"SELECT pg_create_logical_replication_slot('%s', 'pgoutput')", slotName))
	conn := psc.Publisher.Conninfo(slotName)
	psc.Subscriber.Exec(t, fmt.Sprintf(
		"CREATE SUBSCRIPTION %s CONNECTION '%s' PUBLICATION p WITH (enabled = true, copy_data = false, slot_name = '%s', create_slot = false)",
		slotName, conn, slotName))

	// Phase 1 — INSERT goes through applyInsert →
	// decodePgoutputTupleAsRow → maintainUniqueIndexesForInsert.
	// Wrong mapping would either fail with a parse error at
	// publisher decode time (good — at least loud) or install
	// row (v='1', id=NULL) which violates NOT NULL on the PK.
	psc.Publisher.Exec(t, "INSERT INTO public.t VALUES (1, 'alice')")
	psc.Publisher.Exec(t, "INSERT INTO public.t VALUES (2, 'bob')")

	// Phase 2 — no-key-touched UPDATE flexes the
	// primaryKeyOnlyRow + applyUpdateByKey path. Without the
	// rung-10 remap, primaryKeyOnlyRow's PK-by-name lookup
	// already works but `newRow` (from decodePgoutputTupleAsRow)
	// would carry data in the wrong slot, so the post-update
	// row would have `v` swapped with `id` (effectively
	// scrambling the visible state).
	psc.Publisher.Exec(t, "UPDATE public.t SET v = 'alice-updated' WHERE id = 1")

	// Phase 3 — DELETE flexes applyDelete →
	// decodePgoutputTupleAsRow on the OldTuple (under REPLICA
	// IDENTITY DEFAULT the OldTuple carries only the PK column
	// — single int, single name "id"). Wrong mapping would
	// drop the wrong row or none at all.
	psc.Publisher.Exec(t, "DELETE FROM public.t WHERE id = 2")

	// Subscriber assertions via fresh database/sql sessions through
	// goopg's PK IndexScan path. Each fail-fasts a distinct
	// regression: count(*)=1 catches "INSERT silently dropped" or
	// "DELETE didn't fire"; the identity assertion catches
	// "INSERT installed but with id and v swapped", which would
	// show up as either no match (id is now NULL or string) or
	// a match with v='1' (the int value landed in text).
	psc.WaitForRow(t, "public.t", "1=1", 1, 60*time.Second)
	psc.WaitForRow(t, "public.t", "id = 1 AND v = 'alice-updated'", 1, 30*time.Second)
	psc.WaitForRow(t, "public.t", "id = 2", 0, 30*time.Second)
}


// TestPort_PgoutputInteropPGToGoopgSubscriberExtraColumn pins M0103-0007
// rung 11: the goopg subscriber declares an extra `note` column the PG
// publisher never describes in its Relation message. On INSERT the column
// stays NULL on the subscriber (existing behaviour); the subscriber then
// directly UPDATEs the row to populate `note`. A subsequent UPDATE on the
// publisher must NOT erase the subscriber-only value — the apply worker
// has to fill the missing slot from the matched heap row before the
// delete+insert that backs the logical UPDATE.
//
// Design doc: docs/design/0103-0034-m0103-0007-rung-11-pg-to-goopg-subscriber-extra-column.md.
func TestPort_PgoutputInteropPGToGoopgSubscriberExtraColumn(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping in short mode")
	}

	repo := repoRoot(t)
	pgcluster.Available(t, filepath.Join(repo, "postgres", "local_install", "bin"))

	baseDir := filepath.Join(repo, "tmp", "pg2g-extracol")
	_ = os.RemoveAll(baseDir)

	slotName := "pg2g_extracol"
	psc := pubsubcluster.NewMixed(t, "pg2g_extracol", pubsubcluster.Options{
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

	// Publisher has (id, v); subscriber adds a nullable `note` column.
	// The subscriber-only column must survive replicated UPDATEs.
	psc.Publisher.Exec(t, "CREATE TABLE public.t (id int PRIMARY KEY, v text)")
	psc.Subscriber.Exec(t, "CREATE TABLE public.t (id int PRIMARY KEY, v text, note text)")
	psc.CreatePublication(t, "t")

	psc.Publisher.Exec(t, fmt.Sprintf(
		"SELECT pg_create_logical_replication_slot('%s', 'pgoutput')", slotName))
	conn := psc.Publisher.Conninfo(slotName)
	psc.Subscriber.Exec(t, fmt.Sprintf(
		"CREATE SUBSCRIPTION %s CONNECTION '%s' PUBLICATION p WITH (enabled = true, copy_data = false, slot_name = '%s', create_slot = false)",
		slotName, conn, slotName))

	// Phase 1: publisher INSERT replicates with note=NULL.
	psc.Publisher.Exec(t, "INSERT INTO public.t VALUES (1, 'hello')")
	psc.WaitForRow(t, "public.t", "id = 1 AND v = 'hello' AND note IS NULL", 1, 60*time.Second)

	// Phase 2: subscriber writes the subscriber-only value directly.
	psc.Subscriber.Exec(t, "UPDATE public.t SET note = 'kept' WHERE id = 1")
	psc.WaitForRow(t, "public.t", "id = 1 AND note = 'kept'", 1, 10*time.Second)

	// Phase 3 — the load-bearing check. Publisher UPDATE touches only
	// `v`; under the rung-11 fix the apply worker fills `note` from
	// the matched heap row before the delete+insert that backs the
	// logical UPDATE. Without the fix, `note` would be NULL'd because
	// the publisher's Relation message never mentions it and the
	// decoder leaves the local slot at NullDatum.
	psc.Publisher.Exec(t, "UPDATE public.t SET v = 'updated' WHERE id = 1")
	psc.WaitForRow(t, "public.t", "id = 1 AND v = 'updated' AND note = 'kept'", 1, 30*time.Second)

	// Negative pin: no spurious second row, and the NULL-note state
	// did not survive (which would mean the UPDATE didn't fire).
	psc.WaitForRow(t, "public.t", "1=1", 1, 5*time.Second)
	psc.WaitForRow(t, "public.t", "note IS NULL", 0, 5*time.Second)
}

// TestPort_PgoutputInteropPGToGoopgReplicaIdentityUsingIndex pins
// rung 12 of M0103-0007: the apply-worker's no-OldTuple key-synthesis
// path when REPLICA IDENTITY USING INDEX names a NON-PRIMARY unique
// index as the row-locator. Under DEFAULT the rung-2 `primaryKeyOnlyRow`
// fallback resolves correctly because the identity columns ARE the PK;
// under USING INDEX the publisher and subscriber may not share a PK at
// all, so the apply worker must read the per-column identity flags
// (LOGICALREP_IS_REPLICA_IDENTITY, bit 0 of DecodedAttr.Flags) from the
// Relation message instead.
//
// Workload: publisher has table `t (k int, v text)` with no PK but a
// UNIQUE index `t_k_uniq (k)`; `ALTER TABLE t REPLICA IDENTITY USING
// INDEX t_k_uniq`. Subscriber declares the same columns with no
// constraints. Three INSERTs, one UPDATE that does NOT touch `k` (so
// pgoutput omits OldTuple — the rung-12 path), one DELETE keyed on `k`.
//
// Assertions through fresh `database/sql` sessions verify:
//   - All three inserts replicate.
//   - The UPDATE locates row k=2 by the identity-column key and updates v.
//   - The DELETE removes the right row.
//
// Without rung 12 the UPDATE would silently drop (the rung-2 fallback
// returns nil because the subscriber has no PK).
//
// Design doc: docs/design/0103-0035-m0103-0007-rung-12-pg-to-goopg-replica-identity-index.md.
func TestPort_PgoutputInteropPGToGoopgReplicaIdentityUsingIndex(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping in short mode")
	}

	repo := repoRoot(t)
	pgcluster.Available(t, filepath.Join(repo, "postgres", "local_install", "bin"))

	baseDir := filepath.Join(repo, "tmp", "pg2g-rid-idx")
	_ = os.RemoveAll(baseDir)

	slotName := "pg2g_rid_idx"
	psc := pubsubcluster.NewMixed(t, "pg2g_ridx", pubsubcluster.Options{
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

	ctx, cancel := context.WithTimeout(context.Background(), 90 * time.Second)
	defer cancel()
	if err := psc.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Publisher: no PRIMARY KEY; a unique index on `k` only; REPLICA
	// IDENTITY pointed at that non-PK index. Subscriber: same columns
	// with NO constraints at all — exercises the case where the
	// subscriber can't fall back to a local PK.
	// `k` must be NOT NULL — upstream rejects USING INDEX on an index
	// whose columns are nullable (the unique guarantee doesn't include
	// NULL keys, so it can't identify a row).
	psc.Publisher.Exec(t, "CREATE TABLE public.t (k int NOT NULL, v text)")
	psc.Publisher.Exec(t, "CREATE UNIQUE INDEX t_k_uniq ON public.t (k)")
	psc.Publisher.Exec(t, "ALTER TABLE public.t REPLICA IDENTITY USING INDEX t_k_uniq")
	psc.Subscriber.Exec(t, "CREATE TABLE public.t (k int, v text)")
	psc.CreatePublication(t, "t")

	psc.Publisher.Exec(t, fmt.Sprintf(
		"SELECT pg_create_logical_replication_slot('%s', 'pgoutput')", slotName))
	conn := psc.Publisher.Conninfo(slotName)
	psc.Subscriber.Exec(t, fmt.Sprintf(
		"CREATE SUBSCRIPTION %s CONNECTION '%s' PUBLICATION p WITH (enabled = true, copy_data = false, slot_name = '%s', create_slot = false)",
		slotName, conn, slotName))

	psc.Publisher.Exec(t, "INSERT INTO public.t VALUES (1, 'a')")
	psc.Publisher.Exec(t, "INSERT INTO public.t VALUES (2, 'b')")
	psc.Publisher.Exec(t, "INSERT INTO public.t VALUES (3, 'c')")
	// UPDATE does NOT touch identity column `k`. pgoutput therefore
	// omits OldTuple; the apply worker must synthesise the key from
	// newRow using the Relation-message identity flag on `k`.
	psc.Publisher.Exec(t, "UPDATE public.t SET v = 'bb' WHERE k = 2")
	// DELETE keyed on identity column; pgoutput emits 'K' + key-only
	// old tuple, which the existing applyDelete path handles
	// regardless of which index backs the identity.
	psc.Publisher.Exec(t, "DELETE FROM public.t WHERE k = 1")

	// Fresh `database/sql` sessions for every assertion. Subscriber
	// has no PK and no unique index, so every probe goes through
	// SeqScan with predicate equality on `k`.
	psc.WaitForRow(t, "public.t", "1=1", 2, 60*time.Second)
	psc.WaitForRow(t, "public.t", "k = 2 AND v = 'bb'", 1, 30*time.Second)
	psc.WaitForRow(t, "public.t", "k = 1", 0, 30*time.Second)
	psc.WaitForRow(t, "public.t", "k = 3 AND v = 'c'", 1, 30*time.Second)
	// Negative pin: the UPDATE didn't silently drop into a stale
	// 'b' row (would happen if rung-12 wasn't wired and applyUpdate
	// fell back to primaryKeyOnlyRow → nil → silent skip).
	psc.WaitForRow(t, "public.t", "k = 2 AND v = 'b'", 0, 5*time.Second)
}


// TestPort_PgoutputInteropPGToGoopgSubscriberExtraDefault pins M0103-0007
// rung 13: when the goopg subscriber declares an extra column the PG
// publisher never mentions in its Relation message AND that column has a
// CREATE TABLE DEFAULT, the apply worker must evaluate the DEFAULT at
// INSERT time so the subscriber row carries the configured default value.
// Mirrors upstream's slot_fill_defaults() in
// src/backend/replication/logical/worker.c.
//
// Workload: publisher INSERTs (1,'hello') and (2,'world'); subscriber
// schema has an extra `note text DEFAULT 'auto'`. The replicated rows
// must arrive on goopg with note='auto', not note=NULL.
//
// Before rung 13: subscriber rows had note=NULL (silent NULL-fill);
// applications relying on DEFAULT for subscriber-side bookkeeping
// columns would see all NULLs. After rung 13: the DefaultExpr captured
// at CREATE TABLE time is evaluated through evalGenExpr and lands in
// the heap.
//
// Negative pin: a separate column without a DEFAULT stays NULL — proves
// the helper only fires when DefaultExpr is non-nil and doesn't blanket
// every missing slot with a placeholder.
//
// Design doc:
// docs/design/0103-0036-m0103-0007-rung-13-pg-to-goopg-subscriber-extra-default.md.
func TestPort_PgoutputInteropPGToGoopgSubscriberExtraDefault(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping in short mode")
	}

	repo := repoRoot(t)
	pgcluster.Available(t, filepath.Join(repo, "postgres", "local_install", "bin"))

	baseDir := filepath.Join(repo, "tmp", "pg2g-extradefault")
	_ = os.RemoveAll(baseDir)

	slotName := "pg2g_extradefault"
	psc := pubsubcluster.NewMixed(t, "pg2g_extradefault", pubsubcluster.Options{
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

	// Publisher schema: (id, v). Subscriber schema adds two extra columns:
	//   note text DEFAULT 'auto'  — must be filled to 'auto' on apply
	//   bare text                 — no DEFAULT, must stay NULL on apply
	psc.Publisher.Exec(t, "CREATE TABLE public.t (id int PRIMARY KEY, v text)")
	psc.Subscriber.Exec(t,
		"CREATE TABLE public.t (id int PRIMARY KEY, v text, note text DEFAULT 'auto', bare text)")
	psc.CreatePublication(t, "t")

	psc.Publisher.Exec(t, fmt.Sprintf(
		"SELECT pg_create_logical_replication_slot('%s', 'pgoutput')", slotName))
	conn := psc.Publisher.Conninfo(slotName)
	psc.Subscriber.Exec(t, fmt.Sprintf(
		"CREATE SUBSCRIPTION %s CONNECTION '%s' PUBLICATION p WITH (enabled = true, copy_data = false, slot_name = '%s', create_slot = false)",
		slotName, conn, slotName))

	psc.Publisher.Exec(t, "INSERT INTO public.t VALUES (1, 'hello')")
	psc.Publisher.Exec(t, "INSERT INTO public.t VALUES (2, 'world')")

	// Both rows must arrive with note='auto'. Without the rung-13 fix,
	// note would be NULL and the WaitForRow would time out at 30 s.
	psc.WaitForRow(t, "public.t", "id = 1 AND v = 'hello' AND note = 'auto'", 1, 60*time.Second)
	psc.WaitForRow(t, "public.t", "id = 2 AND v = 'world' AND note = 'auto'", 1, 30*time.Second)

	// Negative pin: bare has no DEFAULT, stays NULL. Catches a regression
	// where applyDefaultsForMissing started overwriting every missing slot
	// instead of only those whose column carries DefaultExpr.
	psc.WaitForRow(t, "public.t", "bare IS NULL", 2, 5*time.Second)

	// Row-count pin: exactly the two publisher rows, no duplicates or
	// silently-NULL'd extras.
	psc.WaitForRow(t, "public.t", "1=1", 2, 5*time.Second)
	psc.WaitForRow(t, "public.t", "note IS NULL", 0, 5*time.Second)
}

// TestPort_PgoutputInteropPGToGoopgPgbenchInsert pins M0103-0007
// rung 20 (`docs/design/0103-0043-m0103-0007-rung-20-pgbench-pg-to-goopg.md`):
// the upstream `pgbench` binary drives an INSERT-only workload against
// the PG publisher and every row must surface on the goopg subscriber
// through the pgoutput pipeline. Replaces the rungs 1–19 `psql -c`
// loops with a `-c N -t M` pgbench run — the same workload driver the
// Scenario A DoD calls for (`pgbench -i -s 1 && pgbench -c 2 -T 180`),
// scoped here to an INSERT-only custom script so the rung 1's apply-
// worker INSERT path is what is being measured, not pgbench's
// `tpcb-like` UPDATE schedule (those are pinned across rungs 2–11).
//
// Workload: 2 clients × 25 transactions × 1 INSERT each (50 rows
// total). The script uses pgbench's `\set rid random(1, 10^9)` plus
// `:client_id` (built-in 0..N-1) so each INSERT carries a unique PK
// with negligible collision probability (P(collision) < 10⁻⁹ × 50).
//
// Fresh `database/sql` per assertion through `WaitForRow` exercises
// the same dispatcher MVCC view rungs 1–19 use. Three assertions
// fail-fast distinct regressions: total count != 50 catches a
// workload that fired but lost rows in pgoutput; per-client-id counts
// of 0 catches a pgbench launch that pinned every txn to client_id=0
// (or only one client connected); the row-count pin against
// `client_id IN (0,1)` catches stray rows from a leaked previous test.
func TestPort_PgoutputInteropPGToGoopgPgbenchInsert(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping in short mode")
	}

	repo := repoRoot(t)
	pgcluster.Available(t, filepath.Join(repo, "postgres", "local_install", "bin"))

	// Refuse to run without the in-tree pgbench. The standard PATH
	// pgbench may not match the local libpq, so we explicitly require
	// the local_install build that pgcluster.Cluster's env() sets
	// LD_LIBRARY_PATH for.
	pgbenchBin := filepath.Join(repo, "postgres", "local_install", "bin", "pgbench")
	if st, err := os.Stat(pgbenchBin); err != nil || !st.Mode().IsRegular() {
		t.Skipf("pgbench not present at %s", pgbenchBin)
	}

	baseDir := filepath.Join(repo, "tmp", "pg2g-pgb-ins")
	_ = os.RemoveAll(baseDir)

	slotName := "pg2g_pgb_ins"
	psc := pubsubcluster.NewMixed(t, "pg2g_pgb_ins", pubsubcluster.Options{
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

	// Pre-create matching schema on both ends. INSERT-only workload, so
	// REPLICA IDENTITY DEFAULT (the PK) is sufficient; goopg's CREATE
	// SUBSCRIPTION does not auto-create slots so the publisher slot is
	// minted up front.
	psc.Publisher.Exec(t, "CREATE TABLE public.bench_log (id bigint PRIMARY KEY, client_id int NOT NULL)")
	psc.Subscriber.Exec(t, "CREATE TABLE public.bench_log (id bigint PRIMARY KEY, client_id int NOT NULL)")
	psc.CreatePublication(t, "bench_log")

	psc.Publisher.Exec(t, fmt.Sprintf(
		"SELECT pg_create_logical_replication_slot('%s', 'pgoutput')", slotName))

	conn := psc.Publisher.Conninfo(slotName)
	psc.Subscriber.Exec(t, fmt.Sprintf(
		"CREATE SUBSCRIPTION %s CONNECTION '%s' PUBLICATION p WITH (enabled = true, copy_data = false, slot_name = '%s', create_slot = false)",
		slotName, conn, slotName))

	// Write the custom pgbench script. `\set rid random(1, 10^9)` mints
	// a unique-with-overwhelming-probability PK; :client_id is
	// pgbench's built-in 0..nclients-1 index.
	scriptPath := filepath.Join(t.TempDir(), "bench_insert.sql")
	scriptBody := "\\set rid random(1, 1000000000)\n" +
		"INSERT INTO public.bench_log (id, client_id) VALUES (:rid, :client_id);\n"
	if err := os.WriteFile(scriptPath, []byte(scriptBody), 0o644); err != nil {
		t.Fatalf("write pgbench script: %v", err)
	}

	// 2 clients × 25 transactions = 50 rows. `--no-vacuum` because no
	// vacuum target exists; `-f` overrides the default tpcb-like.
	out := psc.Publisher.Pgbench(t,
		"--no-vacuum",
		"-c", "2",
		"-j", "2",
		"-t", "25",
		"-f", scriptPath,
	)
	if !strings.Contains(out, "tps") {
		t.Logf("pgbench output:\n%s", out)
	}

	// Convergence: exactly 50 rows across both clients within 60 s.
	psc.WaitForRow(t, "public.bench_log", "1=1", 50, 60*time.Second)

	// Per-client progress: each client_id contributed strictly between
	// 1 and 49 rows. pgbench's round-robin client dispatch doesn't
	// guarantee a perfect 25/25 split (system scheduler decides) but
	// both clients MUST contribute or the harness is broken.
	c0 := psc.Subscriber.QueryScalar(t, "SELECT count(*) FROM public.bench_log WHERE client_id = 0")
	c1 := psc.Subscriber.QueryScalar(t, "SELECT count(*) FROM public.bench_log WHERE client_id = 1")
	if c0 == "0" {
		t.Fatalf("subscriber saw 0 rows for client_id=0 (got c0=%s c1=%s) — pgbench client 0 did not fire", c0, c1)
	}
	if c1 == "0" {
		t.Fatalf("subscriber saw 0 rows for client_id=1 (got c0=%s c1=%s) — pgbench client 1 did not fire", c0, c1)
	}

	// Negative pin: no stray rows outside the 0/1 client range.
	psc.WaitForRow(t, "public.bench_log", "client_id NOT IN (0, 1)", 0, 5*time.Second)
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
