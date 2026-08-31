# Code Review TODO — 2026-08-31

Review scope: ALL Go source in the goopg codebase (cmd/ + internal/), **excluding** `*_test.go`
and test-support packages (`internal/testutil/`, `internal/testport/`).

Review focus (per user request):
1. Obviously wasteful processing (明らかに無駄な処理).
2. More efficient processing methods **without major logic changes** (ロジックを大きく変えない範囲で).

Method: each subsystem was reviewed by a subagent, which wrote its findings to
`review/260831/<subsystem>.md` as it worked. The orchestrator spot-checked a sample of
findings (executor/storage/nbtree/xlog/optimizer) via the Serena MCP server.

## Subsystem groups — all COMPLETE

| # | Group | Files | Output file | Findings | Status |
|---|-------|-------|-------------|----------|--------|
| 1 | executor: core (expr/operator/datum/slot/codec/join/instrument/etc.) | 36 | `executor-core.md` | 23 | [x] |
| 2 | executor: operators_*.go (analyze → generate_series) | 24 | `executor-operators-1.md` | 15 | [x] |
| 3 | executor: operators_*.go (index → window) + misc | 36 | `executor-operators-2.md` | 26 | [x] |
| 4 | executor: sys_pg_*, sys_catalog_*, pgindex_*, pgstat_*, parallel_*, pg_* | 82 | `executor-sys.md` | 23 | [x] |
| 5 | optimizer (part 1: cardinality → exprwalk) | 20 | `optimizer-1.md` | 14 | [x] |
| 6 | optimizer (part 2: flaglabels → with) | 70 | `optimizer-2.md` | 34 | [x] |
| 7 | access/transam/xlog | 61 | `xlog.md` | 70 | [x] |
| 8 | initdb | 57 | `initdb.md` | 14 | [x] |
| 9 | storage + aio + lmgr + file | 35 | `storage.md` | 23 | [x] |
| 10 | nbtree + amcheck + pglz + backup + commands/vacuum | 34 | `nbtree-amcheck.md` | 22 | [x] |
| 11 | transam (clog/mvcc/snapshot/visibility/subxact/ssi) + multixact + control | 21 | `transam.md` | 14 | [x] |
| 12 | parser + analyzer + sqlkeywords + nodes + plpgsql | 42 | `parser-nodes.md` | 18 | [x] |
| 13 | catalog + postmaster + autovacuum + replication + libpq + auth + port/gls + port/runtimeshim | 61 | `catalog-postmaster.md` | 19 | [x] |
| 14 | utils (misc, adt/datetime, mb, activity, errcodes, mmgr, similarto, array) | 28 | `utils.md` | 24 | [x] |
| 15 | cmd/ (all 25) | 25 | `cmd.md` | 7 | [x] |

**Totals: 632 source files reviewed, 346 findings recorded.**

(Remaining unclassified: the executor `Files:` headers in some reports were abbreviated;
counts above are the exact per-group file counts from the dispatcher's file lists.)

## Notes / rule-out log

- **Test-support packages excluded by request**: `internal/testutil/`, `internal/testport/`
  (test infra, not production code). `*_test.go` files excluded by request.
- **Generated / seed files** were reviewed but flagged as low-priority (one-shot bootstrap
  code, hand-run generators): `initdb/*_seed_*.go`, `*_bootstrap.go`, `cmd/gen-*`,
  `catalog/pg_operator_seed_data.go`, `parser/keywords_gen.go`, `parser/tokennums_gen.go`,
  `parser/yacc_parser.go` (generated LALR parser), `auth/saslprep_tables.go`,
  `port/runtimeshim/*_linkname.go` + `*_fallback.go` shims.
- **Sample spot-checks (via Serena) — all confirmed accurate**:
  - `storage/heap.go:CollectDeadHeapSlots` uses copy-allocating `ParseHeapTuple` where
    header-only `parseHeapTupleAlias` suffices (storage.md).
  - `nbtree/btree.go:Search` decodes the whole leaf via `pageItems` per point lookup
    (nbtree-amcheck.md).
  - `executor/copy_binary.go:decodeNumericBinary` computes a dead `fullMantissa` before an
    unconditional `decodeNumericBinaryViaBig` fallback (executor-core.md).
  - `xlog/iterator.go:readOneAt` reads the header bytes and then the full record body,
    re-reading the header region (xlog.md).
- **No high-severity findings** — no correctness bugs or blocking performance issues; the
  ~30 medium-severity findings are all in hot paths and are the best targets for follow-up.

## Follow-up candidates (highest value first)

1. `pglz.Compress` — quadratic brute-force matcher (nbtree-amcheck.md).
2. `storage/heap.go` tuple-decode paths — use `parseHeapTupleAlias` where only the header is
   needed (`CollectDeadHeapSlots`, `PageAllVisible/PageAllFrozen`, `pagePruneCore`).
3. `xlog` — `readOneAt` double header read; `encodeRecordXLog`/`pg_assembled_emit` alloc
   chains; `reader.readStreamFrom` O(N²) segment growth.
4. `executor/expr.go:evalCast` — per-row `strings.ToLower` on target type.
5. `executor/applyworker.go` — per-row column-map rebuild; cache per-relation mapping.
6. `executor/copy.go` — use the existing row pool instead of per-row `make(Row, …)`.
7. `optimizer/cardinality.go` — `EstimateRows` no memoization (O(N²) on left-deep chains).
8. `transam` — per-snapshot `abortedXIDs` copies; goroutine-per-wait in `WaitForXID`.
9. `utils/encoding_guc` — re-cleans constant encoding table per lookup.
