# M0127-P5.9-l-ii — the enumeration-provenance channel, live smoke

`2026-08-06-p59lii-dptrace-smoke.txt` is the `DPTRACE` block a real goopg
server emitted for one 4-relation join, captured to prove the channel works
**in a server** and not only in unit tests. It is NOT the clause-6 measurement;
that needs the TPC-H SF=1 arm, which was blocked this loop (see below).

## What produced it

Throwaway cluster, port 5533, capped per `.ralph/AGENT.md`:

```
go build -o tmp/goopg-dptrace-bin ./cmd/goopg
./tmp/goopg-dptrace-bin init -D tmp/dptrace-data --no-sync
GOOPG_PGSHAPED_DP=1 GOOPG_PGSHAPED_DP_TRACE=1 GOOPG_CG_UNIT=goopg-dptrace \
  GOMEMLIMIT=2GiB scripts/goopg-test-run.sh \
  ./tmp/goopg-dptrace-bin start -D tmp/dptrace-data --listen 127.0.0.1:5533
```

Four tiny tables (`nation n1`, `supplier`, `customer`, `orders`, 25/1000/1500/5000
rows), ANALYZEd in the same session (goopg stats are per-connection), then

```sql
EXPLAIN SELECT count(*) FROM customer, orders, supplier, nation n1
 WHERE c_custkey = o_custkey AND c_nationkey = n1.n_nationkey
   AND s_nationkey = n1.n_nationkey;
```

## What it shows

1. **The chosen plan is bushy** — `{customer+orders} ⋈ {n1+supplier}` — and the
   trace records that partition at `phase=2`, the bushy pass. The plan side and
   the search side agree on a live query, which is the property the whole
   channel rests on.
2. **Aliases survive.** `nation n1` is recorded as `n1`, not `nation`. Without
   that, Q7's two `nation` scans collapse into one relset member on the search
   side while staying distinct on the plan side, and every Q7 verdict would be
   noise.
3. **`created=0` on the phase-2 line.** The bushy pairing did NOT create the top
   joinrel — a phase-1 pair reached it first, and the bushy pair added its paths
   to the existing rel, which is `make_join_rel`'s find-or-create working as
   designed. It also means "was this relset built" and "was this PARTITION
   offered" are genuinely different questions, which is the reason this channel
   exists at all.
4. **Refusals are recorded with a cause.** Five `decline … reason=no-join-clause`
   lines name the pairs the connectivity gate withheld, so a missing partition
   can be attributed instead of merely observed.

Adjudicated end-to-end through `estimateaudit.ParseEnumTrace` + `Adjudicate` +
`RenderEnum` (the same path `estimate-audit --enum-trace` takes):

```
  control   smoke OFFERED        {customer+orders} ⋈ {n1+supplier}
      phase=2 lev=4 created=false top={customer+n1+orders+supplier}
  candidate smoke SIDE-NOT-BUILT {n1+orders} ⋈ {customer+supplier}
      no joinrel was ever built over {customer+supplier}
  RATCHET enum_controls=1/1 enum_candidates_offered=0/1 enum_problems=1 enum_malformed=0
```

The "candidate" there is a partition of an unconnected pair, chosen precisely
because it is genuinely absent — the negative arm of the instrument, verified to
report a *named* gap rather than silence.

## What is still missing

The clause-6 measurement itself: Q7's `{customer+lineitem+n2+orders} ⋈ {n1+supplier}`
and Q8's `{lineitem+orders+part} ⋈ {customer+n1+region}` (P5.9-l-i's two
candidates), with Q20's matched bushy pairing as the control. The command is

```
DP_TRACE=1 PGSHAPED=1 NO_BUILD=0 \
  scripts/tpch-estimate-audit-arm.sh 2026-08-0X-p59lii-enum-on --queries 7,8,20
```

It was not run this loop because the nightly CI batch held the host, and the arm
script refuses to run beside it (`FORCE=1` overrides — do not: a bench arm run
concurrently with the nightly lane contaminates both measurements).
