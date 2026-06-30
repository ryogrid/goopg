(idle — nothing in flight)

Loop #99 COMPLETE: M0119-0004 DU-002 slice 368 — a trigger whose
`EXECUTE FUNCTION fn('a', 'b')` carries STRING arguments (TG_ARGV) now has an
oracle-verified round-trip fixture vs real pg_dump 18.3.

How: the whole path already existed end-to-end — parser collects string-literal
args into CreateTriggerStmt.FuncArgs (parseCreateTriggerTail EXECUTE FUNCTION
arm), execCreateTrigger threads them to catalog.Trigger.Args, and
buildTriggerDefString (executor expr.go:4790) re-emits them comma-separated with
`''`-doubled quoting, byte-matching pg_get_triggerdef_worker (ruleutils.c:462-486
simple_quote_literal). The lexer collapses `''`->`'` on input so re-escaping is
symmetric. **NO production change** — fixture-only slice.

Files:
- internal/testport/pgdump_connsetup_test.go (slice-368 trg_arg fixture + assert;
  two args, second with embedded single quote)
- internal/parser/create_trigger_test.go (new TestParseCreateTriggerFuncArgs)
- docs/design/0110-0001-pg-dump-tap-port.md (Slice 368)
- .ralph/deferral_ledger.md (slice-368 row), .ralph/fix_plan.md (loop #99 note)

Gates run: parser+executor unit suites PASS; TestPort_PgDumpConnectionSetup PASS
(5.1s, byte-identical vs real PG 18.3); go build clean; pgbench smoke=pre-commit hook.

Deferred (ledger): the parser SILENTLY SKIPS non-string trigger args
(parseCreateTriggerTail `else { p.advance() }`) — PG accepts `fn(42)`, stores
"42" in tgargs, dumps the quoted `'42'`; goopg drops it. Restart persistence is
the usual in-memory-catalog gap.

Next loop: pick a fresh M0119-0004 pg_dump slice. Candidate quick wins: capture
the non-string trigger-arg form (defer above); other deferred items are the
typed STRING-literal cast (`name || '_x'` -> `'_x'::text`, needs expression
TYPE INFERENCE — PG labels ALL text-typed Consts, broad blast radius) and
action-command CREATE RULE (milestone-sized reverse-compiler).
