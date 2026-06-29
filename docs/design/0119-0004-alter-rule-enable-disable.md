# 0119-0004 — `ALTER TABLE … {ENABLE|DISABLE} [REPLICA|ALWAYS] RULE` round-trip in pg_dump (DU-002 slice 325)

Status: accepted

## Problem

pg_dump's `dumpRule` (pg_dump.c) re-emits each rewrite rule from
`pg_get_ruledef(oid)` and then, for any rule whose `pg_rewrite.ev_enabled` is not
`'O'` (origin/default), appends a separate statement recording the rule's
enable state:

```
ALTER TABLE <t> ENABLE ALWAYS RULE <name>;   -- ev_enabled = 'A'
ALTER TABLE <t> ENABLE REPLICA RULE <name>;  -- ev_enabled = 'R'
ALTER TABLE <t> DISABLE RULE <name>;         -- ev_enabled = 'D'
```

`getRules` selects `ev_enabled` in its simple `SELECT … FROM pg_rewrite ORDER BY
oid` (no subqueries). Slice 324 made an unconditional DO-NOTHING `CREATE RULE`
round-trip, but the `pg_rewrite` projection **hard-coded `ev_enabled='O'`** and
goopg silently consumed `ALTER TABLE … RULE` through the generic ENABLE/DISABLE
trigger/rule no-op arm — so a disabled / replica / always rule restored as
plain-enabled. This slice closes that gap.

goopg implements no query-rewrite system; this is **dump fidelity only** — the
recorded `ev_enabled` has no runtime effect.

## Change

- **Parser** (`internal/parser/ast.go`, `ddl.go`): new `AlterTableActionKind`
  `AlterTableEnableDisableRule` and two `AlterTableAction` fields `RuleName
  string` / `RuleEnabledState byte`. A new branch in `parseAlterTableTail`,
  placed **before** the generic ENABLE/DISABLE no-op arm, recognises
  `{ENABLE|DISABLE} [REPLICA|ALWAYS] RULE <name>` by case-insensitive token-value
  lookahead (`RULE`/`REPLICA`/`ALWAYS` are unreserved idents, matched the same
  way `CREATE RULE` and the RLS arm do) and maps the form to the `ev_enabled`
  char: `ENABLE`→`'O'`, `DISABLE`→`'D'`, `ENABLE REPLICA`→`'R'`, `ENABLE
  ALWAYS`→`'A'`. A non-RULE target (e.g. `DISABLE TRIGGER`) does not match the
  `RULE` lookahead and still falls through to the existing trigger no-op arm.

- **Catalog** (`internal/catalog/catalog.go`): new `RuleInfo.Enabled byte` plus
  `RuleInfo.EvEnabled()` which maps a zero value to `'O'`. The `pg_rewrite`
  `VirtualRows` projection now emits `string(r.EvEnabled())` for the `ev_enabled`
  column instead of the literal `"O"`.

- **Executor** (`internal/executor/operators_ddl.go`): a new
  `parser.AlterTableEnableDisableRule` case in the ALTER TABLE action loop finds
  the named rule in `tbl.Rules` and sets `Enabled = act.RuleEnabledState`. An
  unknown rule name raises `42704` (`rule "%s" for relation "%s" does not
  exist`, matching `ATExecEnableDisableRule`). Because `pg_rewrite` is a fully
  virtual catalog built live from `tbl.Rules` (unlike the heap-backed
  `pg_class` RLS flags of slice 322), mutating the `RuleInfo` in place is
  immediately visible to pg_dump — **no heap re-sync is required.**

## Why dump-fidelity only

goopg never rewrites queries, so a rule (enabled or disabled) does not alter
execution. The `ev_enabled` char exists solely so that a goopg dump faithfully
reproduces a table's rule-enable state and can be restored into either goopg or
real PostgreSQL.

## Blast radius

`RuleInfo.Enabled` defaults to the zero value, which `EvEnabled()` maps to
`'O'`, so every existing rule (and every relation with no rules) projects
`ev_enabled='O'` exactly as before — byte-identical `pg_rewrite` output. The new
parser branch only fires on the `… RULE` lookahead; all other ENABLE/DISABLE
forms (TRIGGER, and the RLS arm above it) are unchanged. TPC-H / pgbench carry no
rules → zero blast radius.

## Oracle

- `src/bin/pg_dump/pg_dump.c` `dumpRule` (the `ev_enabled != 'O'` switch) and
  `getRules` (`ev_enabled` column).
- `src/backend/commands/tablecmds.c` `ATExecEnableDisableRule`
  (`'O'`/`'D'`/`'R'`/`'A'` semantics, 42704 on unknown rule).
- `src/include/catalog/pg_rewrite.h` (`ev_enabled` char domain).

## Tests / gates

- `internal/parser` `TestParseAlterTableEnableDisableRule` — the four forms map
  to the correct `RuleEnabledState`; quoted rule name preserved; `DISABLE
  TRIGGER` still falls to the no-op trigger arm.
- `internal/executor` `TestDDLAlterTableRuleEnabledState` — fresh rule is `'O'`;
  DISABLE / ENABLE REPLICA / ENABLE ALWAYS set `D`/`R`/`A` in the live
  `pg_rewrite` projection; ENABLE restores `'O'`; unknown rule → `ExecError`
  42704.
- `internal/testport` `TestPort_PgDumpConnectionSetup` **DU-002 slice 325** —
  `rule_t` has `r_noupd` DISABLEd and `r_nodel` ENABLE ALWAYS; the dump carries
  `ALTER TABLE public.rule_t DISABLE RULE r_noupd;` and `… ENABLE ALWAYS RULE
  r_nodel;`, and emits **no** `ALTER TABLE … RULE r_noins;` (origin rule). Verified
  byte-identical vs real pg_dump 18.3.
- `parser` / `catalog` / full `executor` suites PASS; `go build ./...` clean;
  pgbench smoke = pre-commit hook.

## Limitations / still open under M0119-0004

- Conditional (`WHERE`) and action-command `CREATE RULE` forms still need the
  query reverse-compiler (slice 324 limitation, unchanged).
- GRANT/ACL (`relacl`) and named-role policies remain blocked on a per-role OID
  registry **and** the `ARRAY(SELECT …)` / `array_to_string` / `quote_ident`
  query stack goopg does not yet implement.
- Extended-protocol commit-time deferral.
