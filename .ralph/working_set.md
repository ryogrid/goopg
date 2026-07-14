(idle — nothing in flight)

Last completed: M-NIGHTLY DateStyle follow-up — `operators_fk.go`'s
`fkValsForDetail` (the FK-violation `23503` DETAIL line) now honors `SET
datestyle` for DATE/TIMESTAMP/TIMESTAMPTZ values, reusing the CAST
follow-up's helper (renamed `formatTimeDatumForCast`→
`formatTimeDatumDateStyle`). Verified working live for the DELETE/UPDATE-
parent-side and partition-detach FK checks (operate on already-typed
stored-row Datums). Deferral ledger + design doc + fix_plan.md updated with
a NEW discovered gap: the INSERT-side FK check still renders un-coerced
raw literal text, because `operators_storage.go`'s `insertOp.Next` only
coerces int2/int4/int8 before constraint checks run (not date/timestamp/
timestamptz/numeric) — resume point: extend that coercion switch (~line
1900) via the same `evalCast` pattern, then audit `updateOp`/upsert
siblings. Next natural DateStyle slice after that: continue the
`Datum.Format()`/`AppendValueText()` ~20-call-site audit (to_char fallback,
plpgsql RAISE, EXPLAIN, error messages, operators_analyze.go bound-
rendering, array_to_string/||), TIMESTAMPTZ timezone-aware conversion, and
pgoutput.go's DateStyle gap — all still open per the ledger tail.
