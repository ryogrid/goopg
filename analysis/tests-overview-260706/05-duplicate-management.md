# 05 — Duplicate / Overlap Management

Snapshot 2026-07-06. What was noticed about duplicated or overlapping test
tracking during this survey, and what a regression batch must do about it.

---

## A. There is no dedicated dedup doc or script

Searches for `duplicat|overlap|redundant|dedupe|test overlap` across `docs/`,
`analysis/`, `scripts/`, `cmd/` found **no dedicated duplicate-tracking document
and no dedup tooling.** Matches were only (a) unrelated feature design docs
(btree deduplication, deferred-unique NULLS NOT DISTINCT, etc.) and (b) prose
"also ported / verified by" cross-references inside `rationale` columns.

So overlap is managed **implicitly**, three ways:

### 1. `id` uniqueness in the port-status CSV (enforced)
`docs/test-port/postgres-oracle-port-status.csv` cannot list the same upstream
test under two ids: `internal/testport/framework/status.go::ValidateStatusRows`
rejects duplicate ids, regression-tested by
`framework/status_test.go::TestValidateRejectsDuplicateID`. This is the one
*mechanical* dedup guarantee.

### 2. Cross-form linkage via `rationale` prose (not structured)
When one upstream spec is covered both as a `TestPort_*` Go test **and** tracked
in a coverage CSV, the `rationale` names the Go function and back-references the
upstream case, e.g. isolation rows read:

> `… Promoted to pass-required via runIsoSpecStrict TestPort_IsolationAlterTable1
> (internal/testport/isolation_port_test.go); matches PG 18.3 byte-for-byte …
> (case alter-table-1)`

This `(case <name>)` suffix is the de-facto mapping between the Go port and the
upstream regress/isolation case — but it is **free text, not a structured join
column.**

### 3. Intentional presence in multiple inventories (by design)
The same upstream area deliberately appears in more than one inventory at
different granularities:

| Roll-up (authority) | Per-item expansion |
|---------------------|--------------------|
| `postgres-oracle-port-status.csv` — one `defer` row per suite (e.g. recovery, subscription) | `postgres-oracle-target-inventory.csv` — 47 recovery-tap + 36 subscription-tap rows |
| single `regress` row (D-001) | 232 `regress-sql` rows + 265 `regress-expected` rows |
| single `isolation` row | 121 `isolation-specs` rows |

The regress directory alone is intentionally counted under two `suite_id`
buckets (`regress-sql` = the `.sql` inputs, `regress-expected` = the expected
outputs) — these describe the *same* cases from two sides.

---

## B. What a regression batch must do to avoid double-counting

1. **Treat `postgres-oracle-port-status.csv` as the roll-up authority** for
   "must pass," and **`postgres-oracle-target-inventory.csv` as the per-item
   expansion.** Do not sum "must-pass" across both — a batch that counts the
   `defer` recovery roll-up row *and* the 47 recovery-tap item rows counts the
   same work twice.
2. **Join the two inventories on `upstream_path` / `item_path`**, not on `id`
   (the ids differ between roll-up and expansion).
3. **Do not run a spec twice across runners.** A spec ported as a
   `TestPort_Isolation*` Go test is *the same test* as the `isolation-specs`
   entry, and a regress case run via `TestPort_RegressSuite` is the same as its
   `regress-sql` inventory row. Pick one execution path per spec (the Go
   `testport` entry is the runnable one; the CSV rows are tracking metadata).
4. **Reconcile `regress-sql` vs `regress-expected`** as one logical case per
   test name, not two.

---

## C. Suggested (not implemented) improvement to surface later

If the future batch needs machine-checkable dedup rather than prose:
add a structured `covered_by` / `go_test` column to the coverage CSVs (currently
the Go-func linkage lives only in `rationale` text), so the roll-up ↔ per-item
↔ Go-test mapping can be joined programmatically instead of parsed from
`(case <name>)` suffixes. This is a note for the batch design phase, **not part
of this information-gathering task.**
