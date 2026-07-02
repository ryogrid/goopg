# 0119-0004aa — CREATE RULE (DO [INSTEAD] NOTHING) round-trip in pg_dump (DU-002 slice 324)

Status: accepted
Milestone: M0119-0004 (pg_dump getter-battery parity; CSV row DU-002)

## Problem

`pg_dump`'s `getRules` reads `pg_rewrite` and `dumpRule` re-emits each
non-view rule from `pg_get_ruledef(oid)` verbatim (`pg_dump.c`). goopg parsed
`CREATE RULE` only as a `CompatNoopStmt` (no rule was recorded anywhere) and the
`pg_rewrite` virtual catalog was a hard-coded empty stub, so a rewrite rule was
**silently lost** on dump/restore.

The query-rewrite system as a whole is out of goopg's scope — reconstructing an
arbitrary rule action (`pg_get_ruledef` of a `DO INSERT … SELECT …` rule) is the
full reverse-compiler. This slice closes the **contained** subset that needs no
action deparse: the unconditional `DO [INSTEAD] NOTHING` rule (the classic
"make a relation reject a DML class" form). Conditional (`WHERE`) and
action-command rules remain `CompatNoopStmt`, exactly as before.

## Oracle (PG 18.3, `./postgres/local_install`)

For a table `public.t`:

```
CREATE RULE r_noins AS ON INSERT TO t DO INSTEAD NOTHING;
CREATE RULE r_also  AS ON UPDATE TO t DO ALSO NOTHING;
```

`pg_dump` (which calls the single-arg `pg_get_ruledef`, i.e. `PRETTYFLAG_INDENT`)
emits:

```
CREATE RULE r_noins AS
    ON INSERT TO public.t DO INSTEAD NOTHING;
CREATE RULE r_also AS
    ON UPDATE TO public.t DO NOTHING;
```

i.e. `CREATE RULE <name> AS\n    ON <EVENT> TO <schema>.<rel> DO [INSTEAD ]NOTHING;`.
`DO ALSO NOTHING` and plain `DO NOTHING` both render without the `INSTEAD`
keyword (`is_instead = false`). `ev_enabled = 'O'` (origin, the default) means
`dumpRule` emits no trailing `ALTER TABLE … ENABLE/DISABLE RULE`.

`getRules` (`pg_dump.c`) selects only
`tableoid, oid, rulename, ev_class, ev_type, is_instead, ev_enabled` from
`pg_rewrite` (never `ev_qual`/`ev_action`), then `dumpRule` prints
`pg_get_ruledef(oid)`. `ev_class` must resolve to a dumped table OID
(`findTableByOid`; a missing parent is `pg_fatal`). View `_RETURN` rules
(`ev_type='1' && is_instead`) are merged into `CREATE VIEW`, not dumped as rules.

Mirrored upstream: `src/backend/utils/adt/ruleutils.c:make_ruledef`,
`src/bin/pg_dump/pg_dump.c:getRules`/`dumpRule`.

## Change (dump-fidelity only — goopg does NOT implement query rewrite)

- **parser** (`ast.go`, `ddl.go`): new `CreateRuleStmt{Name, Event, Table,
  Instead, RuleKind}`. `parseCreateRuleTail` now also captures the `ON <event>`
  keyword and a depth-0 `NOTHING` / action token, and returns a `CreateRuleStmt`
  **only** for the unconditional DO-NOTHING form on an INSERT/UPDATE/DELETE event
  (`isNothing && !hasWhere && !hasAction`). Every other shape (action command,
  `WHERE`, `ON SELECT`) still returns the historical `CompatNoopStmt`, with the
  same `RuleKind` string the COPY-DML path relies on.
- **catalog** (`catalog.go`): new `RuleInfo{Name, OID, Event, Instead}` +
  `Table.Rules`; `RuleInfo.EvType()` maps the event to the `pg_rewrite.ev_type`
  char (`'2'`/`'3'`/`'4'`). The `pg_rewrite` `VirtualRows` projects every table's
  `Rules` (`ev_enabled='O'`, `ev_qual`/`ev_action` left empty → SQL NULL, since
  `getRules` never reads them). A zero-OID rule is invisible to pg_dump.
- **executor** (`operators_ddl.go`): `execCreateRule` records the `RuleInfo`
  (dup name → `42710`), assigns the OID via `AllocOID` (no heap sync — pg_rewrite
  is virtual), and preserves the prior `RegisterCompatObject("rule", …)` +
  `RegisterTableRuleKind` bookkeeping so DROP RULE existence checks and COPY-DML
  rule-kind handling are byte-for-byte unchanged. `execDropRule` additionally
  filters the modelled `RuleInfo` out of `Table.Rules` so a dropped rule stops
  being dumped.
- **executor** (`expr.go`): `pg_get_ruledef`/`pg_get_ruledef_ext` builtin —
  scans tables' `Rules` for the OID and reconstructs the statement via
  `buildRuleDefString` (the `PRETTYFLAG_INDENT` text above). No qual/action
  deparse is ever needed (only DO-NOTHING is modelled).
- **planner** (`planner.go`): `CreateRuleStmt` added to the DDL pass-through.

## Blast radius

Nil outside the modelled form. `Table.Rules` defaults empty → `pg_rewrite`
projects nothing and is byte-identical to the prior empty stub for every
existing relation; `pg_get_ruledef` is a fresh builtin. Action-command,
conditional, and view rules keep the exact `CompatNoopStmt` path (verified by a
parser test). The COPY-DML rule-kind registry is populated identically.

## Limitations / follow-ups

- Conditional (`WHERE`) rules need OLD/NEW-aware qual deparse.
- Action-command rules (`DO [INSTEAD] INSERT/UPDATE/DELETE/SELECT …`) need the
  full query reverse-compiler.
- `ON SELECT` view `_RETURN` rules ride the existing `CREATE VIEW` dump path
  (goopg has no stored user views feeding `pg_rewrite`).
- `ALTER TABLE … ENABLE/DISABLE RULE` (non-`'O'` `ev_enabled`) is not modelled
  (goopg has no rule-firing semantics to enable/disable).

## Gates

- New **DU-002 slice 324** in `TestPort_PgDumpConnectionSetup`: `rule_t` carries
  one rule per (event, instead/also) form; all three asserted byte-identical vs
  real `pg_dump` 18.3 (PASS, 4.7 s).
- `internal/parser` `TestParseCreateRuleNothing` (3 forms → `CreateRuleStmt`) +
  `TestParseCreateRuleNonNothingStaysNoop` (action/WHERE/SELECT → `CompatNoopStmt`).
- `internal/executor` `TestDDLCreateRuleRoundTrip` (record → `pg_rewrite`
  projection → `pg_get_ruledef` byte-match → dup `42710` → `DROP RULE`).
- `internal/parser`/`internal/catalog`/`internal/planner` suites + full
  `internal/executor` suite PASS; `go build ./...` clean; pgbench TPC-B smoke
  via the pre-commit hook.
