---
id: 0103-0042
status: accepted
milestone: M0103-0007 (rung 19)
authors: ralph
date: 2026-05-14
---

# M0103-0007 rung 19 — `DEFAULT nextval('seq')` in the catalog DE slow path

## Context

Rung 18 (`docs/design/0103-0041`) closed the zero-arg time functions in the
catalog DEFAULT-evaluation slow path (`internal/executor/operators_generated.go::evalGenExpr`),
unblocking `created_at timestamptz DEFAULT now()` shapes on the apply-worker
path and the dispatcher INSERT path. Its closure note carved out
`DEFAULT nextval('seq')` as the next rung "when a fixture surfaces a need" —
the natural carve-out because nextval needs the process-global sequence
registry (`seqRegistry` in `internal/executor/operators_sequence.go`) while
the zero-arg path needs only the wall clock.

The fixture that surfaces the need is any pgoutput-replicated table whose
publisher declares a column with `DEFAULT nextval('foo_seq')` and where the
subscriber's table shape carries the same DEFAULT. With rungs 13/14
(`applyDefaultsForMissing` + the rung-14 SERIAL hot path) and rung 18 (time
funcs) in place, this was the last "common" DEFAULT shape that silently
landed as NULL on the subscriber.

## What changes

`evalGenFuncCall` (single edit point in
`internal/executor/operators_generated.go`) gains a one-arg `nextval` branch
alongside the existing zero-arg time-function whitelist. Behaviour:

1. Star args (`*` placeholder) and schemas other than empty / `pg_catalog`
   short-circuit to `NullDatum` so the rest of `evalGenExpr`'s
   silent-passthrough contract holds.
2. When `fn == "nextval"` and `len(x.Args) == 1`, the arg is evaluated
   through `evalGenExpr` itself so cast wrappers (e.g.
   `nextval('public.foo_seq'::regclass)`) and column-ref-wrapped shapes
   degrade cleanly to `NullDatum` when the slow path cannot resolve them.
3. A non-string result falls through to `NullDatum`.
4. A registered sequence advances via `seqState.nextVal()` and the
   result is wrapped as `NewIntDatum(v)`.
5. An UNregistered name is auto-registered with the PG-default shape
   (`start=1, increment=1, min=1, max=int64-max, cycle=false`) before
   advance — mirroring `evalNextval`'s behaviour so apply-worker replays
   of rows produced by a publisher-side SERIAL still land with non-NULL
   ids when the subscriber has not yet seen the matching `CREATE SEQUENCE`
   record. The first advance returns `1` because `RegisterSequence` stores
   `current = start - increment = 0` and `nextVal` returns `current +
   increment`.
6. An overflow / cycle error from `nextVal` falls through to `NullDatum`
   (it's a NOT-NULL-violation-surfaces-loudly path; we do not bubble the
   error up because the apply-worker contract has no error channel for
   "DEFAULT evaluation failed").

The function signature gains `cols []catalog.Column, row Row` so the
nextval-arg can recurse through `evalGenExpr` for the cast / column-ref
shapes. The zero-arg path is byte-equivalent to rung 18.

### What is intentionally NOT done

- **No `ctx.LastSeqVal` / `ctx.CurrSeqVals` update.** `evalGenFuncCall`
  has no `*Context` parameter, by design: it runs from
  `applyDefaultsForMissing` whose two callers (apply worker at
  `applyworker.go:271`, dispatcher `insertOp` at `operators_storage.go:581`)
  have asymmetric Context availability. Threading ctx through would
  touch `evalGeneratedExpr`, `computeGeneratedColumns`, and all three
  call sites. More importantly: a DEFAULT-eval side-channel updating
  session-scoped `currval`/`lastval` would silently break the
  `currval(seq)` SQL invariant (which must error if the session has
  never directly called `nextval`). Upstream PG resolves this via
  `nextval_internal`'s `elevel != ERROR` parameter; goopg's two-layer
  split (process-global registry advance vs session-scoped
  currval/lastval) gives us the same property for free as long as we
  do not call `evalNextval` from the slow path.
- **No CREATE SEQUENCE / pg_sequence wiring.** Out of scope: rung 19 is
  strictly a DEFAULT-evaluator-routing change. CREATE SEQUENCE already
  works through `RegisterSequence` (parser/ddl.go); pg_sequence virtual
  rows remain deferred.
- **No SERIAL hot-path change.** SERIAL columns continue to take the
  `insertOp.Next` SERIAL fast path (rung 14) because their `DefaultExpr`
  is still nil. The slow path here only kicks in for explicit
  `DEFAULT nextval('s')` declarations.

## Why this shape (not the alternatives)

| Alternative | Rejected because |
|---|---|
| Thread `*Context` through `evalGenExpr` / `evalGenFuncCall` | Touches three call sites including the apply worker (which constructs the Context *after* defaults, on purpose); risks subtle ordering bugs around `ctx.LastSeqVal`; and the session-scoped contract for currval/lastval would silently break. |
| Have `evalGenFuncCall` call `evalNextval(args, ctx)` directly with `ctx=nil` | `evalNextval` returns `nil` early on `args[0].IsNull()`, but also writes to `ctx.LastSeqVal` / `ctx.CurrSeqVals` when ctx is non-nil. With ctx=nil it would silently skip session-state updates — equivalent in observable behaviour to the inline path, but adds a load-bearing nil-check at a place where future maintainers might trip. The inline path is simpler and more transparent. |
| Reject unknown sequence names instead of auto-create | Breaks the apply-worker replay path: a subscriber receiving a row whose publisher SERIAL sequence has not yet been mirrored locally would silently land with id=NULL. Mirroring `evalNextval`'s auto-create-with-defaults keeps the contract uniform between SQL-level `nextval('s')` and DEFAULT-driven `nextval('s')`. |

## Test pins

`internal/executor/storage_test.go`:

- `TestInsertFillsMissingColumnDefaultNextval` — pre-registers a sequence,
  asserts two consecutive INSERTs that omit the DEFAULT column land with
  monotonic ids 1, 2. Catches a fixed-sentinel or no-op-fallthrough
  regression.
- `TestInsertFillsMissingColumnDefaultNextvalAutoCreates` — drops the
  sequence first, asserts the first INSERT still lands id=1 via the
  auto-create branch.

Both tests use the dispatcher INSERT path (`planner.Insert` → `insertOp`)
which calls `applyDefaultsForMissing`. The apply-worker path shares the
exact same helper, so a separate apply-worker test would be tautological.

## Verification

```
go test -count=1 -timeout 60s -run "TestInsertFillsMissingColumnDefaultNextval" ./internal/executor/
```

→ PASS (two subtests, ~ms).

Broader regression sweep (executor + planner + analyzer + catalog +
parser + server + wal):

```
go test -count=1 -timeout 300s ./internal/executor/ ./internal/planner/ ./internal/analyzer/ ./internal/catalog/ ./internal/parser/ ./internal/server/ ./internal/wal/
```

→ recorded at commit time in the rung-19 fix_plan entry.

## Follow-ups (deferred within M0103-0007)

- `nextval` arg as a cast over a non-StringConst (e.g. `nextval(some_col::regclass)`):
  the cast-passthrough recursion already supports `CastExpr`-wrapped string
  literals, but evaluating `some_col` against the row's column slice is the
  only path that could surface this; no fixture surfaces it today.
- pgbench against PG publisher with `pgbench_history` polling.
- proto_version=2 streaming subxacts.
- kill -9 + libpq multi-host reconnect plumbing on the client side.
