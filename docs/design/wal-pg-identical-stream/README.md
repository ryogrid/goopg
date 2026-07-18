# WAL PG-identical stream — design bundle

| Field   | Value                                                             |
| ------- | ----------------------------------------------------------------- |
| Status  | draft — **agent-reviewed** 2026-07-15 (0 blocker; design sound and feasible); no code change in this bundle |
| Date    | 2026-07-15                                                        |
| Branch  | `wal-format-mod`                                                   |
| Oracle  | PostgreSQL 18.3 — tree under [`postgres/`](../../../postgres/)     |
| Goal    | Make goopg's WAL **stream** byte-identical to what a normally-configured PG 18.3 emits |

## Why this bundle exists

After the canonical-removal work ([`../wal-native-pg-format/04-remove-canonical-and-pg-rmgr-dispatch.md`](../wal-native-pg-format/04-remove-canonical-and-pg-rmgr-dispatch.md)),
goopg emits a **PG-compatible WAL frame**: page headers (magic `0xD118`), the
24-byte `XLogRecord` header with a real `(xl_rmid, xl_info)`, CRC32C, MAXALIGN
padding, and cross-segment contrecords. A stock `pg_waldump` can already walk the
page/record structure.

Two things still diverge from PostgreSQL, and this bundle designs the work to
close both so that **a byte-for-byte diff of a goopg WAL segment against a real PG
segment for the same workload is empty** (modulo intentionally non-deterministic
fields — timestamps, system identifier):

1. **Record bodies** are still goopg-native. Each record's main-data + block
   references do not match PostgreSQL's `xl_heap_insert` / `xl_btree_split` /
   `xl_xact_commit` / … byte layouts, and goopg never emits real block references
   or hole-compressed full-page images. → **Part A**.
2. **~110 catalog/DDL records ("Section B")** have **no PostgreSQL analog at all**.
   PostgreSQL journals catalog changes as ordinary heap-tuple DML on `pg_catalog`
   relations; goopg journals them as bespoke `RmgrGoopgCatalog` records. Parity is
   *impossible* while these records exist. → **Part B** (the crux).

This bundle is the follow-on that the spec bundle deliberately scoped out
(`../wal-native-pg-format/README.md` "Scope note"; doc 04 §1 non-goals).

## Guiding principle: emit PG-shaped records at the source, never convert

The single most important design rule, per the project owner's directive:

> **Generate PG-compatible WAL records natively, at the point of the page
> mutation — do NOT build a goopg-native record and then translate it to PG form.**

A conversion layer would double the per-record encoding cost on the write hot path
and duplicate the format knowledge. Instead, every emit site builds the PG record
directly. Concretely this means the record *encoders* are rewritten to produce PG
bodies, and — because goopg's own recovery currently re-parses the native bodies —
**recovery moves onto the already-faithful PG decoder** (`internal/wal/pg_xlog_decode.go`)
so there is exactly one format, read and written the same way PostgreSQL does.

Where "just change the WAL encoder" cannot achieve parity — which is the case for
all of Section B — the design **embraces the larger rewrite** (catalog storage) it
requires, rather than papering over it.

## Relationship to the `wal-native-pg-format` bundle

| Doc | Role |
| --- | --- |
| [`../wal-native-pg-format/01-emitted-wal-record-inventory.md`](../wal-native-pg-format/01-emitted-wal-record-inventory.md) | The emit set (§A analog records, §B goopg-private records). This bundle's scope. |
| [`../wal-native-pg-format/02-wal-schema-dsl-spec.md`](../wal-native-pg-format/02-wal-schema-dsl-spec.md) | The DSL doc 03 is written in. |
| [`../wal-native-pg-format/03-pg183-wal-record-schemas.md`](../wal-native-pg-format/03-pg183-wal-record-schemas.md) | **The byte-layout target** for Part A. |
| [`../wal-native-pg-format/04-remove-canonical-and-pg-rmgr-dispatch.md`](../wal-native-pg-format/04-remove-canonical-and-pg-rmgr-dispatch.md) | The `(rmid,info)` classification + dispatch this bundle builds on. |

## Documents in this bundle

| Doc | Title | Scope |
| --- | ----- | ----- |
| [01](01-record-content-parity.md) | Record content parity (Section A) | Rewrite heap/heap2/btree/xact/smgr/clog/xlog bodies to PG byte layouts; the block-reference + FPI encoder; the atomic encode↔replay flip; `xl_xid`; FPI/logical unification. |
| [02](02-catalog-heap-journaling.md) | Catalog heap journaling (Section B) | Eliminate the ~110 bespoke catalog records by journaling catalog DDL as PG-style heap ops on real `pg_catalog` relations. The catalog-storage rewrite. |
| [02a](02a-phase-b0-enablers.md) | Phase-B0 enablers (detailed design) | The four shared enablers: generic per-catalog heap reload, catalog `XLOG_HEAP_UPDATE` emit, per-DB catalog index bootstrap, `pg_filenode.map` + `XLOG_RELMAP_UPDATE` (deferrable). |
| [02b](02b-catalog-conversion-recipe.md) | Per-catalog conversion recipe (normative) | The reusable seven-step checklist, read-model matrix, gate list, and transition rules every B1–B4 conversion follows. |
| [02c](02c-phase-b1-application.md) | Phase-B1 application | pg_namespace / pg_proc / pg_sequence specifics; the pg_sequence catalog-row-only scope decision. |
| [02d](02d-phase-b2-b5-overview.md) | Phase-B2–B5 overview | Application tables + risk deltas for the remaining groups; shared `global/` catalogs; RmgrGoopgCatalog retirement; ledger index. |
| [02e](02e-content-fidelity-and-durability.md) | Content fidelity + durability (post-B5 open items) | The three deferrals left after rmid-128 closed: matview `IsPopulated` durability, `ALTER … RENAME` durability, and the canonical `pg_node_tree` subsystem (`internal/pgnodes`) so a PG standby can evaluate/query user defaults/stats/views (`relhasrules=true`). |

Cross-cutting concerns (performance, verification, risk) are covered at the end of
each doc where they apply, and summarized in §"Program-level view" below.

## Program-level view

### Two efforts of very different shape

- **Part A is a bounded WAL-generation + recovery rewrite.** The reusable pieces
  (PG tuple encoding, the FPI trigger, the mutation hooks, the faithful decoder)
  already exist; the net-new is one PG-faithful **block-reference + FPI encoder**
  plus a disciplined **per-record atomic flip** of encode + replay. It touches
  `internal/wal` and the emit closures in `internal/initdb/open.go`.
- **Part B is a catalog-storage rewrite.** The WAL records that replace Section B
  (`XLOG_HEAP_*` + `XLOG_BTREE_*` on catalog relfiles) are *already produced* by
  goopg's heap/btree access methods — so "WAL generation" per se is largely solved.
  What is missing is making **every** system catalog a real heap-backed relation
  with runtime insert/update/delete + full index maintenance + generic heap-scan
  recovery, and retiring the ~50 base-catalog virtual builders, the ~102 **bespoke**
  `RecordKind` constants (the 122 total minus the ~20 heap/btree/xact/smgr/clog
  analogs that Part A *rewrites* rather than deletes), and the 26
  `*_ddl_recovery.go` recovery scanners. It touches
  `internal/catalog`, `internal/executor`, `internal/server/*_ddl.go`,
  `internal/initdb`, and `internal/wal/recovery.go`.

### Sequencing

Both parts are independently staged so each increment lands green:

1. **Part A block-ref/FPI encoder** (keystone) + the first hot-path records
   (HeapInsert, HeapDelete, HotUpdate, BtreeInsert, XactCommit). Each record flips
   encode+replay together; re-enable its `pg_waldump` parity assertion as it lands.
2. **Part A** remaining records (heap2 prune/vacuum/freeze composite; the rarer
   btree structural records; smgr/clog).
3. **Part B** catalogs by leverage: `pg_namespace` / `pg_proc` / `pg_sequence`
   first, then the type/operator/opclass families, then TS / publication /
   subscription. Each catalog: heap-backed DML + index maintenance + heap-scan
   recovery, then delete its bespoke record + scanner + virtual builder.

Part A and Part B are largely independent (different subsystems) and can proceed in
parallel; they converge on the same verification: `pg_waldump` decodes every record
cleanly, and a real PG 18 standby replays goopg's WAL.

### Verification (shared)

- **`pg_waldump` structural + rmgr decode** returns to green — re-enable the doc-04
  expected-fail tests (`TestPort_WALPgWaldumpCompat`, `TestPGWaldumpParsesEmittedWAL`,
  the `pg_waldump` fullpage/prune tests) **per record/catalog as it converts**.
- **A real PG 18 standby replays goopg WAL** (`TestE2E_FailoverGoopgToPG`,
  `TestE2E_ChecksumStreamingGoopgToPG`) — the ultimate parity gate.
- **goopg↔goopg recovery/replication stays green** (`TestE2E_NativeOnlyReplicationAndPromotion`,
  crash-recovery) at every step — the decoder is already the replay source, so the
  flip is transparent to goopg's own standby.
- **Byte-diff** a goopg WAL segment against a PG segment produced by the same
  workload (`pg_waldump` normalized, then raw-byte for the deterministic fields).
- The **atomic-flip gate** (G-crash on `internal/wal` + `internal/initdb`) run on
  every per-record / per-catalog increment.

### Risk posture

- **Sibling-path atomic flip** (Part A): a record whose encoder emits a PG body but
  whose replay still expects the native body silently corrupts recovery. Every flip
  is encode+decode+replay together, gated by G-crash and a per-record round-trip.
- **Catalog-storage blast radius** (Part B): touching catalog read/write is the
  project's most expensive silent-regression class (row-count/visibility). Stage per
  catalog; run the full regress + isolation suites after each; re-init data dirs
  (on-disk catalog format).
- **Data-dir format break**: record bodies and catalog storage change on disk —
  existing data dirs must be re-initialized (fresh clusters), as with the perf-optimize3
  native-only default and doc 04. This is acceptable and documented.

## Review log (Phase-B detail docs 02a–02d)

| date | reviewer lens | outcome |
|------|---------------|---------|
| 2026-07-16 | PG fidelity (adversarial, vs `./postgres` sources) | 2 BLOCKER (RelMap rmgr id 7 not 15; foreign-data trio is per-DB, not shared) + 2 MAJOR (CREATE DATABASE WAL_LOG strategy DOES emit relmap; pg_range second index 2228) + 4 MINOR — all folded with inline `(review …)` tags |
| 2026-07-16 | goopg integration (adversarial, vs code) | 2 BLOCKER (write-dbOid vs reload-dbOid routing through the postgres-DB mirror; the reload visibility rules as actually implemented) + 8 MAJOR (TID cache is net-new; M0114 cache fast path + batch-apply API; full recovery-pass ordering; CREATE DATABASE creates no catalog heaps; pg_proc kind inventory; DropSequence(66)/name-keying; global schema registry vs per-DB heap; unassigned records blocking B5) + 4 MINOR — all folded |
