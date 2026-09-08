# R9 — the jointype filter the partial hash-join producer never had

*Round 9 of `../TODO.md`. Implements K17. Blocking prerequisite for
R10 (flipping `GOOPG_GATHER_PATHS`).*

## 1. The bug

`addPartialHashJoinPath` (`joinpathsparallel.go:80`) takes
`jt parser.JoinType` and **never compares it to anything** — there is no
jointype test in the file. It files a parallel-aware partial path for
whatever direction it is handed, including RIGHT.

Consequence, measured in R8: `GOOPG_GATHER_PATHS=all` **crashes** on
TPC-DS Q5, with `assertParallelAwareJoinIsRunnable` reporting a `2/1`
(`JoinTypeRight`/`JoinAlgoHash`) join whose *"workers' verdicts are not
row-local, so the join would silently drop or duplicate rows"*.

The assertion's own comment asserts the opposite of the code:

> *"`addPartialHashJoinPath` files only INNER hash joins today — since
> C-03b it is passed the direction's jointype and declines outright for
> SEMI/ANTI"*

That describes a filter which does not exist. Correcting the comment is
part of this round, not an afterthought: it is what made the defect
invisible to reading.

## 2. What the filter must be

Two authorities agree, and the filter must satisfy both.

**PG** — `hash_inner_and_outer`'s parallel block
(`joinpath.c:2418`) files partial hash joins for `JOIN_INNER`,
`JOIN_LEFT`, `JOIN_SEMI` and `JOIN_ANTI`, and never for RIGHT or FULL
(a right/full hash join needs every worker to see the whole outer to
decide unmatched rows).

**goopg's executor** — `hashJoinIsPartialCapable`
(`parallel.go:759`): INNER/SEMI/ANTI unconditionally, LEFT when
`!BuildLeft`, everything else false.

The hash arm always builds on the right — `createHashJoinPlan` records
*"Outer drives the probe, inner is hashed … BuildLeft stays false"* — so
LEFT is capable in practice, and the two authorities coincide on
**{INNER, LEFT, SEMI, ANTI}**.

## 3. Deriving rather than hand-listing

K17's lesson is that a hand-written claim and the code drifted apart
silently. So the filter is not a fresh literal list: it is expressed as
a single predicate, `partialHashJoinTypeOK(jt)`, placed next to
`hashJoinIsPartialCapable` in `parallel.go`, with a test that pins it
**against** `hashJoinIsPartialCapable` for every jointype — so if the
executor predicate is ever narrowed, the producer's filter fails its
test rather than silently over-filing.

That is the actual fix for the class of bug K17 represents; the
one-line `if` is only the fix for the instance.

## 4. Gates

- Unit: every `parser.JoinType` value, asserting the producer's verdict
  agrees with `hashJoinIsPartialCapable` on the corresponding `*Join`;
  RIGHT and FULL explicitly declined.
- Regression: `GOOPG_GATHER_PATHS=all` must plan TPC-DS Q5 without
  panicking — the exact reproduction R8 recorded. This is the round's
  real acceptance test.
- Suites: optimizer + executor.
- Values: both corpora. Default behaviour is unchanged (the knob is
  still off), so this is a no-op check — but "it's a no-op" was the
  assumption that hid K17, so it is measured rather than asserted.

## 5. Prediction

- Default-off behaviour: **byte-identical plans on both corpora**. The
  filter only removes candidates from a list nothing currently reads.
- `GOOPG_GATHER_PATHS=all`: TPC-DS Q5 plans instead of crashing;
  `Parallel Hash Join` counts drop somewhat from R8's 19/132 as RIGHT
  joins stop being filed.
- No parity movement at the default. R10 is where movement is decided.

## 6. Review record

Subagent delegation unavailable (`Task` not exposed; recorded since R0).
Self-review:

- **Both authorities were read, not one**: PG's parallel block and
  goopg's own executor predicate. Filtering to PG's set alone would
  have re-created K17 in the other direction, since goopg's LEFT
  support is conditional on `BuildLeft`.
- **The `BuildLeft` claim was checked** at `createHashJoinPlan`, which
  states it stays false for the hash arm — that is what makes LEFT
  admissible here rather than an assumption that it is.
- **The fix is deliberately a derived predicate**, because the instance
  bug and the class bug are different, and K17 was a class bug.
