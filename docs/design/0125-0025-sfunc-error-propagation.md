# M0125-0025 — a raising aggregate support function must abort the statement

Status: **implemented** (2026-07-30)
Milestone: M0125 (TPC-DS timeout class & walker extinction) — ledger debt from M0125-0024
Code: `internal/executor/operators_join_agg.go`, `internal/executor/operators_window.go`
Gate: `internal/executor/agg_sfunc_error_propagation_test.go`

## 1. What was wrong

A user-defined aggregate has up to three support routines — `SFUNC`, `COMBINEFUNC`,
`FINALFUNC`. If any of them raised an error, goopg discarded the error and
answered the query anyway.

Measured at HEAD `0de1b404`, over `raise_t(a bigint)` holding `1, 2, 3` and an
`SFUNC` that raises on any `v > 1`:

| statement | goopg before | PG 18.3 (oracle, port 65438) |
|---|---|---|
| `SELECT p_rsum(a) FROM raise_t` | `1`, no error | `ERROR: boom 2` |
| `SELECT p_rsum(DISTINCT a) FROM raise_t` | `1`, no error | `ERROR: boom 2` |
| `SELECT p_rsum(DISTINCT a), p_rsum(DISTINCT a) FROM raise_t` | `(1, 1)`, no error | `ERROR: boom 2` |
| `SELECT p_fsum(a) FROM raise_t` (raising `FINALFUNC`) | `NULL`, no error | `ERROR: final boom 6` |
| `SELECT p_csum(a) FROM raise_t` (raising `COMBINEFUNC`) | `6`, no error | `ERROR: combine boom` |

`1` is the sum of the rows that happened to be transitioned *before* the raise.
That is the defining property of this defect class: the wrong answer is a
plausible value of the right type in a result set of the right shape, so no
row-count gate in this programme could see it. It is the same class as
M0125-0024 (a follower inheriting the leader's aggregate state) and it was found
in the same place, by reading `executeSFuncCall` closely enough to write that
task's value gate — recorded as its third ledger row (2026-07-30), which
deliberately claimed *a code path* rather than an observed wrong answer. It is
an observed wrong answer.

The PG oracle readings above were taken inside a transaction that was rolled
back, so the TPC-DS reference database was left byte-unchanged (verified: zero
rows in `pg_proc` for the five fixture routines afterwards).

## 2. Two independent swallows

**(a) `executeSFuncCall` lost the error.** The function models many built-in
transition functions inline (`int8inc`, `int4pl`, …) and only then looks the name
up in the routine registry. Its user-defined tail discarded every candidate's
error — literally `_ = rerr`, twice — and fell through to a synthesised

```
42883 aggregate state function "p_raise" does not exist
```

so a routine that was **present and failed** was reported as **missing**. That
misreporting is the reason the second swallow was invisible: a caller
inspecting the error would have seen a lookup failure, not a user error.

**(b) Every caller swallowed it again.** All seven call sites were written
`if serr == nil { state = newState }`, i.e. *on failure, keep the previous
state*. The three transition-function loops are separate code in three
different methods, and each swallowed independently:

| site | method | consequence before |
|---|---|---|
| `applyAgg` (star form) | per-row accumulation | previous state kept |
| `applyAgg` (normal form) | per-row accumulation | previous state kept |
| `finishAgg` DISTINCT/ORDER BY loop | dedup-then-transition | partial sum returned |
| `aggregateOp.Open` leader pre-compute | shared-slot leader/follower copy | partial sum copied to **every** follower |
| `finishAgg` `COMBINEFUNC` | merge | un-combined partial state returned |
| `finishAgg` `FINALFUNC` (×2) | finalize | `NULL` returned |

PG has no equivalent choice to make. `advance_transition_function`
(`postgres/src/backend/executor/nodeAgg.c`) invokes the transition function
through `FunctionCallInvoke`, so an `ereport(ERROR)` inside it unwinds the
statement carrying the routine's own SQLSTATE. There is no "keep the old state"
branch to write.

## 3. Why a blanket propagate would have been wrong

`executeSFuncCall` is not only the user-defined-routine path — it is also the
lookup for the built-in transition functions it models inline, and it is called
speculatively for slots an aggregate may not have declared. Its `42883` is
therefore a **normal outcome**, not a failure: an aggregate declared with no
`FINALFUNC` finishes on its state alone, and turning that `42883` into a client
error would have broken every such aggregate.

So the fix needs the same shape M0125-0024's `exprIdentityKey` needed — the two
failure modes must be *decidable*, because they are propagated in **opposite**
directions:

```go
type errSFuncNotFound struct{ inner *ExecError }   // "nothing to call"  → swallow
func sfuncRaised(err error) bool                   // "it ran and raised" → propagate
```

`errSFuncNotFound` unwraps to its `*ExecError`, so `errors.As` still reaches the
`42883`; it is never propagated to a client by design, which is stated in its
doc comment because a bare `err.(*ExecError)` type assertion elsewhere would not
see through the wrapper.

One further subtlety in `executeSFuncCall`: candidates are matched by **arity,
not signature**, so several may be tried where PG resolves exactly one. The fix
therefore keeps trying after a failure and remembers the *first* error, changing
only the all-candidates-failed outcome. Every call that succeeds today still
succeeds by the same route; the sole behavioural change is that the synthesised
`42883` is replaced by the routine's real error.

## 4. `finishAgg` needed an error channel, and 100 returns did not

`finishAgg` is a 460-line function with ~103 `return` statements, all of them
`return <Datum>`. Only its leading `if call.UserAgg != nil` branch can fail.
Changing the signature in place would have produced a ~103-hunk diff in which
the five lines that matter were invisible.

Instead the built-in tail — everything from `switch strings.ToLower(call.Name)`
onward, which already sits at one level of indentation inside the function body —
was lifted verbatim into `finishBuiltinAgg(st, call) Datum`, with **no
re-indentation and no touched returns**. `finishAgg` keeps its name and its
callers, gains `(Datum, error)`, and ends with
`return o.finishBuiltinAgg(st, call), nil`. The split also documents the fact
worth knowing: built-in finalization cannot fail.

Four callers updated: `aggregateOp.Open`, `windowOp.evalFrameAggFuncs`,
`windowOp.evalExplicitFrameAggFuncs`, and one test helper (which already had an
`error` in its own signature and was returning a hardcoded `nil`).

## 5. Gate

`internal/executor/agg_sfunc_error_propagation_test.go`. Every `want` is the
message PG 18.3 produced for the identical statement, measured, not derived; and
each subtest's failure text quotes the pre-fix wrong answer so a regression
explains itself.

- `TestRaisingSFuncAbortsStatement` — five subtests, one per swallowing site
  reachable from SQL (`SFUNC`, `SFUNC` under DISTINCT, `SFUNC` under a shared
  DISTINCT state slot, `FINALFUNC`, `COMBINEFUNC`). Each asserts **both** halves:
  that an error is returned at all, and that it is the routine's own error rather
  than the misleading `42883`.
- `TestMissingSFuncStillFallsThrough` — pins the behaviour that must *not*
  change, and is the reason §3's distinction exists rather than a blanket
  propagate.
- `TestRaisingSFuncInWindowFrameDoesNotAnswerSilently` — see §6.

goopg's `RAISE EXCEPTION` yields SQLSTATE `P0001`, matching PG's, so the fixed
paths reproduce the oracle's error identically and not merely in spirit:
`P0001: boom 2`, `P0001: final boom 6`.

## 6. What this loop found and did NOT fix

The two `windowOp` sites are now plumbed but **unreachable**, and the gate is how
we know: goopg's v0 analyzer rejects a user-defined aggregate in `OVER (...)`
with `0A000 window function "p_fsum" is not supported in v0 analyzer`, before the
executor is involved. PG accepts it. So no user-defined support routine can run
in a window frame at all, and the propagation added at those two sites cannot be
exercised today. That is a separate gap with its own ledger row (2026-07-30);
the test asserts the durable invariant instead of a message — a raising
aggregate in a frame must not yield a *silent value* — which stays meaningful
once window support for user-defined aggregates lands.

Also unchanged: `applyAgg`'s neighbouring argument-evaluation sites
(`if everr == nil { … }` around `evalExprSlot` for `Arg2`/`ExtraArgs`) swallow in
exactly the same shape — a failed argument expression is dropped and the sfunc is
called with *fewer arguments than declared*. That is the same defect class one
layer out, was not the subject of this task's measurement, and has its own ledger
row.
