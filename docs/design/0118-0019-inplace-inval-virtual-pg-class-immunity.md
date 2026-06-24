# 0118-0019 — `inplace-inval` passes by construction (virtual pg_class immunity)

**Milestone:** M0118-0009 (Misc / system-level isolation specs)
**Spec:** `postgres/src/test/isolation/specs/inplace-inval.spec`
**Status:** accepted — spec promoted `failed` → `pass`
**Go test:** `TestPort_IsolationInplaceInval` (`internal/testport/isolation_port_test.go`)

## What the upstream spec tests

`inplace-inval.spec` is a regression test for a real PostgreSQL bug
(commit history: an inplace update could abort before sending its inplace
invalidation message to the shared queue). The scenario:

1. `cachefill3` — `TABLE newly_indexed` populates the `pg_class` row for the
   table in the backend's **catcache**.
2. `cir1` — `BEGIN; CREATE INDEX i1 …; ROLLBACK;` sets `pg_class.relhasindex=true`
   via `heap_inplace_update`, then the rollback was supposed to discard the
   cache invalidation.
3. `cic2` — a second `CREATE INDEX i2` sees `relhasindex` already `true` and
   skips changing it (so it sends **no** invalidation).
4. `ddl3` — `ALTER TABLE … ADD extra int` performs a normal `heap_update` whose
   *oldtup* is fetched from the now-stale catcache row — which in the buggy
   version still carried the pre-inplace value, reverting `relhasindex` to
   `false`.
5. `read1` — observes the damage: `relhasindex` would read `f` instead of `t`.

The expected output has `read1` returning `relhasindex = t` in **both**
permutations (the bug is fixed upstream).

## Why goopg already matches — architectural immunity

goopg serves `pg_class` from its **virtual catalog builder**, not from a heap
relation (see memory note *pg_class is virtual, pg_attribute is heap*). The
`relhasindex` column is recomputed **live on every read** from the in-memory
index set:

```go
// internal/catalog/catalog.go (virtual pg_class row builder)
hasIdx := "f"
if len(c.byTable[t.OID]) > 0 {
    hasIdx = "t"
}
```

Consequently the entire upstream hazard chain is absent in goopg:

- **No heap tuple for `pg_class`** — there is nothing for `heap_update` to
  rewrite, and no on-disk `relhasindex` byte to be reverted.
- **No catcache oldtup** — `cachefill3` cannot capture a tuple that later goes
  stale; goopg derives the value fresh each time.
- **No `heap_inplace_update` path** — `CREATE INDEX` registers the index in the
  in-memory catalog (`c.byTable`); it does not perform an inplace byte patch
  that a subsequent `heap_update` could clobber.

So the result is determined purely by *whether an index currently exists* on the
table:

- `cir1` creates `i1` inside a transaction and **rolls back** → no index remains.
- `cic2` creates `i2` and **commits** → `c.byTable[oid]` is non-empty.
- `ddl3` (`ALTER … ADD COLUMN`) does not touch the index set.
- `read1` → `len(c.byTable[oid]) > 0` → `relhasindex = t`. ✓

Both permutations therefore observe `relhasindex = t`, byte-identical to the PG
18.3 expected output.

## Scope / risk

No code change. This loop only:

- adds the dedicated sequential test `TestPort_IsolationInplaceInval` (mirrors
  the other M0118-0009 dedicated tests; no `t.Parallel`, fresh cluster), and
- promotes the inventory CSV row `failed` → `pass` with a comma-free rationale,
- regenerates `upstream-isolation-coverage.md` and the oracle inventory md.

Risk is nil: no executor/planner/codec/WAL/MVCC behavior changed. The
"immunity" holds as long as `pg_class` stays virtual and `relhasindex` stays
derived from `c.byTable`; if a future change moves `pg_class` to a heap or
caches its rows, this spec must be re-verified (the upstream hazard would then
become reachable).

## Gates

- `go test -run TestPort_IsolationInplaceInval ./internal/testport/` — PASS
  (both permutations byte-match expected).
- Coverage md regenerated via `cmd/gen-isolation-coverage`; oracle inventory md
  regenerated via `cmd/gen-oracle-inventory`.
