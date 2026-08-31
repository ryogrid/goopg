# cmd/ tools — Bug Review 2026-08-31

Files: 25 files in cmd/
Findings count: 6

## No-bug files (trivial dev tools, no issues found)

The following files are small dev/one-shot tools with no bugs to report:

- `cmd/diag/main.go` — debug helper, no bugs
- `cmd/parsetest/main.go` — minimal parse test, no bugs
- `cmd/gen-planner-flag-labels/main.go` — trivial single-call wrapper, no bugs
- `cmd/gen-tokennums-go/main.go` — simple code generator, no bugs
- `cmd/gen-kwlist-go/main.go` — code generator, no bugs
- `cmd/gen-sqlstate/main.go` — code generator, no bugs
- `cmd/gen-information-schema-procs/main.go` — code generator, no bugs
- `cmd/gen-information-schema-views/main.go` — code generator, no bugs
- `cmd/gen-nailed-view-tables/main.go` — code generator, no bugs
- `cmd/gen-oracle-inventory/main.go` — report generator, no bugs
- `cmd/gen-oracle-report/main.go` — report generator, no bugs
- `cmd/gen-pg-proc-data/main.go` — code generator, no bugs
- `cmd/gen-pg-type-data/main.go` — code generator, no bugs
- `cmd/gen-regress-coverage/main.go` — coverage report generator, no bugs
- `cmd/validate-ralph-state/main.go` — state validator, no bugs
- `cmd/tpch-runner/digest.go` — result digest, no bugs
- `cmd/tpch-runner/digestdiff.go` — digest diff, no bugs

---

### `cmd/goopg/standby.go:standbyController.Promote` — `promoting` atomic never reset, failed promote permanently wedges

- **Bug**: `Promote()` sets `sc.promoting` via `CompareAndSwap(false, true)` at line 169, but never resets it to false after completion. After a failed promote, `promoted` stays false while `promoting` stays true. Every subsequent `Promote()` call hits the CAS guard at line 169 and returns `"promotion already in progress"` without ever reaching `promoteOnce.Do` or the stored error. The `promoteOnce` is also consumed (sync.Once runs the function once regardless of error), so even if `promoting` were reset, the promotion function would never re-execute.
- **When it triggers**: Any `Promote()` call that fails (e.g. drain timeout, receiver error, history write error). After that, the standby can never be promoted again without a process restart. The `promoteSignalWatcher` removes the signal file before calling `Promote`, so even a file-based trigger cannot retry.
- **Fix**: Reset `sc.promoting.Store(false)` after `runPromote` returns (or in `promoteOnce.Do`'s deferred cleanup). Also, `promoteOnce` should be replaced with a manual check so a failed promotion can be retried, or `promoteOnce` should be defended with a `promoting` reset that falls through to the stored error.
- **Severity**: **high**

---

### `cmd/goopg/standby.go:Promote` — `sc.replayer.ApplyLSN()` called without nil guard at line 248

- **Bug**: The drain loop at line 210-215 guards `sc.replayer != nil` before calling `ApplyLSN()`, but the log line at line 248 calls `sc.replayer.ApplyLSN()` without a nil check. While `sc.replayer` is always non-nil in normal operation (set by `startStandby`), the inconsistency suggests a latent nil-deref — and the guard elsewhere proves the author considered nil possible.
- **When it triggers**: If `startStandbyReplayer` returned nil for some reason, or if `replayer` were ever set to nil between the drain loop and the final log.
- **Fix**: Add a nil guard or early return before line 248.
- **Severity**: **low**

---

### `cmd/plan-snapshot/main.go:planEqual` — `rowsRegexp` never matches the actual EXPLAIN format

- **Bug**: The `rowsRegexp` is `\s*\(rows=(\d+)\)`, which requires a **standalone** `(rows=N)` parenthetical. But both goopg (`internal/executor/operators_explain.go:493` `"(cost=0.00..0.00 rows=%d width=0)"`) and PG emit `rows=N` as a field *inside* the `(cost=...)` parenthetical, never as `(rows=N)` by itself. Verified: `rowsRegexp.ReplaceAllString("... (cost=0.00..0.00 rows=5 width=0)", "")` strips nothing and `extractCosts` returns `[]`. This means:
  - `structural` mode: `ReplaceAllString(a, "")` strips nothing, so structural comparison is effectively `strict-text` — cost variance is NOT ignored as the mode advertises.
  - `semantic-cost` mode: both `extractCosts(a)` and `extractCosts(b)` return empty lists, so `len(ca) != len(cb)` is false and the tolerance loop never executes — the ±10% cost tolerance check is never applied.
- **When it triggers**: Every run. The modes silently degrade to strict-text without any warning.
- **Fix**: Change the regex to match the embedded form, e.g. `\s*rows=(\d+)` (and the actual/estimated variants), and update `extractCosts` accordingly. Note `rows=%.2f` (float) appears in ANALYZE `actual` lines, so a `rows=(\d+(\.\d+)?)` pattern is needed.
- **Severity**: **medium** (dev tool, but the modes' documented guarantees are silently broken)

---

### `cmd/gen-isolation-coverage/main.go:loadCSV`, `cmd/gen-tap-coverage/main.go:loadCSV`, `cmd/gen-regress-coverage/main.go:loadCSV` — `row[statusIdx]` out-of-bounds access on malformed CSV

- **Bug**: All three `loadCSV` functions access `row[statusIdx]` (or `rec[statusIdx]`) without checking `statusIdx < len(row)`. The code checks `kindIdx >= len(row)` and `itemPathIdx >= len(row)` but not `statusIdx`. If a CSV row has fewer columns than the header (e.g. a truncated row), `statusIdx` may be >= `len(row)`, causing a panic.
- **When it triggers**: A malformed CSV input where a row is shorter than the header. In practice, the CSV is checked in, so this is unlikely, but the guard is missing compared to the `itemPathIdx` check done immediately before it.
- **Fix**: Add `statusIdx < len(row)` guard before accessing `row[statusIdx]`.
- **Severity**: **low** (dev tools, controlled input)

---

### `cmd/estimate-audit/main.go:selectQueries` — `Atoi` errors silently ignored, allowing negative query numbers

- **Bug**: Range parsing: `lo, _ := strconv.Atoi(...)` and `hi, _ := strconv.Atoi(...)` ignore parse errors. A spec like `-5` (i=0, `i > 0` false, so falls through to plain number parsing) results in `strconv.Atoi("-5")` = -5, which is added to `seen` map. Then `queryFor(-5)` returns `Q-5` and `tpch.Queries()[-5]` returns `""` → handled as "query SQL not found". But this is an undiagnosed silent misconfiguration.
- **When it triggers**: User passes `--queries -5` or `--queries 5-` (empty hi). The tool proceeds without error.
- **Fix**: Validate Atoi results and report parse errors.
- **Severity**: **low** (dev tool, edge case)

---

### `cmd/gen-pg-operator-data/main.go:parseOperatorDat` — right-unary operator kind ('r') not handled

- **Bug**: `kind` defaults to `byte('b')` and only changes to `'l'` when `oprkind == "l"`. PostgreSQL supports three operator kinds: 'b' (binary), 'l' (left unary), and 'r' (right unary). Right-unary operators would be incorrectly classified as binary. While pg_operator.dat currently has no 'r' operators, this is a latent correctness gap.
- **When it triggers**: If a future upstream version introduces a right-unary operator, or if the oracle tree is updated.
- **Fix**: Add `case "r": kind = 'r'`.
- **Severity**: **low** (dev tool, latent issue; no 'r' operators in PG18 core)

---

## Summary

| # | File | Finding | Severity |
|---|------|---------|----------|
| 1 | `cmd/goopg/standby.go` | `promoting` never reset → failed promote permanently wedges | high |
| 2 | `cmd/goopg/standby.go` | `sc.replayer.ApplyLSN()` nil deref potential | low |
| 3 | `cmd/plan-snapshot/main.go` | `rowsRegexp` requires standalone `(rows=N)` but rows is embedded in `(cost=...)` → structural/semantic-cost modes broken | medium |
| 4 | `cmd/gen-*-coverage/main.go` (3 files) | `row[statusIdx]` out-of-bounds on malformed CSV | low |
| 5 | `cmd/estimate-audit/main.go` | `selectQueries` Atoi errors ignored → negative query numbers | low |
| 6 | `cmd/gen-pg-operator-data/main.go` | right-unary operator kind 'r' not handled | low |