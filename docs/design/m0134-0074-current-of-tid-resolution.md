# M0134-0074 — `tidscan.sql`: `WHERE CURRENT OF` silently dropped → full-table UPDATE/DELETE

Status: accepted · Date: 2026-08-22 · Milestone: M0134-0074

## The case

`tidscan.sql` is a `failed` regress row. Sized at HEAD it is **301 diff lines /
8 hunks / 0 `^+ERROR` / 1 `^-ERROR`** (the expected `cursor "c" is not positioned
on a row`), no crash, deterministic across fresh-cluster runs.

The diff splits into six root-cause buckets (sizing report
`tmp/ralph-handoffs/m0134-0074-tidscan-sizing/report.md`):

| bucket | root cause | tier | ~lines |
|---|---|---|---|
| **E** | `WHERE CURRENT OF` dropped → full-table UPDATE/DELETE + missing 24000 | **CONTAINED — this slice** | ~53 |
| A | no `Tid Scan` plan node; ctid equality stays Seq Scan + Filter | REFACTOR (same class as 0073 Bucket A) | ~30 |
| D | join on ctid returns 0 rows (needs first-class tid Datum) | CONTAINED-adjacent, follow-up | ~28 |
| B | tid[] array const rendered wrong in EXPLAIN Filter | CONTAINED (deparser), follow-up | ~8 |
| F | SERIALIZABLE tuple SIReadLock absent (`pg_locks` 0 rows) | REFACTOR (SSI pred-lock infra) | 7 |
| C | `::tid` cast / literal stays StringConst (overlaps A) | CONTAINED (small), follow-up | ~6 |

## Root cause of Bucket E

`UPDATE/DELETE … WHERE CURRENT OF c` is destructive today: it rewrites **all**
rows (EXPLAIN ANALYZE `actual rows=3.00`) and the past-end case runs another full
update instead of erroring.

The parser captures the cursor name — `internal/parser/dml.go:423` (UPDATE) and
`:583` (DELETE) write `stmt.CurrentOf`, and leave `stmt.Where == nil` (PG's
`where_or_current_clause` forbids combining `CURRENT OF` with a boolean WHERE).
But the **planner transform never copies `CurrentOf`** (zero references in
`internal/optimizer`), so the UPDATE/DELETE plan carries a bare `SeqScan` with
`pred=nil` and sweeps the table. No "not positioned" check exists anywhere.

PG oracle: `WHERE CURRENT OF` becomes `CurrentOfExpr`, resolved by the tidscan
executor from the portal's positioned tuple
(`postgres/src/backend/executor/nodeTidscan.c:53,66,373,407`); the resolver
`execCurrentOf` (`postgres/src/backend/executor/execCurrent.c:44`) raises
- **34000** `cursor "%s" does not exist` on a portal miss (`:65-70`,
  `ERRCODE_UNDEFINED_CURSOR`), and
- **24000** `cursor "%s" is not positioned on a row` when
  `portal->atStart || portal->atEnd` (`:134-139`, `:179-184`,
  `ERRCODE_INVALID_CURSOR_STATE`).

## Fix decision (chosen: resolve CURRENT OF in the postmaster, reuse the ctid-string-equality path)

goopg has no TID scan node (bucket A) and the executor has **zero** access to the
per-connection cursor registry (`connTxState.Cursors` — confirmed: the only
`Cursors` reference in `internal/executor` is a comment in `join_merge_stream.go`).
Threading the registry into the executor `Context` would couple the executor to
postmaster connection state — a larger, riskier change than the behavior warrants.

Instead, resolve `CURRENT OF` **in the postmaster at dispatch time** (where
`connTx` is available) and substitute a concrete `ctid = '(block,off)'` equality
for the nil WHERE clause. That equality flows through the **existing**
`WHERE ctid = '(0,1)'` parse→plan→execute path (bucket C confirms ctid equality is
already string-equality on the `(block,off)` form and returns the right row), so
**no optimizer or executor source change is needed**.

The tid must be re-resolved on **every** execution (the cursor position changes
between statements, and two identical `WHERE CURRENT OF` statements in the test
must update different rows), so `CURRENT OF` statements must also bypass the
cross-session plan cache.

## The slice

### 1. `cursorEntry` gains `AtEnd` + per-row TIDs (`internal/postmaster/conn_tx.go:43-49`)

- `AtEnd bool` — "positioned past the last row". `Pos == len(Rows)` is ambiguous
  today: it is both "at the last row" and "past end" (the past-end forward fetch
  leaves `Pos` unchanged, `dispatch.go:3798`). `AtEnd` disambiguates.
- `TIDs []storage.ItemPointer` (or a minimal local `{Block uint32; Off uint16}`
  if the `storage` import is awkward) — the per-row self-tid captured at
  materialization, in lockstep with `Rows`. `TIDs[i]` is the tid of `Rows[i]`.

### 2. `materializeCursor` captures per-row tid (`dispatch.go:3874-3933`)

`materializeCursor` drains `op.Next()` → `TupleSlot` and clones `slot.Row()`.
Capture the slot's carried ctid side-channel in the same loop and append it to
`cur.TIDs`. This needs an exported accessor on the `TupleSlot` interface:

```go
// TID returns the slot's carried self-tid (block, offset) and whether it is
// valid. A heap-scan slot stamps it (seqScanOp, operators_storage.go:2076-2078,
// propagated through projectOp operators.go:369-377); synthesized slots
// (values/aggregates) return ok=false.
TID() (block uint32, off uint16, ok bool)
```

Implement it on every `TupleSlot` implementer (`MaterializedSlot`, `VirtualSlot`,
and the opnode `slot` embedded type); non-scan slots return `ok=false`. If a
cursor row has `ok=false` (e.g. `SELECT *` over a slot whose ctid did not
survive), store a zero tid — `WHERE CURRENT OF` on it is a pre-existing gap
(ledger), not this slice's error path.

### 3. FETCH arms set/clear `AtEnd` (`dispatch.go:3781-3838`)

- forward finite: `cur.AtEnd = (len(rowsToSend) == 0)` (past-end fetch returned
  nothing).
- forward `ALL`: `cur.AtEnd = true` (FETCH ALL always ends at EOF).
- backward `ALL` and backward finite: `cur.AtEnd = false` (both move toward BOF).

This is the minimum needed for the 24000 disambiguation. The existing
finite-BACKWARD off-by-one from EOF (h6) is a separate follow-up (ledger) — do
not fix it here.

### 4. Resolve CURRENT OF in `executeOneSimpleStmt` (`dispatch.go:2926`)

(a) **Plan-cache exclusion** (caller `dispatch.go:1106`): add
`&& !isCurrentOfDML(stmt)` to the cache-eligibility condition (helper returns
true for `*parser.UpdateStmt`/`*parser.DeleteStmt` with `CurrentOf != ""`), so
the resolved tid is never baked into a cached plan.

(b) **Resolution**, in `executeOneSimpleStmt` before `optimizer.Plan` (line 2955):

```go
if upd, ok := stmt.(*parser.UpdateStmt); ok && upd.CurrentOf != "" {
    where, err := s.resolveCurrentOf(connTx, upd.CurrentOf)
    if err != nil { return s.writeQueryError(w, err.Code, err.Msg) }
    upd.Where = where
} else if del, ok := stmt.(*parser.DeleteStmt); ok && del.CurrentOf != "" {
    where, err := s.resolveCurrentOf(connTx, del.CurrentOf)
    if err != nil { return s.writeQueryError(w, err.Code, err.Msg) }
    del.Where = where
}
```

`resolveCurrentOf(connTx, name)`:
- `cur, ok := connTx.Cursors[strings.ToLower(name)]`; `!ok` → `34000`
  `cursor "%s" does not exist`.
- `cur.Pos == 0 || cur.AtEnd` → `24000` `cursor "%s" is not positioned on a row`.
- else `tid := cur.TIDs[cur.Pos-1]` and return
  `&parser.BinaryOp{Op: parser.OpEq, Left: &parser.ColumnRef{Column: "ctid"},
  Right: &parser.StringConst{Value: fmt.Sprintf("(%d,%d)", tid.Block, tid.Offset)}}`.

`parser.ColumnRef{Column:"ctid"}` (unqualified) resolves to `*optimizer.CTIDExpr`
at `planner.go:13862-13863`; `CTIDExpr` evaluates to `NewStringDatum("(block,off)")`
(`expr.go:488`), so string-equality matches exactly. Constructed nodes carry
`pos=0` (the established "suppress LINE 1" convention — the equality cannot
error).

### 5. No optimizer / executor logic change

`planUpdate`/`planDelete` already turn a `WHERE` into the scan Filter; the
executor already evaluates `ctid = 'str'`. The `actual rows=1.00` Update line
greens for free: `instrumentedOp.Next` counts emitted rows and the filtered scan
feeds `updateOp` (sizing §5).

## Acceptance

- `UPDATE … WHERE CURRENT OF c` updates exactly the cursor's current row
  (`tidscan.out:196` and `:212` `Update … (actual rows=1.00)`), and
  `SELECT * FROM tidscan` then returns `1 / -2 / -3` (`:220-222`).
- The past-end `UPDATE … WHERE CURRENT OF c` raises
  `ERROR: cursor "c" is not positioned on a row` (`:234` — the sole `^-ERROR`).
- `cursor "c" does not exist` (34000) for an unknown cursor name (targeted unit
  test; no regress witness in tidscan.sql).
- No regression: `WHERE ctid = '(0,1)'` equality still returns the right rows;
  `internal/executor` + `internal/postmaster` + `internal/parser` PASS;
  `scripts/tpch-spotcheck.sh` row counts unchanged (Q12=2, Q13=35).

## Deferrals (ledger rows)

- **Bucket A** — no `Tid Scan` plan node (`create_tidscan_paths`
  `optimizer/path/tidpath.c:498` + `nodeTidscan.c`); the `Tid Scan … TID Cond:
  CURRENT OF c` EXPLAIN lines (`tidscan.out:197-198`, `:213-214`) stay open.
  Own milestone, shared with M0134-0073 Bucket A.
- **Bucket D** — join on ctid returns 0 rows: needs ctid as a first-class
  hashable/comparable Datum (`hashtid` `utils/adt/tid.c:257`; `hash/tid_ops`
  `pg_opclass.dat:182`).
- **Bucket B / C** — tid[] deparser + `::tid` cast (small, follow-ups).
- **Bucket F** — SERIALIZABLE tuple SIReadLock (`pg_locks` tuple row).
- **h6** — FETCH BACKWARD off-by-one from EOF (the `AtEnd` flag now provides the
  disambiguator; the finite-BACKWARD arm still needs its own fix).
- **Current-of via materialized tid ≠ PG's re-resolve-at-execution** — PG
  re-resolves the latest tuple at execution (`table_tuple_get_latest_tid`
  `nodeTidscan.c:373-377`, HOT-update safety); goopg resolves the tid captured at
  cursor materialization. Divergent only under concurrent HOT updates of the
  cursor's row between FETCH and UPDATE — out of scope for this test.
