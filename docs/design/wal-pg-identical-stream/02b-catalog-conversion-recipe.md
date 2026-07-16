# 02b — Per-catalog conversion recipe (normative)

| | |
|---|---|
| Status | draft — pending agent review |
| Date | 2026-07-16 |
| Scope | The reusable checklist every B1–B4 catalog conversion follows. One catalog per landing; no half-states. |
| Target | PostgreSQL 18.3 (`postgres/src/include/catalog/pg_<name>.h`, `postgres/src/backend/catalog/indexing.c`) |
| Parent | [02-catalog-heap-journaling.md](02-catalog-heap-journaling.md) §4.2; enablers in [02a](02a-phase-b0-enablers.md) |

## 1. Inputs per catalog (gather BEFORE writing code)

For catalog `pg_<name>`:

1. **Tuple layout**: the `CATALOG(pg_<name>,<oid>,...)` block in
   `postgres/src/include/catalog/pg_<name>.h` — column order, types, nullability
   (`CATALOG_VARLEN` marks the varlena tail), BKI defaults. This is the only
   source of truth; never derive layout from goopg's virtual-row column list.
2. **Index set**: the `DECLARE_UNIQUE_INDEX[_PKEY]` / `DECLARE_INDEX` lines in the
   same header — index name, OID, keyed columns, opclasses. Every one of them
   gets runtime insert maintenance.
3. **OID constants**: relation OID, index OIDs, TOAST OIDs (if `DECLARE_TOAST`).
   TOAST relations are out of scope until a converted catalog actually stores a
   >2KB varlena (none in B1; note it in the conversion's risk list).
4. **DDL inventory**: every goopg emit site of the catalog's bespoke records
   (grep the `Encode<X>` symbols) and the PG command that corresponds, with its
   upstream journaling shape (INSERT / UPDATE / DELETE and which columns change).

## 2. The seven conversion steps (one landing)

1. **Tuple builder** — new `internal/executor/sys_pg_<name>.go`:
   `pg<Name>ColumnsPG18() []catalog.Column` (mirroring the header) +
   `buildPG<Name>Row(obj) Row`. Encoding rides `writeHeapRowReturningPG`
   (`internal/executor/operators_storage.go:8038`) so the on-page bytes are
   PG-canonical (name = 63-byte-clipped NameData, aclitem[] arrays, etc.).
2. **Emit-site swaps** — each `ctx.WAL.Append(Encode<X>(...))` becomes:
   - CREATE → heap INSERT (`writeHeapRowReturningPG` into
     `base/<dbOid>/<relOid>`, or `global/<relOid>` for shared catalogs) +
     index inserts (step 3) + `XLOG_SMGR_CREATE` only when a genuinely new
     relation file is born (sequences, matviews).
   - ALTER → `updateHeapRowCanonicalPG` (02a §3). Non-HOT when any indexed
     column changes → fresh entries in **all** indexes; old entries stay until
     vacuum (PG semantics).
   - DROP → heap DELETE (stamp xmax; index entries untouched).
   The in-memory registry mutation stays exactly where it is today and becomes
   the write-through cache (02a §6), extended to carry the row TID.
3. **Index-insert helpers** — one helper per index in the
   `sys_catalog_index_insert.go` pattern, keyed per the header's `btree(...)`
   spec, routing to `base/<ctx.dbOid>/` (needs B0.3) or `global/`.
4. **Reload descriptor** — a `catalogReloadDesc` (02a §2.2) with its `Order` row
   added to 02a §2.4's table and its bootstrap-row policy chosen (02a §2.5).
5. **Read-model swap** — per §3 below. Default: the existing registry becomes
   the write-through cache; the `VirtualRows` builder is re-pointed at the
   registry (pg_class precedent) — it is no longer "virtual truth", just a
   render of the cache.
6. **Deletion checklist** (same landing — the recipe forbids emitting neither or
   both):
   - [ ] `RecordKind*` constants + `Encode*`/`Decode*` in `internal/wal/recovery.go`
   - [ ] dispatch cases in `ReplayRecords`' native switch + the native-kind set
   - [ ] `internal/initdb/<name>_ddl_recovery.go` + its call in `open.go`
   - [ ] any `internal/wal/<name>_*.go` payload helpers
   - [ ] tests pinning the bespoke records → rewritten to pin the heap records
   - [ ] grep-zero: the deleted symbols appear nowhere (comments excepted)
7. **initdb bootstrap check** — the catalog's heap file must be populated (or
   correctly empty) at initdb and its btrees present in `relcache_init.go` /
   `btree_index_bootstrap.go`; if the catalog was a placeholder heap, add its
   bootstrap rows so PG tools and the reload scan agree.

## 3. Read-model decision matrix

| Read model | When | Examples |
|---|---|---|
| **Write-through cache** (registry + TID map, rebuilt from heap at startup) — DEFAULT | Catalog is consulted on hot paths (name resolution, planning) or by resolver SQL | pg_namespace, pg_proc, pg_class |
| **True heap-read** (`RegisterRealTable`, `internal/catalog/catalog.go:11603`) | Low-traffic, low-cardinality; a SeqScan per lookup is acceptable | pg_event_trigger, pg_transform (B3) |

A real indexed syscache is future work (doc 02 §4.2); nothing in Phase B may
assume one exists.

## 4. Per-catalog gate list (normative — run ALL for every conversion)

1. `go build ./...` + `go vet` on touched packages.
2. Unit suites: `internal/wal`, `internal/initdb`, `internal/executor`,
   `internal/catalog`; `-race` on WAL/storage-touching packages.
3. **Crash-after-each-converted-DDL** recovery test (replaces the deleted
   scanner's tests): run the DDL, kill, restart, assert the object via SQL.
4. Full `TestPort_RegressSuite` + `TestPort_IsolationSuite`.
5. `pg_waldump` structural: the DDL produces exactly the PG record shape
   (e.g. CREATE = `Heap/INSERT` on the catalog relfile + one
   `Btree/INSERT_LEAF` per index).
6. `TestE2E_FailoverGoopgToPG`: a real PG standby replays the DDL and the object
   is visible via psql (`\dn`, `\df`, …).
7. Data-dir re-init + re-run (on-disk catalog contents changed).
8. Deletion checklist grep-zero (step 6).
9. `scripts/tpch-spotcheck.sh` when `internal/executor` planner/eval surfaces
   were touched.

## 5. Transition rules

1. **One catalog per landing** (doc 02 risk R1) — never batch conversions.
2. **Atomic swap**: the bespoke record's emit AND its scanner die in the same
   landing that introduces the heap emit. A record that is emitted but no longer
   scanned (or vice versa) is a silent recovery hole.
3. **Old-WAL compatibility is NOT preserved**: converting a catalog is an
   on-disk/WAL format break — re-init is required (bundle-wide decision,
   README "Data-dir format break is accepted").
4. **Residuals go to the ledger**: any payload field or DDL a conversion cannot
   yet retire (e.g. pg_sequence's counter state, OwnedBy dependencies) gets a
   deferral-ledger row with a resume point, and the conversion doc says exactly
   which fields survive.
