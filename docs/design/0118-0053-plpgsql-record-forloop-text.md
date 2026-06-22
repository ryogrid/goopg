# 0118-0053 — PL/pgSQL record FOR-loop `record::text` framing (M0118-0008 enabler)

**Status:** accepted
**Milestone:** M0118-0008 (isolation spec `plpgsql-toast`)
**Kind:** Enabler — **NOT a spec promotion.** Closes the `r::text` output gap for
`plpgsql-toast` steps `assign4`, `assign5`, and `assign6`. The spec stays
unported because it still needs detoast-across-`COMMIT` safety in the FOR-loop
body and the advisory-lock / VACUUM `<waiting …>` concurrency marker (the spec's
last structural blocker — Effort-L, deferred).

## Problem

`plpgsql-toast` exercises four "expanded record" variable-assignment code paths,
each ending in `raise notice 'length(r) = %', length(r::text)`. After 0118-0052
landed `assign3` (`SELECT * INTO r` field assignment), the remaining record steps
were:

| step | binding | r::text expected |
|------|---------|------------------|
| assign4 (`expanded_record_set_fields`) | `r test2; select * into r from test1` | `6004` |
| assign5 (`expanded_record_set_tuple`) | `r record; for r in select test1.b loop … end loop` | `6002` |
| assign6 (FOR-loop detoast) | same loop, per iteration over 3 rows | `6002`, `9002`, `12002` |

(`test1.b` = `repeat('foo',2000)` = 6000 chars; the composite framing adds `(`/`)`
plus comma separators. assign4's `(1,…)` adds 4; the single-column assign5/6
`(…)` adds 2.)

* **assign4** was already correct after 0118-0052 — a named composite type
  target (`r test2`) flows through the *same* `bindSelectIntoRow` multi-column
  branch as bare `record`, so the composite framing was already produced. Probe:
  `length(r::text) = 6004`. (The working-set note predicting `<NULL>` / `6002`
  was stale; verified by probe before any change.)
* **assign5/assign6** were wrong: a single-column record FOR-loop variable held
  the **raw column value**, so `length(r::text)` was the value length (6000), not
  the composite length (6002).

**Root cause (assign5/6).** `ForSelectStmt`'s per-row binding had a *scalar
shortcut*: when the loop variable existed in the frame and the query returned
exactly one column, it bound `frame.values[idx] = row[0]` — the raw scalar — with
no `(…)` framing and no `compositeVarFields[r]` schema. That is correct for a
scalar loop variable (`for x in select g.i`), but a `record`-declared variable
must hold a composite value so `r::text` reassembles the row, exactly as PG's
expanded-record representation does. Multi-column record loops took the
`else` branch (sub-field `_r_<col>` binding only) and would have rendered NULL
for `r::text` for the same reason — also fixed here.

## Change

`internal/executor/plpgsql_runtime.go`:

1. **`(*plpgsqlFrame).isRecordVar(name)`** — a record/composite discriminator: a
   variable whose declared type is `record`, or one with a registered
   `compositeVarFields[name]` (a named composite type like `test2`), is a record
   variable. A plain scalar (`text`, `int`, …) is not.

2. **`bindRecordRowComposite(varName, row, schema, frame)`** — the shared
   record-binding helper, extracted from `bindSelectIntoRow`'s multi-column
   branch. It (a) registers the `_<var>_<col>` sub-fields for field access
   (`r.a`), (b) binds the variable itself to one `(c0,c1,…)` composite Datum, and
   (c) records its field schema. No-row ⇒ the record stays NULL (`FOUND`=false).
   `bindSelectIntoRow` now calls this helper (behaviour-preserving; keeps the
   SELECT-INTO and FOR-loop record paths in sync — sibling-paths discipline).

3. **`ForSelectStmt` per-row binding** now branches on `isRecordVar(varName)`
   *first*: a record loop variable is bound via `bindRecordRowComposite` (composite
   framing, regardless of column count); the scalar shortcut and the legacy
   sub-field branch are retained unchanged for non-record loop variables.

The composite text framing reuses the existing comma-join `Format()` framing
shared with `updateCompositeField` / `extractCompositeField`; `r::text` is the
identity cast on the resulting `KindString` Datum.

### Scope / limits (deferred, Effort-L)

* **`COMMIT` inside the FOR-loop body** (assign6 deletes + `commit`s each
  iteration). The 0118-0049 `PLpgSQLCommitChain` callback drives transaction
  control, but holding a cursor across `COMMIT` and freeing external TOAST
  pointers at the assignment boundary (so a concurrent VACUUM can't orphan
  chunks) is a separate enabler. This change fixes only the `r::text` *framing*
  the spec asserts; the detoast-across-commit safety is unverified.
* The `<waiting …>` advisory-lock / VACUUM timing marker remains the spec's last
  structural blocker (the runner decides blocking purely by a 300 ms timeout).
* The comma-join framing is still unquoted (fine for `repeat('foo',N)`; a
  faithful `record_out` quote/escape pass is a separate enabler — see 0118-0052).

## Oracle

PostgreSQL `src/pl/plpgsql/src/pl_exec.c` (`exec_for_query` → `exec_move_row`
into an expanded record; `exec_eval_datum` for record-to-Datum;
`expanded_record_set_tuple`). goopg mirrors the *observable* semantics
(`record::text` renders the loop row), not the expanded-record TOAST-flattening
internals.

## Tests

`internal/executor/plpgsql_record_forloop_test.go` —
`TestPlpgSQLRecordForLoopAndText`:

* `a4` — `r test2; select * into r` then `length(r::text)` = 6004 (named composite);
* `a5` — `for r in select test1.b loop null; end loop` then `length(r::text)` =
  6002 (single-column record FOR-loop framing);
* `a6` — three rows (6000/9000/12000) → per-iteration `length(r::text)` =
  `6002 9002 12002`.

Gates: `TestPlpgSQLRecordForLoopAndText`,
`TestPlpgSQLRecordFieldAndText`, `TestPlpgSQLSelectInto`,
`TestPlpgSQLScalarSubquery`, `TestPlpgsqlComposite*` PASS; full
`internal/executor` package PASS; `go build ./internal/executor` clean; pgbench
smoke = pre-commit hook (executor dispatch change only, no
hot-path/codec/storage change).
