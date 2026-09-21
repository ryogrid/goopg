# Working set — inter-loop baton

Task: **M0145-0011** — scopes (a)+(b) landed loop 39 (`8b91593c7`); **scope (d)
adjudication is now reported with SF1 confirmation** (this loop, docs-only).
The task stays `[ ]`: scope **(c)/E2 is the only thing left**.

## Banner

Item 3's order ends `… M0145-0010 → M0145-0011 → M0145-0012`. Unchanged this
loop. Re-read it anyway — it moved under us on loop 39.

## What this loop produced — E1 repeated at SF1, and it is CLEAN

Private clone of `bench/tpcds/runtime_goopg/data` (SF=1, store_sales 2 880 404)
at `/tmp/m11sf1` on `:5547`, knob arm, same build, fresh server per arm.
Evidence dir `tmp/m0145-0011-sf1/` (gitignored — numbers transcribed into the
design doc).

```
99 EXPLAINs both arms: only Q77 and Q78 move (Q36/Q70/Q86 "differ" only in the
temp-file name inside a pre-existing grouping-sets syntax error).

Q78  ON   Hash Left Join    CTE cs rows=11    CTE ws rows=7
     OFF  Hash Right Join   CTE cs rows=2280  CTE ws rows=1485     <- no NL shape
Q77  OFF  gains one Nested Loop Left Join rows=1 cost=0.06 (grouping-sets Append)

           ON            OFF          values
Q77    9015 ms 44 r   5306 ms 44 r    ck 9bd1900a34ce55c5 both
Q78   50885 ms 100 r 48066 ms 100 r   ck 3331a74f6d9f53e6 both
```

**The SF0.25 limit is now discharged.** At the scale C-04a's 15 s → 327 s
blowup was measured, the firewall still costs Q78 its honest estimates, buys no
measurable safety, and the one NL shape it would have prevented (Q77) runs
**1.7x faster** without it. Adjudication reported to the owner in the design
doc: the residual blockers' unblock conditions can be redefined as
"PG-equivalent row estimates + measured safety at the scale the catastrophe was
measured", and a relaxation is filed as its OWN task. **The loop does not pick,
and landed no relaxation.**

Residual limit stated in the doc: SF=1 is still not C-04a's exact
configuration, and the hazard shape (epsilon-driven NL over the full fact
table) is not what either arm picks now. That argues for narrowing the guard to
the SHAPE (veto NL paths with a derived inner), not for keeping a decline that
poisons the estimates of every problem it touches.

## Next step

**Scope (c)/E2.** Relax `flattenPulledBodyTree`'s bare-`*SeqScan` rule for
`*CTEScan` leaves on the knob arm — it REQUIRES `GOOPG_DERIVED_FIREWALL=off`
as a pairing, because pulled ANY/EXISTS bodies become JoinSemi/JoinAnti SJIs
and the firewall's jointype switch (`relfromjoinlist.go:604`) covers Semi/Anti.
Then re-run the pull-up/seam decline census and say what it moved. When (c)
lands, M0145-0011 becomes `[x]` and M0145-0012 is next.

Hard constraints still bind: firewall ON for the default arm, `rows<=1` guard
untouched, firewall-off captures are knob-arm private evidence (G8).

## Still pending

- **M0145-0012** `[ ]` — gated on this task.
- Owner escalations open: M0145-0010 (both premises measured wrong),
  M0140-0007 (capability already exists; declined a label-only change).

## Traps carried forward

- A knob-arm capture in the canonical results dir poisons the next default
  sweep's baseline — always redirect `SF025_RESULTS_DIR`.
- Gate stamps hash the staged index — re-run a gate if code changed after it.
- `GOOPG_DERIVED_FIREWALL` is read once at process START — an A/B needs two
  server runs, not two sessions.
- A 3.3G SF1 clone must go to a SHORT path (`/tmp/m11sf1`): a long datadir
  path overruns the 108-byte AF_UNIX limit on `.ctl.sock`.
- `..._arm_test.go` is silently excluded (`arm` is a GOARCH).

## Gates run

units PASS (no production code changed this loop — docs + fix_plan note only).
Pre-commit pgbench smoke runs via the hook.

## In-flight

none
