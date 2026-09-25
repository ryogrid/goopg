# Seam admission for pulled `*CTEScan` leaves (M0145-0013)

Status: LANDED 2026-09-21, knob arm only. The admission machinery works and
the declines it targeted are gone; the plans it unlocks are **mispriced**, and
that residual is reported, not hidden.

Task: `.ralph/fix_plan.md` M0145-0013. Kind: impl. Parent: M0145-0011.

## The invariant this closes

M0145-0011's scope (c) relaxed `flattenPulledBodyTree` (the producer) to admit
`*CTEScan` leaves and measured that every admitted body then died at the seam's
own re-check. The bare-`*SeqScan` rule was **one invariant held at three
sites** — one producer and two consumers (`pulled-leaf-not-scan` and
`flat-leaf-not-scan`, both in `tryPGShapedJoinSearch`).

Both consumer sites now route through two shared helpers in
`internal/optimizer/joinsearchseam.go`:

- **`seamLeafBinding`** — the single admission point. `*SeqScan` binds to its
  catalog relation; `*CTEScan` is admitted with `table` left **nil**; anything
  else is refused with the site's existing decline reason. Pulled leaves take
  their alias from the body's own `jtPulledBody.bodyBindings` record (whose
  comment reserved them for exactly this) with only the offset re-stamped onto
  the problem's span; the flattened-link leaves have no body-order index and
  pass `nil`.
- **`seamLeafRelInfo`** — pricing, routed on `b.table` rather than on the node
  type. A real relation goes through `estimateBaseRelInfo` +
  `applyRelSizeFallback`; a derived leaf is priced with `EstimateRows` plus the
  local filter's selectivity, which is the same arm the seam already applies to
  an opaque Semi/Anti RHS leaf a few lines below.

### The nil table is load-bearing

`leafIsDerivedInput` reads `binding.table`, so leaving it nil is what keeps the
`outer-over-derived` firewall in force over an admitted derived leaf. That is
the ordering the milestone requires — admission machinery in 0013, firewall
relaxation last in 0018 — and `TestSeamLeafBindingAdmission` pins it so a later
"helpful" synthesis of a `catalog.Table` cannot lift the firewall a milestone
early without failing a test.

No per-column statistics are synthesised for the derived leaf. M0145-0009's
census established that the remaining CTE-output column asks are columns PG
itself leaves unknown, so inventing them would be precision the oracle does not
have either. `EstimateRows(*CTEScan)` recursing the body is goopg's equivalent
of `set_cte_size_estimates` propagating the subplan's `plan_rows`
(`postgres/src/backend/optimizer/path/allpaths.c`).

## Measurement — the expected movement, achieved

Seam decline census, TPC-DS SF0.25, knob arm
(`GOOPG_JOINTREE_PIPELINE=1 GOOPG_PULLUP_CTE_LEAF=on`), firewall ON, all 99
queries, same cluster, pre-change binary built from a worktree at HEAD:

```
reason                    before   after
pulled-leaf-not-scan          19       0     <- the target
leaf-count                    15      15
semianti-not-tail              6       6
outer-over-derived             6       6
residual-hits-pad              0       4     <- the new wall
flat-leaf-not-scan             0       0
```

The pull-up census is unchanged (`(pulled)` 72, `SubqueryExpr` 29,
`any-nested-sublink` 12, `ExistsExpr` 4, `InExpr` 2), as it must be: 0013
changes nothing upstream of the seam.

**Correctness gate — values identical on every moved statement.** Exactly three
queries move (Q14, Q23, Q95) and no others across all 99:

```
Q14  ck 7d36a1f744b34462  200 rows     Q23  ck 68b329da9893e340  1 row
Q95  ck 01a182a4af6d2fbc    1 row
```

all three byte-identical to M0145-0011's captures of the same queries.

**The refactor is inert where it should be.** On the knob arm with
`GOOPG_PULLUP_CTE_LEAF` off, the pre-change and post-change binaries produce
**0/100 differing plans** — so the shared-helper rewrite of the `*SeqScan` path
changes nothing, and the movement above is entirely the new admission.

## The residual: admission works, pricing does not

The plans the search now reaches are more expensive than the fallback shapes
they replace, in estimate and in fact:

```
              estimated cost                measured runtime (knob arm)
          leaf-off   leaf-on   0013      leaf-off   leaf-on   0013
Q14         (n/a)     27949   40055        13068     14490   16341
Q23         (n/a)      8389   23428        15024     15545   12867
Q95         (n/a)     70693 1232685         3004      3230    9148
```

Q23 improves (-14% against the honest `leaf-off` baseline), Q14 costs 25% more,
and **Q95 is 3.0x slower**. Values are identical throughout, so this is a
pricing problem, not a correctness one: the DP can now form these join orders
and picks badly among them.

This is the same shape the milestone has hit repeatedly — an unwinnable path is
an untested path, and making it winnable exposes what it was never costed for.
It is **not** a reason to withhold the machinery: the default arm cannot reach
any of it (`GOOPG_PULLUP_CTE_LEAF` defaults off, so no CTE leaf ever arrives at
the seam), and 0013's own scope is "knob arm only; default arm byte-identical".

## What is deferred

1. **The Q95 misprice** — the first thing M0145-0018's "fresh E1
   re-verification keeps the relaxed plans clean" precondition will trip on.
   Ledgered.
2. **`residual-hits-pad`, 4 fires** — the new wall for the statements that used
   to die at `pulled-leaf-not-scan`. Unexamined here; it is a different
   invariant (residual-qual placement against the problem's pad), and naming it
   is what the next census round is for.
3. The hard constraint about a `pulled` mark suppressing the legacy subplan
   route on a seam decline is **unchanged, not discharged**: 0013 reduces how
   often the seam declines but does not alter what happens when it does.
