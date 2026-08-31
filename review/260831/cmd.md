# cmd/ tools — Code Review 2026-08-31

Files: diag/main.go, estimate-audit/main.go, gen-information-schema-procs/main.go, gen-information-schema-views/main.go, gen-isolation-coverage/main.go, gen-kwlist-go/main.go, gen-nailed-view-tables/main.go, gen-oracle-inventory/main.go, gen-oracle-report/main.go, gen-pg-operator-data/main.go, gen-pg-proc-data/main.go, gen-pg-type-data/main.go, gen-planner-flag-labels/main.go, gen-regress-coverage/main.go, gen-sqlstate/main.go, gen-tap-coverage/main.go, gen-tokennums-go/main.go, goopg/main.go, goopg/standby.go, parsetest/main.go, plan-snapshot/main.go, tpch-runner/digest.go, tpch-runner/digestdiff.go, tpch-runner/main.go, validate-ralph-state/main.go
Findings count: 7

### `cmd/validate-ralph-state/main.go:loadStatus` — JSON decoded twice for the same bytes
- **Issue**: `loadStatus` reads the file once but calls `json.Unmarshal(b, &v)` (typed struct) and then `json.Unmarshal(b, &raw)` (generic `map[string]any`) on the same `b` — the JSON document is tokenized/parsed twice.
- **Why**: The file is small and this is a CLI validator, so the cost is negligible, but the second decode is pure duplicate work; a single decode into `map[string]any` followed by field extraction (or a single decode into a struct plus a separate small map only for fields not in the struct) would do.
- **Suggestion**: Decode once. If only `timestamp`/`status` need to survive as raw, unmarshal into the struct and marshal back; or use `json.RawMessage`/`DisallowUnknownFields` and build the raw object from the typed struct. At minimum, hoist the shared `b` read (already done) and decode only what is needed.
- **Severity**: low

### `cmd/validate-ralph-state/main.go:loadProgress` — JSON decoded twice for the same bytes
- **Issue**: Same double-`json.Unmarshal` pattern as `loadStatus` (lines 295-299).
- **Why**: Duplicate parse of the same buffer for a trivial two-field file.
- **Suggestion**: Same as `loadStatus` — single decode.
- **Severity**: low

### `cmd/validate-ralph-state/main.go:autoRepair` — `parseTimestamp` called twice on the same values
- **Issue**: `statusTS, statusErr := parseTimestamp(status.Timestamp)` and `progressTS, progressErr := parseTimestamp(progress.Timestamp)` are computed at the top of `autoRepair` (lines 117-118), and then `validate()` (called by the caller, lines 52/66) recomputes exactly the same two `parseTimestamp` calls (lines 220-221) for the same `status`/`progress`.
- **Why**: `validate` runs before and after `autoRepair` with the same inputs, so timestamp strings are parsed up to three times per invocation. The strings are short, so the absolute cost is trivial, but it is duplicated work that could be parsed once and passed around.
- **Suggestion**: Parse both timestamps once (e.g. in `main` or a small result struct) and thread them through `validate`/`autoRepair` instead of re-parsing in each.
- **Severity**: low

### `cmd/tpch-runner/main.go:drainRows` — per-row slice allocation in the EXPLAIN path
- **Issue**: In the `doExplain` branch of the row loop, `vals := make([]interface{}, len(cols))` and `ptrs := make([]interface{}, len(cols))` are allocated on **every** row iteration (lines 314-315).
- **Why**: TPC-H EXPLAIN outputs are tens to hundreds of rows, and this is a benchmark tool, so it is not hot production code — but the digest path in the same function deliberately hoists `raw`/`targets` outside the loop (lines 304-306) while the EXPLAIN path reallocates each pass. Also `line += fmt.Sprintf("%v", v)` uses string concatenation in the inner loop instead of a `strings.Builder` or `strconv.Append*`.
- **Suggestion**: Hoist `vals`/`ptrs` above the loop and reuse them (reset each iteration), mirroring the `raw`/`targets` pattern already present. Use a `strings.Builder` (or `bytes.Buffer`) for the line assembly.
- **Severity**: low

### `cmd/diag/main.go:main` — per-row slice allocation
- **Issue**: `vals := make([]interface{}, len(cols))` is allocated on every `rows.Next()` iteration (lines 39-43), and the per-column `&v` interface boxes are recreated each row.
- **Why**: This is a tiny interactive debug helper that prints at most 10 rows, so the cost is immaterial; noted only for completeness of the criteria. If it ever drove many rows it would allocate once per row.
- **Suggestion**: Hoist the slice construction above the loop and reset it per row.
- **Severity**: low

### `cmd/tpch-runner/main.go:selectQueries` — dead sorted-copy computation
- **Issue**: `selectQueries` (lines 439-444) builds `sortedCopy := append([]int(nil), out...)`, sorts it, then discards it with `_ = sortedCopy` — a fully dead sort of a copy of the input.
- **Why**: Dead work: the sorted copy is never used (the comment says "Keep user-supplied order for transparency" and nothing consumes the sorted result). The sort and the copy allocation are pure waste.
- **Suggestion**: Delete the `sortedCopy` block entirely (it is only there to appear to sort). If a sorted form is actually needed for some gate, return it and use it; otherwise remove.
- **Severity**: low

### `cmd/gen-pg-operator-data/main.go` — pg_operator.dat parsed twice (two full passes)
- **Issue**: `parseOperatorDatFirstPass` and `parseOperatorDat` each call `os.ReadFile("pg_operator.dat")`, `splitBlocks`, and `parseKV` over the entire file (lines 248-320); the file is fully tokenized twice, and both passes re-`resolveType` the operator's own left/right types.
- **Why**: This is a one-shot code generator (build-tag `ignore`, run by hand), so the absolute cost is irrelevant; the two-pass shape exists to resolve cross-references (`oprcom`/`oprnegate`) that can point at operators defined later in the file. Still, it is the clearest case of "duplicate work" in the cmd/ tree.
- **Suggestion**: Parse the blocks once into memory and reuse the `map[string]string` list for both passes, or build the key map in a single pass and only do a light second pass that reuses the cached block/`parseKV` results rather than re-reading and re-splitting the file.
- **Severity**: low

---

## Files with no significant issues

- **estimate-audit/main.go** — per-query `EXPLAIN ANALYZE` and `strings.Join` accumulation are appropriately lightweight; the stats warmup loop is intentional (per-connection stats). No wasteful processing.
- **goopg/main.go** — startup-path GUC reads via `boolGUC`/`stringGUC`/`intGUC` are map lookups per call, but they run once at boot (not per request); the pprof listener, checkpointer, and drain loops are all idiomatic. No wasteful processing.
- **goopg/standby.go** — ticker-based poll loops (`drainPollInterval`, `promoteSignalPollInterval`) are deliberate anti-busy-wait designs; `walDirFor` reuse avoids drift. No issues.
- **plan-snapshot/main.go** — `readSnapshot` uses `strings.Builder`; `planEqual`/`extractCosts` regex use is proportionate to a CLI diff tool. No issues.
- **tpch-runner/digest.go** — buffer reuse (`d.buf = d.buf[:0]`), stack-allocated `hex16`, and hoisted scan targets are already done well. No issues.
- **tpch-runner/digestdiff.go** — single-pass log parsing, reused slices. No issues.
- **gen-information-schema-procs/main.go**, **gen-information-schema-views/main.go**, **gen-isolation-coverage/main.go**, **gen-kwlist-go/main.go**, **gen-nailed-view-tables/main.go**, **gen-oracle-inventory/main.go**, **gen-oracle-report/main.go**, **gen-pg-proc-data/main.go**, **gen-pg-type-data/main.go**, **gen-planner-flag-labels/main.go**, **gen-regress-coverage/main.go**, **gen-sqlstate/main.go**, **gen-tap-coverage/main.go**, **gen-tokennums-go/main.go**, **parsetest/main.go** — all one-shot code/report generators run by hand or in CI; their inputs (manifests, catalogs, CSVs) are tiny and they use `strings.Builder` where output accumulates. Any redundancy there (e.g. `gen-pg-proc-data` reading `proargtypes` from the map three times, `gen-pg-type-data` rebuilding `oidByTypname`) is execution-time immaterial for a hand-run generator. No actionable findings.
