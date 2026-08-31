# M0106-0010 Step 3c — pg_amop and pg_amproc Heap Bootstrap

Status: implemented (2026-05-17)
Milestone: M0106 (PG standby ↔ goopg primary)
Scope: `internal/initdb/`

## Why this exists

Step 3b (commit `1d59b06`) seeded `pg_opclass` rows so PG18's
`SearchSysCache1(CLAOID, opcid)` resolves the opclass referenced
by every nailed-index `pg_index.indclass` entry. That unblocked
relcache opclass resolution but immediately exposes the next
load-bearing gap.

PG's `RelationInitIndexAccessInfo` flows into
`IndexSupportInitialize` → `LookupOpclassInfo`
(`postgres/src/backend/utils/cache/relcache.c:1616-1810`), which
scans **pg_amproc** for every nailed opclass:

```c
/* Scan pg_amproc to obtain support procs for the opclass.  We only fetch
 * the default ones (those with lefttype = righttype = opcintype).  */
ScanKeyInit(&skey[0], Anum_pg_amproc_amprocfamily, ...)
ScanKeyInit(&skey[1], Anum_pg_amproc_amproclefttype, ...)
ScanKeyInit(&skey[2], Anum_pg_amproc_amprocrighttype, ...)
rel = table_open(AccessMethodProcedureRelationId, AccessShareLock);
scan = systable_beginscan(rel, AccessMethodProcedureIndexId, indexOK, NULL, 3, skey);
while (HeapTupleIsValid(htup = systable_getnext(scan))) {
    Form_pg_amproc amprocform = (Form_pg_amproc) GETSTRUCT(htup);
    if (amprocform->amprocnum <= 0 ||
        (StrategyNumber) amprocform->amprocnum > numSupport)
        elog(ERROR, "invalid amproc number %d for opclass %u",
             amprocform->amprocnum, operatorClassOid);
    opcentry->supportProcs[amprocform->amprocnum - 1] = amprocform->amproc;
}
```

If pg_amproc has no row for `(opcfamily, opcintype, opcintype)`,
the support-proc slot stays zero and the standby ERRORs the
first time the index AM dispatches to its comparison function.

**pg_amop** is not scanned by `LookupOpclassInfo` but is required
the moment the planner resolves an operator → strategy mapping
(`get_op_btree_interpretation`, `op_in_opfamily`, etc.). Without
it, `SELECT * FROM pg_class WHERE oid = $1` cannot prove `oid =`
is the btree equality strategy and falls back to seq-scan or
errors. Step 3c bootstraps both catalogs together.

## What landed

1. **`internal/initdb/initdb.go`**: new `pgAmopEntry` /
   `pgAmopColDefs()` / `pgAmopInitialEntries()` / `pgAmopRow()` /
   `bootstrapPgAmopTuples()` and parallel `pgAmprocEntry` /
   `pgAmprocColDefs()` / `pgAmprocInitialEntries()` / `pgAmprocRow()` /
   `bootstrapPgAmprocTuples()`. Both write single-page heaps via
   `writeMultiPageHeapRows` to `base/{1,5}/2602` and `base/{1,5}/2603`.

2. **pg_amop seed — 40 rows (8 opclass families × 5 strategies)**:

   | family OID | opcintype | label         | strategies → pg_operator OIDs (<, <=, =, >=, >) |
   |-----------:|----------:|---------------|--------------------------------------------------|
   | 1976 INTEGER_BTREE | 23 int4 | int4_ops      | 97, 523, 96, 525, 521 |
   | 1976 INTEGER_BTREE | 21 int2 | int2_ops      | 95, 522, 94, 524, 520 |
   | 1976 INTEGER_BTREE | 20 int8 | int8_ops      | 412, 414, 410, 415, 413 |
   | 1989 OID_BTREE     | 26 oid  | oid_ops       | 609, 611, 607, 612, 610 |
   | 1994 TEXT_BTREE    | 25 text | text_ops      | 664, 665, 98, 667, 666 |
   | 1994 TEXT_BTREE    | 19 name | name_ops      | 660, 661, 93, 663, 662 |
   | 2095 TEXT_PATTERN  | 25 text | text_pattern_ops (shared by varchar_pattern_ops) | 2314, 2315, 98, 2317, 2318 |
   | 424  BOOL_BTREE    | 16 bool | bool_ops      | 58, 1694, 91, 1695, 59 |

   OIDs sourced from `postgres/src/include/catalog/pg_operator.dat`.
   `amoppurpose = 's'` (search) and `amopsortfamily = 0` for every
   row — ordering operators are out of scope for v0.

   Row OIDs start at synthetic `baseOID = 7000` and increment
   contiguously (well below `FirstGenbkiObjectId = 10000`).

3. **pg_amproc seed — 8 rows (one cmp support proc per opclass family)**:

   | family OID | opcintype | label              | amproc OID | amproc name        |
   |-----------:|----------:|--------------------|-----------:|--------------------|
   | 1976 | 23 int4 | int4_ops           | 351 | btint4cmp          |
   | 1976 | 21 int2 | int2_ops           | 350 | btint2cmp          |
   | 1976 | 20 int8 | int8_ops           | 842 | btint8cmp          |
   | 1989 | 26 oid  | oid_ops            | 356 | btoidcmp           |
   | 1994 | 25 text | text_ops           | 360 | bttextcmp          |
   | 1994 | 19 name | name_ops           | 359 | btnamecmp          |
   | 2095 | 25 text | text_pattern_ops   | 2166 | bttext_pattern_cmp |
   |  424 | 16 bool | bool_ops           | 1693 | btboolcmp          |

   OIDs sourced from `postgres/src/include/catalog/pg_proc.dat`.
   Row OIDs start at synthetic `baseOID = 7100`. Only support
   proc 1 (BTORDER_PROC, the comparison function) is seeded —
   sortsupport (2), in_range (3), and equalimage (4) are
   optional and only consulted when an AM-specific code path
   exercises them.

4. **`internal/initdb/relcache_init.go`**:
   - `pgAmopAttrs()` extended 4 → 9 columns to match PG18's
     `FormData_pg_amop`. New attrs: `amopstrategy` (int2, attnum 5),
     `amoppurpose` (char, attnum 6), `amopopr` (oid, attnum 7),
     `amopmethod` (oid, attnum 8), `amopsortfamily` (oid, attnum 9).
   - `pgAmprocAttrs()` extended 4 → 6 columns to match PG18's
     `FormData_pg_amproc`. New attrs: `amprocnum` (int2, attnum 5),
     `amproc` (regproc, attnum 6).
   - `nailedLocalRels` bumps pg_amop `relnatts` 4 → 9 and
     pg_amproc `relnatts` 4 → 6 so PG's `heap_deformtuple` reads
     every attr without overrunning the descriptor.

5. **`Init` wiring**: `bootstrapPgAmopTuples(abs)` and
   `bootstrapPgAmprocTuples(abs)` run immediately after
   `bootstrapPgOpclassTuples(abs)` so a fresh `goopg init` writes
   pg_am → pg_proc → pg_opclass → pg_amop → pg_amproc in the
   order PG's startup probes them.

## Layout notes

`FormData_pg_amop` ends with `int16 amopstrategy / char amoppurpose
/ Oid amopopr`. PG's struct alignment inserts 1 padding byte
between `amoppurpose` (offset 18, 1 byte) and `amopopr` (offset
20, 4-byte aligned). `executor.EncodeRowPG` already honours
PG-native alignment, so the encoded payload places `amopopr` at
offset 20 — see `TestPgAmopRowInt4LessMatchesFormPgAmop`.

`FormData_pg_amproc` has `int16 amprocnum / regproc amproc`. The
2-byte amprocnum at offset 16 is followed by 2 padding bytes
before amproc (regproc, 4-byte aligned, offset 20) — see
`TestPgAmprocRowInt4CmpMatchesFormPgAmproc`.

Neither catalog has varlena columns, so `hasVarWidthCol` correctly
returns false and the HEAP_HASVARWIDTH infomask bit is not set.

## Out of scope (tracked separately)

- **Cross-type amop / amproc rows** (e.g. int2 < int4, name vs
  text). PG's `LookupOpclassInfo` only scans for
  `lefttype = righttype = opcintype`, so default-type rows are
  sufficient for nailed-index startup. Cross-type entries are
  needed for any cross-type predicate to plan through a btree
  index and will be revisited if the planner exercises them.

- **Step-3b family OID corrections**. `pg_opclass` rows pinned in
  Step 3b assign the wrong opfamily to four opclasses:
  - `bpchar_pattern_ops` (4219) → currently family 426 (BPCHAR_BTREE),
    canonical is 2097 (BPCHAR_PATTERN_BTREE_FAM_OID).
  - `char_ops` (1985) → currently family 1994 (TEXT_BTREE),
    canonical is the dynamically-assigned `btree/char_ops` family.
  - `oidvector_ops` (1987) → currently family 1989 (OID_BTREE),
    canonical is the dynamically-assigned `btree/oidvector_ops` family.
  - `varchar_pattern_ops` (4218) shares family/lefttype with
    `text_pattern_ops`; deduped here by emitting one row pair.

  Step 3c skips amop/amproc rows for these four opclasses to
  avoid encoding incorrect operator → strategy mappings under a
  shared family OID. Fixing Step 3b's opfamily assignments and
  adding the missing canonical rows is filed as a follow-up.

- **pg_amop OIDs are synthetic**. PG's `pg_amop.dat` and
  `pg_amproc.dat` don't pin per-row OIDs upstream — initdb
  assigns them. We pick `baseOID = 7000` / `7100` ranges; the
  oid_index can be re-seeded from these values when M0106-0011
  wires up the rebuild path.

- **Support procs 2/3/4** (sortsupport, in_range, equalimage).
  These are optional in btree (`BTNProcs - 1`); only the cmp
  proc is mandatory. Adding them later is purely additive.

- **pg_operator and pg_proc heap rows** for the OIDs we reference
  here. PG can record the regproc references without resolving
  them (no FK enforcement in pg_amproc), so the standby boots
  past relcache init. The first actual *call* to any of these
  procs needs the body in pg_proc — out of scope until a later
  step that bootstraps the builtin function catalog.

## Verification

- `go test -count=1 -run "TestPgAmop|TestPgAmproc|TestBootstrapPgAmop|TestBootstrapPgAmproc" ./internal/initdb/` — PASS.
- `go test -count=1 ./internal/initdb/` — all green except the
  pre-existing baseline failure `TestSynchronousCommitFlushesByDefault`
  (tracked under M0106-0012; confirmed unchanged via baseline stash).
- `go test -count=1 ./internal/executor/ ./internal/server/
  ./internal/storage/ ./internal/catalog/ ./internal/mvcc/` — PASS.

### Regression pins

`internal/initdb/pg_amop_bootstrap_test.go`:

- `TestPgAmopRowInt4LessMatchesFormPgAmop` — byte-offset asserts
  for every field of the int4 strategy-1 row, including the
  1-byte char→oid padding between `amoppurpose` and `amopopr`.
- `TestPgAmopInitialEntriesCoverPinnedOpclasses` — every pinned
  opclass family has its canonical 5 strategy rows wired to
  real pg_operator.dat OIDs, and (family, lefttype, strategy)
  is unique.
- `TestBootstrapPgAmopTuplesWritesRowsToBase1And5` — the
  end-to-end bootstrap writes the int4 strategy-1 row to both
  `base/1/2602` and `base/5/2602`.
- `TestPgAmopAttrsMatchesPg18FormPgAmop` — the relcache
  init-file TupleDesc has all 9 columns in the PG18-canonical
  order with the right (TypeOID, Len, attnum) per attr.

`internal/initdb/pg_amproc_bootstrap_test.go`:

- `TestPgAmprocRowInt4CmpMatchesFormPgAmproc` — byte-offset
  asserts for every field of the int4 cmp row, including the
  2-byte int2→regproc padding between `amprocnum` and `amproc`.
- `TestPgAmprocInitialEntriesCoverPinnedOpclasses` — every pinned
  opclass family has its canonical comparison-proc row wired to
  a real pg_proc.dat OID.
- `TestBootstrapPgAmprocTuplesWritesRowsToBase1And5` — the
  end-to-end bootstrap writes the int4 cmp row to both
  `base/1/2603` and `base/5/2603`.
- `TestPgAmprocAttrsMatchesPg18FormPgAmproc` — the relcache
  init-file TupleDesc has all 6 columns in the PG18-canonical
  order, including `amproc` typed as regproc (OID 24).
