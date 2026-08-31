# 0118-0082 — `alter-table-4` PROMOTED: inherited-column type re-validation after locking the child (M0118-0008 perm 4)

**Status:** accepted
**Milestone:** M0118-0008 (upstream isolation spec suite pass-through)
**Spec:** `postgres/src/test/isolation/specs/alter-table-4.spec` — *Add and remove inheritance with concurrent reads*
**Scope:** **Promotion.** Closes the spec — all four permutations byte-for-byte vs PG 18.3 via `TestPort_IsolationAlterTable4` (`runIsoSpecStrict`), on top of 0118-0080 (perms 1 & 2) and 0118-0081 (perm 3).

## The spec (perm 4)

```
permutation s1b s1delc1 s1modc1a s2sel s1c s2sel
```

- `s1b` — `BEGIN`
- `s1delc1` — `ALTER TABLE c1 NO INHERIT p` (deferred to commit, 0118-0080)
- `s1modc1a` — `ALTER TABLE c1 ALTER COLUMN a TYPE float`
- `s2sel` — `SELECT SUM(a) FROM p` → **`<waiting ...>`**
- `s1c` — `COMMIT`
- `s2sel` — `<... completed>` → **`ERROR:  attribute "a" of relation "c1" does not match parent's type`**
- `s2sel` — `SELECT SUM(a) FROM p` → sum = 1 (c1 detached, only p's row)

Spec comment: *"this case currently results in an error; doesn't seem worth preventing."*

The load-bearing PG semantics: `s2sel` identifies `p`'s children (still `c1`, since
`NO INHERIT` has not committed) **before** locking them, then blocks acquiring
`AccessShare` on `c1` behind `s1`'s `AccessExclusiveLock`. Once `s1` commits and the
lock is granted, planning continues into `make_inh_translation_list`
(`optimizer/util/appendinfo.c`), which matches each parent attribute to the child by
name and verifies the types still agree:

```c
/* Found it, check type and collation match */
if (atttypid != att->atttypid || atttypmod != att->atttypmod)
    ereport(ERROR,
            (errcode(ERRCODE_INVALID_COLUMN_DEFINITION),
             errmsg("attribute \"%s\" of relation \"%s\" does not match parent's type",
                    attname, RelationGetRelationName(newrelation))));
```

Column `a` is now `float` on `c1` but `integer` on `p` → the error fires, with the
**parent's** attribute name (`a`) and the **child's** relation name (`c1`).

## The gap in goopg

goopg keeps one shared catalog with no per-session MVCC, so `s1modc1a`'s
`ALTER COLUMN a TYPE float` mutates `c1`'s column type in place immediately
(`execAlterColumnType`, `operators_ddl.go`). The inheritance-child `SeqScan` from
0118-0081 already (a) is identified at plan time, (b) waits on `c1`'s lock in
`acquireScanReadLockTxn`, and (c) skips the child if it vanished. But nothing
re-checked the child's **column types** against the parent after the lock was
acquired, so `s2sel` simply scanned `c1` and returned `sum = 1` (the detached child
contributes nothing post-commit) instead of erroring. First divergence was spec L65.

## The fix

Re-validate inherited column types at the child scan's `Open`, exactly where (and
when) PostgreSQL's `make_inh_translation_list` runs — **after** the child's lock is
acquired (i.e. after any concurrent `ALTER` on the child has committed), so the
error appears post-`<... completed>` rather than immediately.

1. **`planner.SeqScan.InheritParentOID`** (`plan.go`) — set to the parent OID on
   every inheritance-child scan, alongside the existing `SkipIfVanished`
   (`planner.go`, the `allDesc` expansion loop). Zero on every non-inheritance scan.

2. **`seqScanOp.inheritParentOID`** (`operators_storage.go`) — copied in
   `newSeqScanOp`. In `Open`, inside the existing post-lock `skipIfVanished` block
   (which already proves the child still exists), when `inheritParentOID != 0` the
   scan looks up the parent and calls `validateInheritedColumnTypes`.

3. **`validateInheritedColumnTypes(im, parent, child)`** — mirrors
   `make_inh_translation_list`: for each non-dropped parent column it finds the child
   column of the same name and compares **canonical type class**; a mismatch returns
   `ExecError{Code: "42611" (ERRCODE_INVALID_COLUMN_DEFINITION),
   Message: 'attribute "<parent attr>" of relation "<child rel>" does not match parent's type'}`.

4. **`canonicalTypeClass(im, t)`** — collapses equivalent spellings
   (`integer`/`int4`/`int` → `int4`, `double precision`/`float8`/`float` → `float8`,
   …), resolving domains via `InMemory.ResolveColumnType` first and folding in the
   array flag. The typmod args are deliberately **not** compared — a coarser check
   than PG's exact `atttypmod` — so the only thing that can trip the validation is a
   genuine base-type change, eliminating false positives on legitimate inheritance
   scans where parent and child were declared with different-but-equivalent aliases.

`integer` (parent) vs `float8` (child) → mismatch → error. Perms 1–3 never change a
child's type, so both sides resolve to `int4` and no error fires.

## Why this is safe (blast radius)

- The check runs **only** for inheritance-child scans (`skipIfVanished` is the
  marker, set exclusively in the `allDesc` inheritance-expansion loop) that have a
  non-zero `inheritParentOID`. Partition leaves (`LockParentOID`) and direct scans
  are untouched.
- It runs after the lock, so it cannot reorder the `<waiting ...>` rendering.
- Canonical-class comparison ignoring typmod means an existing passing inheritance
  scan (where types are by construction copied from the parent) can never be made to
  error. Confirmed by the full executor unit suite + `inherit-temp`/`alter-table-1/3`
  strict specs passing unchanged.

## Oracle

`postgres/src/backend/optimizer/util/appendinfo.c` — `make_inh_translation_list`
(the type-match `ereport` at lines 161–166). Behaviour compared against
`./postgres/local_install` PG 18.3 via the isolation runner (byte-for-byte
expected-output match).

## Gates

- `TestPort_IsolationAlterTable4` strict PASS (all 4 perms, byte-for-byte).
- No regression: `TestPort_IsolationAlterTable1/AlterTable3/InheritTemp` strict PASS.
- `go test ./internal/executor/ ./internal/planner/ ./internal/catalog/` PASS.
- `go build ./...` clean; new fields/funcs gofmt-clean.
- pgbench smoke = pre-commit hook.
