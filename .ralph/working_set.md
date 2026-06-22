Loop #50: M0118-0008 enabler (design 0118-0052) — PL/pgSQL record field
assignment + `record::text`. NOT a promotion.

Done: a `record` var bound by `SELECT * INTO r` is now a first-class composite.
`bindSelectIntoRow`'s single-target/multi-column branch additionally binds the
record var to one composite Datum (`(c0,c1,…)` framing shared with
`updateCompositeField`/`extractCompositeField`) AND registers
`compositeVarFields[r]` from the query schema. The existing machinery then
handles `r.a`/`r.b` reads, `r.b := (select …)` field-assign (RHS via
`evalScalarSubquery`, 0118-0051), and `r::text` (identity cast on KindString).
No-row guarded (`len(row)>0`) so `r` stays NULL like PG (FOUND=false). Lifts
plpgsql-toast assign3: `length(r) = <NULL>` → `6004`.

Files: internal/executor/plpgsql_runtime.go (bindSelectIntoRow record branch,
~line 149-176), internal/executor/plpgsql_record_field_test.go (new,
TestPlpgSQLRecordFieldAndText), docs/design/0118-0052-* + README index,
deferral_ledger.md.

Gates run: TestPlpgSQLRecordFieldAndText PASS (rec_text=9 / rec_fields=`1:hello`
/ rec_set_field=9 / rec_set_field_big=6004); TestPlpgSQLSelectInto +
TestPlpgSQLScalarSubquery + TestPlpgsqlComposite* PASS; go build
./internal/executor clean. pgbench smoke = pre-commit hook (executor dispatch
change only, no hot-path/codec/storage change). NOT yet committed.

Next step (M0118-0008 hard tail — all Effort-L, one slice per loop). Next
plpgsql-toast blocker = assign4: `r test2` (NAMED composite type target, not
bare `record`) + `select * into r` then `length(r::text)` → goopg `<NULL>` vs PG
`6002`. The 0118-0052 record-binding covers bare `record`; assign4 needs the
named-composite-type target to flow through the same composite framing (verify
compositeVarFields is set from the declared type AND select-into populates the
composite Datum — declaration already registers compositeVarFields[r] from
LookupCompositeTypeFields, but bindSelectIntoRow's single-target multi-col branch
should now also populate it; confirm no double-bind conflict). Then:
- assign5/6: `FOR r IN SELECT … LOOP` detoast (`length(r::text)`) + COMMIT-in-loop.
- detoast-across-COMMIT: free external TOAST pointers at the assignment boundary
  so a concurrent VACUUM can't orphan chunks; + runner `<waiting ...>`
  advisory-lock/VACUUM timing marker (the spec's last structural blocker).
- record_out quote/escape framing (current comma-join is unquoted — fine for
  repeat('foo',N) but not general).
- Other tail specs (each a new subsystem): alter-table-4 (INHERITS + txn catalog
  visibility), partition ATTACH/DETACH concurrent visibility,
  partition-drop-index-locking (real pg_locks view), reindex-concurrently-toast.
