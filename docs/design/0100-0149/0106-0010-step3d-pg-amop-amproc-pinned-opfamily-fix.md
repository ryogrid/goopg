# M0106-0010 Step 3d — Pinned-opclass opfamily OID corrections + amop/amproc rows

Status: Accepted (2026-05-17)
Milestone: M0106 — PG Relcache Init File Compatibility
Sub-milestone: M0106-0010 (Resolve array assertion and bootstrap pg_am(+related) tuples)
Predecessor: 0106-0010-step3c-pg-amop-amproc-bootstrap.md

## Why

Step 3b seeded twelve btree opclass rows into the bootstrapped `pg_opclass`
heap, but three of them carried the wrong `opcfamily` OID:

| opclass            | OID  | Step 3b family       | Correct (`pg_opfamily.dat`)        |
| ------------------ | ---- | -------------------- | ---------------------------------- |
| `char_ops`         | 1985 | 1994 (TEXT_BTREE)    | **429** (`btree/char_ops`)         |
| `oidvector_ops`    | 1987 | 1989 (OID_BTREE)     | **1991** (`btree/oidvector_ops`)   |
| `bpchar_pattern_ops` | 4219 | 426 (BPCHAR_BTREE)   | **2097** (`BPCHAR_PATTERN_BTREE_FAM_OID`) |

PG's `RelationCacheInitializePhase3 → load_critical_index → ScanPgClass`
chain reads the opcfamily from the heap tuple and then issues
`SearchSysCache1(AMOPOPID, ...)` keyed on `(family, lefttype, strategy)`.
With the wrong family, the lookup misses every amop/amproc row even
though Step 3c populated them correctly under the canonical families,
and the nailed-index scan PANICs.

Step 3c additionally seeded amop/amproc rows for eight default-type
families but omitted `char_ops`, `oidvector_ops`, and `bpchar_pattern_ops`
because Step 3b hadn't introduced their opfamilies yet. Step 3d adds
those rows so the corrected opclasses resolve end-to-end.

## What changed

### `internal/initdb/initdb.go`

`pgOpclassInitialEntries()` — `famCharBtree=429`, `famOidvectorBtree=1991`,
`famBpcharPatternBtree=2097`, `famBool=424` introduced as named constants;
the three wrong rows now reference them. (`famBpchar=426` removed — was
only used by the bpchar_pattern_ops row, which now uses 2097.)

`pgAmopInitialEntries()` — slice capacity bumped 40 → 55. Three additional
`add(family, lefttype, ops)` calls:

| family | lefttype       | strategies 1..5                                  |
| ------ | -------------- | ------------------------------------------------ |
| 429    | 18 (char)      | 631 `<`, 632 `<=`, 92 `=`, 634 `>=`, 633 `>`     |
| 1991   | 30 (oidvector) | 645 `<`, 647 `<=`, 649 `=`, 648 `>=`, 646 `>`    |
| 2097   | 1042 (bpchar)  | 2326 `~<~`, 2327 `~<=~`, 1054 `=`, 2329 `~>=~`, 2330 `~>~` |

Operator OIDs sourced from `postgres/src/include/catalog/pg_operator.dat`.

`pgAmprocInitialEntries()` — three additional default cmp procs:

| family | lefttype       | amproc                                |
| ------ | -------------- | ------------------------------------- |
| 429    | 18 (char)      | 358 (`btcharcmp`)                     |
| 1991   | 30 (oidvector) | 404 (`btoidvectorcmp`)                |
| 2097   | 1042 (bpchar)  | 2180 (`btbpchar_pattern_cmp`)         |

Procedure OIDs sourced from `pg_proc.dat`.

### Tests

`internal/initdb/pg_opclass_bootstrap_test.go::TestPgOpclassInitialEntriesCoverNailedIndexNeeds`
now pins the canonical `opcfamily` value for every required OID so a future
regression of Step 3b's bug fails the test immediately.

`internal/initdb/pg_amop_bootstrap_test.go::TestPgAmopInitialEntriesCoverPinnedOpclasses`
extended with three new rows (char, oidvector, bpchar_pattern) and an
explicit `len(entries) == 55` count check.

`internal/initdb/pg_amproc_bootstrap_test.go::TestPgAmprocInitialEntriesCoverPinnedOpclasses`
extended with three new procs and a `len(entries) == 11` count check.

## What is still out of scope (Step 3d does not cover)

* **Cross-type amop/amproc rows** (e.g. `int4 < int8`). Every row added so
  far has `amoplefttype == amoprighttype`; the test enforces that
  invariant. Cross-type rows can be added once a concrete nailed-index
  scan needs them.
* **Strategy 2 sortsupport / strategy 4 equalimage rows.** Step 3c+3d seed
  only `amprocnum == 1` (BTORDER_PROC). PG's `LookupOpclassInfo` will use
  the default code path for sort/equalimage when these are absent.

## Verification

```
go test -count=1 -run 'TestPgAmop|TestPgAmproc|TestPgOpclass' ./internal/initdb/
ok  github.com/goopg/goopg/internal/initdb  0.004s

go test -count=1 ./internal/executor/ ./internal/server/ ./internal/storage/ \
        ./internal/catalog/ ./internal/mvcc/
ok all 5 packages
```

`go test -count=1 ./internal/initdb/` shows the same 14 pre-existing
failures present before this change (confirmed via `git stash` baseline
diff). No new failures introduced.
