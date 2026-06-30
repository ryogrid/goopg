(idle — nothing in flight)

Loop #8/#100 COMPLETE: M0119-0004 DU-002 slice 369 — a trigger whose
`EXECUTE FUNCTION fn(0042, 3.14, foo)` carries NON-string args (integer/float/
bare identifier) now round-trips through pg_dump. PRODUCTION fix (resolves the
slice-368 deferral).

How: PG gram.y TriggerFuncArg (6198) stores EVERY arg form as a string in
pg_trigger.tgargs — Iconst via psprintf("%d") ("0042"→"42"), FCONST by lexeme,
ColLabel by text — and pg_get_triggerdef re-quotes them all as `'…'` →
`trig_fn('42', '3.14', 'foo')`. goopg's parseCreateTriggerTail EXECUTE-FUNCTION
arm previously captured only TokenStringLit and p.advance()-skipped the rest
(dropping the args). Now also captures TokenIntLit (canonicalised via new
canonicalTriggerIntArg helper), TokenNumericLit, TokenIdent into FuncArgs.
buildTriggerDefString (executor expr.go) already quotes every stored arg → NO
deparse change.

Files:
- internal/parser/ddl.go (EXECUTE-FUNCTION arg switch + canonicalTriggerIntArg)
- internal/parser/create_trigger_test.go (int/float/ident/string case)
- internal/testport/pgdump_connsetup_test.go (slice-369 trg_narg fixture+assert)
- docs/design/0110-0001-pg-dump-tap-port.md (Slice 369; 368 deferral→resolved)
- .ralph/deferral_ledger.md (368→resolved, 369 row)

Gates run: parser suite PASS; TestPort_PgDumpConnectionSetup PASS (5.5s,
byte-identical vs real pg_dump 18.3, oracle-verified directly); build clean;
pgbench smoke=pre-commit hook. gofmt -l flags create_trigger_test.go line 70 —
PRE-EXISTING version-mismatch artifact (`''`→`"`), not my edit.

Deferred (ledger): int canonicalisation covers Go-int range only (PG rejects
larger literals first → fallback unreachable); restart persistence = usual
in-memory-catalog gap.

Next loop: pick a fresh M0119-0004 pg_dump slice. Remaining deferred candidates:
typed STRING-literal cast (`'_x'::text`, needs expression TYPE INFERENCE, broad
blast radius) and action-command CREATE RULE (milestone-sized reverse-compiler).
