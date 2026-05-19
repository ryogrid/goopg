# M0106-0010 Step 3b — pg_opclass Heap Bootstrap

Status: implemented (2026-05-17)
Milestone: M0106 (PG standby ↔ goopg primary)
Scope: `internal/initdb/`

## Why this exists

Step 3a (commit `dccbd88`) seeded `pg_proc` rows for the seven AM
handler functions so PG18's `OidFunctionCall0(amhandler)` resolves
during standby startup. PG's `RelationInitIndexAccessInfo` does not
stop there: for every nailed index it then reads `pg_index.indclass`
(an oidvector of opclass OIDs) and, for each entry, calls
`SearchSysCache1(CLAOID, opcid) → GETSTRUCT → Form_pg_opclass`. The
returned tuple's `opcfamily`, `opcintype`, `opcdefault`, and
`opckeytype` are written into the relcache `RelationAmInfo` struct.

If `pg_opclass` has no row for `opcid`, `SearchSysCache1` returns
`NULL`, `GETSTRUCT(NULL)` segfaults, and the standby PANICs with
`"cache lookup failed for opclass <oid>"` before reaching
`PM_HOT_STANDBY`. Step 3b closes that gap by seeding the canonical
btree opclasses every nailed index can reference.

## What landed

1. **`internal/initdb/initdb.go`**: new `pgOpclassEntry` struct +
   `pgOpclassColDefs()` / `pgOpclassInitialEntries()` / `pgOpclassRow()`
   / `bootstrapPgOpclassTuples()`. The bootstrap writes a single-page
   heap to `base/1/2616` and `base/5/2616` via the existing
   `writeMultiPageHeapRows` helper.

   Entries cover the hardcoded OIDs from
   `postgres/src/include/catalog/pg_opclass_d.h`:

   | OID  | opcname               | opcintype | opcfamily              |
   |------|-----------------------|-----------|------------------------|
   | 1978 | int4_ops              | int4 (23) | INTEGER_BTREE (1976)   |
   | 1979 | int2_ops              | int2 (21) | INTEGER_BTREE (1976)   |
   | 1981 | oid_ops               | oid (26)  | OID_BTREE (1989)       |
   | 3124 | int8_ops              | int8 (20) | INTEGER_BTREE (1976)   |
   | 3126 | text_ops              | text (25) | TEXT_BTREE (1994)      |
   | 4217 | text_pattern_ops      | text (25) | TEXT_PATTERN_BTREE (2095) |
   | 4218 | varchar_pattern_ops   | text (25) | TEXT_PATTERN_BTREE (2095) |
   | 4219 | bpchar_pattern_ops    | bpchar (1042) | BPCHAR_BTREE (426) |

   Plus four pinned dynamically-assigned OIDs the nailed indexes
   need but `pg_opclass.dat` does not hardcode:

   | OID  | opcname        | opcintype       | opckeytype       |
   |------|----------------|-----------------|------------------|
   | 1984 | bool_ops       | bool (16)       | 0                |
   | 1985 | char_ops       | char (18)       | 0                |
   | 1986 | name_ops       | name (19)       | cstring (2275)   |
   | 1987 | oidvector_ops  | oidvector (30)  | 0                |

   `name_ops` uses cstring storage per the
   `pg_opclass.dat` comment ("hack to save space in system catalog
   indexes"). The four pinned OIDs land below `FirstGenbkiObjectId`
   (10000) so they don't collide with user-assigned OIDs.

2. **`internal/initdb/relcache_init.go`**:
   - `pgOpclassAttrs()` extended from 7 → 9 columns to match PG18's
     `FormData_pg_opclass`: adds `opcdefault` (bool, attnum 8) and
     `opckeytype` (oid, attnum 9).
   - `nailedLocalRels` bumps pg_opclass `relnatts` 7 → 9 so PG's
     `heap_deformtuple` reads both new attrs without overrunning
     the descriptor.

3. **`Init` wiring**: `bootstrapPgOpclassTuples(abs)` runs
   immediately after `bootstrapPgProcTuples(abs)` so a fresh
   `goopg init` writes pg_am → pg_proc → pg_opclass in the order
   PG's startup will probe them.

## Layout note

`FormData_pg_opclass` ends with `bool opcdefault` followed by
`Oid opckeytype`. PG's struct alignment inserts 3 padding bytes
between offset 88 (opcdefault) and offset 92 (opckeytype, 4-byte
aligned). `executor.EncodeRowPG` already honours PG-native alignment
rules, so the encoded payload places opckeytype at offset 92 — see
`TestPgOpclassRowOidOpsMatchesFormPgOpclass`.

## Out of scope (tracked separately)

- `pg_amop` and `pg_amproc` strategy- and support-function rows
  (step 3c). Without these, PG can resolve opclasses but the
  index AM's `IndexAmRoutine` can't find comparison/sort/equality
  functions and will PANIC the first time an index scan dereferences
  one.
- `pg_index` heap rows. Nailed indexes are bootstrapped through the
  relcache init file, which already carries `indclass`. Future
  non-nailed indexes will need pg_index heap bootstrap.
- Operational catalog maintenance — `CREATE OPERATOR CLASS` must
  append new pg_opclass rows and re-emit the relcache init file
  (M0106-0011).

## Verification

- `go test -count=1 -run "TestPgOpclass|TestBootstrapPgOpclass" \
   ./internal/initdb/` — PASS.
- `go test -count=1 ./internal/initdb/` — all green except the
  pre-existing baseline failure `TestSynchronousCommitFlushesByDefault`
  (tracked under M0106-0012; confirmed unchanged via baseline stash).
- `go test -count=1 ./internal/executor/ ./internal/server/
   ./internal/storage/ ./internal/catalog/ ./internal/mvcc/` — PASS.

### Regression pins

`internal/initdb/pg_opclass_bootstrap_test.go`:

- `TestPgOpclassRowOidOpsMatchesFormPgOpclass` — byte-offset asserts
  for every field of the oid_ops row, including the 3-byte
  bool→oid padding.
- `TestPgOpclassInitialEntriesCoverNailedIndexNeeds` — every
  required opclass OID (1978–1981, 1984–1987, 3124, 3126, 4217–4219)
  is present with the right opcname / opcmethod / opcnamespace.
  Pins name_ops opckeytype = 2275 (cstring).
- `TestBootstrapPgOpclassTuplesWritesRowsToBase1And5` — the end-to-end
  bootstrap writes the oid_ops row to both `base/1/2616` and
  `base/5/2616` in a single page-sized file.
- `TestPgOpclassAttrsMatchesPg18FormPgOpclass` — the relcache
  init-file TupleDesc has all 9 columns in the PG18-canonical
  order with the right (TypeOID, Len, attnum) per attr.
