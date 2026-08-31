# 0118-0052 — PL/pgSQL record field assignment + `record::text` (M0118-0008 enabler)

**Status:** accepted
**Milestone:** M0118-0008 (isolation spec `plpgsql-toast`)
**Kind:** Enabler — **NOT a spec promotion.** Advances the first divergence
of `plpgsql-toast` from `assign3` toward `assign4`; the spec stays unported
because it still needs the FOR-loop detoast paths (`assign5`/`assign6`) and the
advisory-lock / VACUUM `<waiting …>` concurrency marker (all Effort-L, deferred).

## Problem

`plpgsql-toast` step `assign3` (upstream `expanded_record_set_field`):

```sql
do $$
  declare r record;
  begin
    select * into r from test1;
    r.b := (select test1.b from test1);
    delete from test1;
    commit;
    perform pg_advisory_lock(1);
    raise notice 'length(r) = %', length(r::text);
  end;
$$;
```

PostgreSQL emits `length(r) = 6004` (`test1.b` is `repeat('foo',2000)` = 6000
chars, so `r::text` = `(1,foofoo…foo)` = `"(" + "1" + "," + 6000 + ")"` = 6004).
goopg emitted `length(r) = <NULL>`.

**Root cause.** A `record` variable bound by `SELECT * INTO r` was *only*
materialised as `_r_<col>` sub-fields (the `FOR … IN SELECT` record convention).
The record variable `r` itself was left at its declared NULL, and no
`compositeVarFields[r]` schema was registered. Consequently:

* `r::text` read the NULL record variable → NULL;
* `r.b := …` field assignment (which routes through the composite-field path,
  keyed on `compositeVarFields[varName]`) found no field schema and silently
  skipped;
* `r.a` / `r.b` field reads (same key) fell through to
  `0A000 qualified names are not supported`.

So three composite operations on a SELECT-INTO record were all unimplemented,
collapsing to a NULL `r::text`.

## Change

`bindSelectIntoRow` (`internal/executor/plpgsql_runtime.go`), in the
single-target / multi-column (record) branch, now — in addition to the existing
`_r_<col>` sub-field binding — binds the **record variable itself** to a single
composite Datum and registers its field schema:

```go
if idx, ok := frame.indexByName[name]; ok && len(row) > 0 {
    parts := make([]string, len(schema))
    cf := make([]catalog.CompositeField, len(schema))
    for i, sc := range schema {
        if i < len(row) && !row[i].IsNull() {
            parts[i] = row[i].Format()
        }
        cf[i] = catalog.CompositeField{Name: sc.Name, ColType: sc.Type.Name}
    }
    frame.values[idx] = NewStringDatum("(" + strings.Join(parts, ",") + ")")
    frame.compositeVarFields[name] = cf
}
```

The composite Datum reuses the existing PL/pgSQL composite text framing
(`(c0,c1,…)`, comma-joined unquoted `Format()` values) already produced and
consumed by `updateCompositeField` / `extractCompositeField`. With both the
single composite Datum and the `compositeVarFields` schema in place, the
*existing* machinery handles the rest with no further change:

* **field read** `r.a` / `r.b` → `lowerPLpgSQLExpr`'s `*parser.ColumnRef`
  composite branch (`compositeVarFields[varName]` + `extractCompositeField`);
* **field assignment** `r.b := (select …)` → the `AssignStmt` composite-field
  path (`Target == "r\x00b"`, `updateCompositeField`), whose RHS scalar subquery
  is evaluated by `evalScalarSubquery` from 0118-0051;
* **`r::text`** → the record variable now holds the composite string, and the
  cast is the identity on a `KindString` Datum.

The no-row case (`len(row) == 0`) is guarded out, leaving `r` NULL as PostgreSQL
does (`FOUND` = false).

### Scope / limits (deferred, Effort-L)

* The composite text framing is the simplistic comma-join already used across
  the PL/pgSQL composite path: it does not quote/escape field values containing
  `,`, `"`, or parentheses. Correct for `plpgsql-toast` (`repeat('foo',N)` has
  none) and the existing `avg_state` callers; a faithful `record_out`
  quote/escape pass is a separate enabler.
* `assign4` (`r test2` — a *named* composite type target, vs. bare `record`)
  exercises the same binding but `r::text` framing must also drop through the
  named-type path; not verified here.
* `assign5`/`assign6` (`FOR r IN SELECT … LOOP` + `r::text` detoast across
  `COMMIT`) and the `<waiting …>` advisory-lock/VACUUM timing marker remain the
  spec's blockers.

## Oracle

PostgreSQL `src/pl/plpgsql/src/pl_exec.c` (`exec_assign_value` →
`expanded_record_set_field`, `exec_eval_datum` for record-to-Datum). goopg
mirrors the *observable* semantics (record fields settable, `record::text`
renders the row), not the expanded-record TOAST-flattening internals — which are
the subject of the still-deferred detoast enabler.

## Tests

`internal/executor/plpgsql_record_field_test.go` — `TestPlpgSQLRecordFieldAndText`:

* `rec_text` — `SELECT * INTO r` then `length(r::text)` = 9 (`(1,hello)`);
* `rec_fields` — `r.a::text || ':' || r.b` = `1:hello` (field reads);
* `rec_set_field` — `r.b := (select …)` then `length(r::text)` = 9;
* `rec_set_field_big` — exact assign3 shape, `repeat('foo',2000)` →
  `length(r::text)` = 6004.

Gates: `TestPlpgSQLRecordFieldAndText`, `TestPlpgSQLSelectInto`,
`TestPlpgSQLScalarSubquery`, `TestPlpgsqlComposite*` PASS; full
`internal/executor` plpgsql/composite/trigger subset PASS; `go build
./internal/executor` clean; pgbench smoke = pre-commit hook (executor dispatch
change only, no hot-path/codec/storage change).
