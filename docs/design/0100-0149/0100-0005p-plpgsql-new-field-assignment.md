# 0100-0005p — PL/pgSQL `NEW.<col> := <expr>` writes back to the trigger row

Status: accepted (2026-05-15, M0100-0005p / loop 32)
Milestone: M0100-0005 (RC isolation suite 21-spec pass)

## Problem

`partition-key-update-1.spec` defines a BEFORE UPDATE trigger that
rewrites the partition-key column on the source partition:

```sql
CREATE FUNCTION func_footrg_mod_a() RETURNS TRIGGER AS $$
  BEGIN
    NEW.a = 2; -- changes partition key, triggers cross-partition move
    RETURN NEW;
  END $$ LANGUAGE PLPGSQL;
CREATE TRIGGER footrg_mod_a BEFORE UPDATE ON footrg1
  FOR EACH ROW EXECUTE PROCEDURE func_footrg_mod_a();
```

Pre-fix, `internal/plpgsql/parser.go::parseAssign` routed every
dotted-target statement (`<ident>.<field> [:=|=] <expr>`) through
`parseDottedExprStmt`, which silently swallowed the whole line and
returned a `_plpgsql_noop` `AssignStmt`. The comment claimed this was
safe because `OLD` is immutable — but it conflated `OLD.*` (truly
immutable) with `NEW.*` (mutable in BEFORE triggers). Result: the
trigger body parsed, the trigger fired, the comment said it succeeded,
and `pu.newRow` was unchanged. Downstream `routeToPartition` resolved
the destination partition from the *unmodified* row, returned the
source partition, set `isCrossPartitionMove = false`, and the executor
stamped a plain xmax instead of `PageSetHeapTupleMovedPartition`. The
M0100-0005n EPQ check on the old slot then never tripped.

Cluster-trace confirmation (captured during loop 32 with temporary
DEBUG instrumentation, removed before commit):

```
DEBUG: routing pre-check — tbl=footrg newRow=[{Kind=2 Int=1 …}, {Kind=3 …'EFG'}]
DEBUG: routing — destPart=footrg1  destRel={1 16448 0}  puRel={1 16448 0}  same=true
DEBUG: isCrossPartitionMove=false
```

`Int=1` proves the trigger did not rewrite `a`. Adjacent `foo`
permutations (non-trigger SET a=2) showed the correct `Int=2` and
`isCrossPartitionMove=true`.

## Fix

Two coordinated changes — both required.

### 1. `internal/plpgsql/parser.go::parseDottedExprStmt`

Replace the unconditional no-op fall-through with a real-assign path
gated on the dotted target's prefix:

- `new.<field> := <expr>` and `new.<field> = <expr>` → emit an
  `AssignStmt{Target: "_new_<field>", Value: <expr>}`. The
  `_new_<field>` slot is the same one `injectTriggerVars` already
  populates, so existing read paths keep working.
- `old.<field> [:= | =] <expr>` and any other prefix → keep the
  legacy no-op semantics. `OLD` is genuinely immutable, and record
  fields aren't first-class in v0.

Both `:=` and `=` are tokenised as `TokenOperator` by the SQL lexer
(`internal/parser/lexer.go:443` puts `=` on the operator track); the
prior `TokenSymbol == "="` arm in `parseAssign` was a vestige and
never matched. The new check uses the operator tokenisation directly:

```go
isAssign := p.cur().Kind == parser.TokenOperator &&
    (p.cur().Value == ":=" || p.cur().Value == "=")
```

The pos passed to the synthesised `AssignStmt` is the original
identifier token's position so error diagnostics still point at
`NEW`, not the field.

### 2. `internal/executor/plpgsql_runtime.go::executePLpgSQLTriggerBody`

Before this fix the function returned `trig.NewRow` verbatim — the
original input slice, by-passing any frame mutations. Add
`rebuildNewRowFromFrame(frame, trig)` and call it on both the explicit
`flowReturnTriggerNew` arm and the default arm for non-DELETE timings:

```go
func rebuildNewRowFromFrame(frame *plpgsqlFrame, trig *plpgsqlTrigCtx) Row {
    out := make(Row, len(trig.NewRow))
    copy(out, trig.NewRow)
    for i, col := range trig.Cols {
        idx, ok := frame.lookup("_new_" + strings.ToLower(col.Name))
        if !ok { continue }
        out[i] = frame.values[idx]
    }
    return out
}
```

`copy(out, trig.NewRow)` preserves any columns the trigger did *not*
touch (no `_new_<col>` slot collision); the loop overlays the columns
the trigger *did* touch. `OLD` and the default-`OLD` BEFORE-DELETE
path are unaffected — they return `trig.OldRow` verbatim.

## Why these two changes have to land together

If only the parser change lands, the AST gains a real assignment to
`_new_<col>` but the runtime still returns the original input row —
the partition-routing path keeps seeing `a=1` and the cross-partition
move never happens.

If only the runtime change lands, no parser ever produces a
`_new_<col>` write — `rebuildNewRowFromFrame` runs but the frame's
`_new_<col>` slots still hold the initial input values, so the
rebuilt row is byte-identical to `trig.NewRow`.

## Regression coverage

- `internal/plpgsql/parser_test.go`
  - `TestParseTriggerNewFieldAssignColonEquals` — `NEW.a := 2`
    produces `AssignStmt{Target: "_new_a"}`
  - `TestParseTriggerNewFieldAssignBareEquals` — `NEW.a = 2` (the
    exact shape in the spec script) does the same
  - `TestParseTriggerOldFieldAssignStaysNoop` — `OLD.a = 99` is
    still dropped to `_plpgsql_noop`
- `internal/server/notice_test.go`
  - `TestTriggerDrivenPartitionKeyRewriteMovesRowAcrossPartitions`
    runs the full end-to-end shape against a real server: BEFORE
    UPDATE trigger sets `NEW.a := 2`; verifies the row moves to the
    `a=2` partition and the source partition's row count drops to
    zero. Pre-fix this test fails with `mv2=0`, `mv1=1`.

## Spec progression

`partition-key-update-1.spec` diff before this loop:
- divergence at L55 (first trigger-driven blocked permutation —
  `s1b s2b s1u2 s2u2 s1c s2c`)

After this loop:
- divergence at L72 (first `foo_range_parted` permutation —
  `s1b s2b s1u3pc s2i s1c s2c`)

Lines 55–71 (covering all 3 trigger-driven blocked permutations on
`footrg`) now match upstream byte-for-byte. The remaining L72+ diff
is a separate bug — `s2i: INSERT INTO bar VALUES(7);` runs FK lookup
against `foo_range_parted1` and does *not* wait for s1's in-flight
cross-partition `UPDATE foo_range_parted SET a=11 WHERE a=7`. That is
the FK-check side of the moved-partition wiring, tracked as a
follow-up (the FK check path doesn't consult
`epqSlotMovedToAnotherPartition`); the M0100-0005n EPQ checks live
only on `updateOp` / `deleteOp`'s retry sites.
