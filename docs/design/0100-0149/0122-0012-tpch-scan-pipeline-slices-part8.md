# M0122-0012 — scan pipeline slices, part 8 (TPC-DS executor gaps, continued)

Continued from [part 7](0122-0012-tpch-scan-pipeline-slices-part7.md) (slices 69–79), which was split at the
800-line limit.

**Measurement method.** All slices here are measured on the SF0.25 clone `tmp/q64-clone`, as in part 7, mostly by
instructions:u per query (`tmp/ds-instr.sh`).

**Reference timings.** Gaps are now ranked against a *release* (non-cassert) PG 18.3 build. Its per-query medians
are in `analysis/timing-after-planprity-match/data/tpcds-sf025-pgrel.tsv`. The `:65438` reference binary is a
cassert build, so ratios against it understate goopg's gap: Q64 is 9.9× against release PG but 6.6× against
cassert PG.

**Two-statement files.** Q14, Q23, Q24 and Q39 each hold two statements. An `explain` prefix covers only the first,
so measure each statement separately.

## Slice 80 — an index scan forms its probe key from resolved columns and re-aims one cursor per rescan

**Target.** Q14 is the largest absolute gap against release PG: 8.9 s against 4.8 s. Its statement 1 takes 5.3 s
(cassert PG 3.25 s) and statement 2 takes 4.8 s (cassert PG 4.4 s).

**Diagnosis.**

- **Same plans and rows.** Both statements are the same plan as PG's, with the same row counts at every node.
- **Parameterised probes.** The time is in parameterised nested loops: `store_sales_pkey`, `catalog_sales_pkey` and
  `web_sales_pkey` index scans rescanned 16 173 times each, then a `date_dim_pkey` probe per fetched row.
- **Profile.** `indexScanOp.Next` and its children are 51 % of the profile, `Rescan` 19 %.
- **The rescan.** About 62 % of `Rescan` is the descent (`NewScanCursor`) and about 35 % is `lookupKey`. Of
  `lookupKey`:
  - 85 % was `ctx.indexProbeKey`: `pgIndexKeyColumns` re-resolving every key column by name, three `make` calls,
    and the general `pgIndexTupleKey` builder;
  - the rest is a by-name `Catalog.LookupColumn` per rescan.
- **The leaf walk.** `nextLeafBatch` is 53 % of the index scan, mostly the leaf binary search (`leafScanStart`)
  through the generic comparator.

**PG.**

- `ExecIndexBuildScanKeys` builds a scan's ScanKey array once, at `ExecInitIndexScan`.
- `ExecIndexEvalRuntimeKeys` only stores each rescan's datums into it.
- `btrescan` reuses the scan's one `BTScanOpaque`.

**Change** (`internal/executor`):

- **`indexProbeForm`** (`index_probe_form.go`) is a per-operator cache:
  - `colAt` resolves key position i's column once.
  - `form` is `ctx.indexProbeKey`. The first probe it forms is kept as a template when its attributes are all
    fixed-width and it has no null bitmap (`keepTemplate`). From then on, a probe with the template's attribute
    count is the template copied, with each value's `encodeValuePG` image written at its fixed offset (`fill`).
  - A value whose image is not the template's width, a TOAST pointer, or a different attribute count falls back
    to `indexProbeKey`.
  - The bytes are the same because a fixed-width, null-free index tuple's header and layout do not depend on the
    values (the reasoning behind slice 56's `groupBoundsFixed`).
- **Both siblings.** `indexScanOp` and `indexOnlyScanOp` use it in `lookupKey` and `lookupKeys`.
- **One cursor.** `indexScanOp`'s equality and range rescan re-aims one cursor (`curSpare`,
  `BTree.ResetScanCursor` from slice 77) instead of allocating a cursor per rescan.

**Pins.**

- `TestIndexProbeFormMatchesIndexProbeKey` checks that the form's bytes equal `indexProbeKey`'s on every call:
  one- and two-attribute prefixes of an int index (templated), alternating attribute counts, and a prefix holding
  a varchar (never templated, `tmpl == nil` asserted).
- Writing every image at the first attribute's offset fails it.

**Measured** against `71a749a8e`:

| query | HEAD | candidate |
|---|---|---|
| Q14 instructions | 151.33 G | 143.95 G (−4.9 %) |
| Q14 allocation per run | 5.15 GB, 58.4 M objects | 4.47 GB (−13 %), 46.0 M (−21 %) |
| Q17 | 2.804 G | 2.677 G (−4.5 %) |
| Q25 | 2.800 G | 2.719 G (−2.9 %) |
| Q72, Q54 | — | within ±0.7 % |

Q14's wall time is noisy (8–12 s per run): the run still allocates 4.5 GB.

Values are identical:

- sf025 PASS=99 (61 ck) with identical plan shapes, and PASS again with the deform poison armed;
- the executor package passes with the poison armed;
- acceptance 24 MATCH;
- RegressSuite PASS;
- TestPort\_Isolation PASS.

**Left.**

- **The leaf walk.** `leafScanStart`'s binary search through the generic comparator (`compare` →
  `comparePGIndexTuples`) is about 12 % of Q14.
- **The descent.** It is per rescan, as in PG, but costs more per level.
- **Allocation.** Q14 still allocates 4.5 GB and 46 M objects per run.

## Slice 81 — a re-opened inner index scan keeps its batch, and a set-op drain forms one key per distinct row

**Target.** Q14's allocation: 4.46 GB and 46.0 M objects per run after slice 80. I attributed allocation samples
(the first non-runtime frame above `mallocgc` / `growslice`) in a 20 s profile. The top sources:

1. `indexScanOp.scanAppendEntry`, 80 % of it `growslice`.
2. `decodePhysicalPGValueLowered`: string columns decoded with no arena, one Go string each.
3. `drainSetOpInput`: `appendRowKeyTrimmed`'s `strconv` growth, one key string per row.

**Diagnosis.**

- **Batch regrowth.** The lateral join stream re-opens its right subtree for every outer row (`stepOuter` →
  `right.Open`, `finishOuter` → `Close`). `indexScanOp.Open` and `Close` both set the TID batches `tids` / `poss` to
  nil, so every probe regrew both from zero, about 7 doublings for Q14's 42-row probes. PG's `ExecReScan` keeps the
  scan state.
- **Wasted keys.** `computeBuffered` drains both set-op inputs with `drainSetOpInput`, which builds a key string for
  every row. The left input's counts are discarded (`leftRows, _, err`). The right input's 190 k rows have 3.8 k
  distinct keys.

**Change** (`internal/executor`):

- **Keep the batch.** `indexScanOp.Open` / `Close` truncate `tids` / `poss` to length 0 instead of setting them to
  nil.
- **One key per distinct row.** `drainSetOpInput` takes `wantCounts`, so the left input forms no keys. The right
  input forms each key in one reused buffer, looks it up without a conversion in a key → slot index, and increments
  the slot. A key string is allocated once per distinct key, and the map it returns is the same.
- **Key format unchanged.** The integer key stays decimal text: `appendRowKeyTrimmed` canonicalises an int and a
  numeric spelling of one value to one key (`1` ≡ `1.00`).

**Pins.**

- `TestDrainSetOpInputCountsMatchRowKeys`: the counts equal the per-row `rowKeyTrimmed` counts on a fixture whose
  7 rows collapse to 4 keys through int/numeric, bpchar-trim and NULL canonicalisation. A drain without counts
  returns every row and no map.
- Not incrementing a duplicate fails it.

**Measured** against `afdfe9869`:

| query | HEAD | candidate |
|---|---|---|
| Q14 objects allocated per run | 45.96 M | 32.57 M (−29 %) |
| Q14 bytes allocated per run | 4.46 GB | 4.20 GB (−6 %) |
| Q14 instructions | 143.99 G | 139.86 G (−2.9 %) |
| Q38, Q87 | 12.23, 12.46 G | 12.10, 12.33 G (−1.1 %) |
| Q8 | — | flat |

Values are identical:

- sf025 PASS=99 (61 ck) with identical plan shapes, and PASS again with the deform poison armed;
- the executor package passes with the poison armed;
- acceptance 24 MATCH;
- RegressSuite PASS;
- TestPort\_Isolation PASS.

**Left** (ledger row).

- **Index scans decode dead string columns.** An index scan decodes every column up to its deform bound with no
  arena, so `item_pkey`'s probes copy `i_item_id`, `i_item_desc`, `i_brand` and `i_class` into new strings per row.
  The seq scan walks past dead columns (`colTypeInfo.skip`, `readCols`), but the read set reaches only a scan
  directly under a Project. An index scan on a nested loop's inner side has no such parent; it needs a join-aware
  read set.
- **Bytes per run.** The 4.2 GB per run is dominated by these decoded rows and the sort and set-op row copies.
