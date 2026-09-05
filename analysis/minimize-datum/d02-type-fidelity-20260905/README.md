# D-02 (MD-02) — derived-column type fidelity audit

Date: 2026-09-05. Item: `docs/design/not_ralph/minimize_datum/TODO_ALL.md`
D-02. Design under audit: `04-target-design.md` §3, §3.1, §9.2 (risk R-1).
Document-only item; no code landed for it.

The audit's job is to decide whether the intermediate-row half of the
minimize_datum bundle can pay, by counting how often a plan node's output
schema carries a column type that `NewTupleDesc` would have to decline.
**It is allowed to stop the bundle.**

## Verdict: **PROCEED**

Dynamic census over both corpora: **0 declining columns out of 160,302
schema columns, 0 of 5,876 plan nodes, 0 of 985 retention sites.** The
static half below is PROCEED-shaped and the dynamic half confirms it with
counts. Two qualifications belong in the close-out, not the verdict:

- **The margin is thinner than the zero suggests.** `l_shipdate + interval
  '1 day'` in a SELECT list types `unknown`, which is NOT Kind-stable;
  TPC-H only ever writes that in a WHERE clause, where it lands in a
  `Filter.Predicate` rather than any node's `Output()`. That is luck of the
  corpus, not a planner property. Likewise `percent_rank`, `cume_dist`,
  `corr`, `regr_*` and `percentile_cont` all type `float8`, which the
  derivation shows is also not Kind-stable. D-05's stopping rule should
  re-run this census on any corpus change.
- **The row-weighted half is formally unmeasured**, not measured-and-zero:
  the in-process fixture catalogs carry no statistics, so all 1,865 nodes
  with a search-produced `PlanRows` report exactly 1.0. Any non-negative
  weighting of an empty declining set is zero, so the verdict does not turn
  on it.

## Dynamic census (in-process planning, no server)

| | TPC-H | TPC-DS | total |
|---|---|---|---|
| queries planned | 22 of 23 sub-statements | 97 of 100 | — |
| plan nodes | 281 | 5,595 | 5,876 |
| schema columns | | | 160,302 |
| nodes with >= 1 declining column | 0 | 0 | **0** |

| split | nodes | declining |
|---|---|---|
| RETAINING (hash build, sort, agg, window, setop, distinct) | 985 | **0** |
| STREAMING (scan, filter, project, limit, join) | 4,891 | **0** |

Every type name that appears in any schema: `integer` (70,906 columns),
`char(n)` (41,692), `decimal` (28,296), `varchar` (9,982), `date` (4,062),
`numeric` (3,554), `int8` (985), `text` (490), `timestamp` (271), `int4`
(52), `int` (12). All packable. Restricting to DERIVED columns
(`SourceTableIdx == 0`) gives 10,731 columns drawn from the same eleven
names — no `unknown`, no `float8`, no `<elem>[]` anywhere.

The reason is mechanical rather than lucky in one respect worth recording:
`buildAggregateCall` initialises `outType` to `unknown` but its default arm
overwrites it with `numeric` (`planner.go:9418`), and `buildWindowFunc` has
a named count/sum/avg/min/max switch ahead of its `unknown` fallback
(`planner.go:6876-6893`), so `stddev_samp` and `avg(sum(x)) over (...)`
land on `numeric` rather than `unknown`.

**Positive control** (the instrument is not vacuous): planted against the
TPC-H catalog, the census correctly flags `array_agg(l_returnflag)` →
`char[]`, `array_agg(l_quantity)` → `numeric[]`, `percent_rank()` and
`cume_dist()` → `float8`, `l_comment::bit(3)` → `bit`, and
`l_shipdate + interval '1 day'` → `unknown`, while correctly NOT flagging
`stddev_samp(l_quantity)` → `numeric`.

**Queries that did not plan (4, none affecting the verdict):** TPC-H
Q15-main (fixture limitation — the harness catalog cannot create the view
Q15 needs; its view body was planned instead) and TPC-DS query36 / query70 /
query86. The TPC-DS three are a dsqgen fixture defect, not a goopg gap —
the generated file puts a statement terminator inside a derived table and
PG rejects it too, which is why the SF0.5 oracle marks them SKIP. Repaired
in memory they then hit a real goopg gap (ORDER BY referencing a select-list
alias from inside a `CASE`), filed separately. All three are
`rollup` + `grouping()` + `rank() over` shapes whose types
(`int4`, `int8`) are packable, so including them would not have changed the
count.

## Static half: the design correction that had to land first

Every base-column type in both benchmark suites is packable, and every
statically reachable derived output-column type is packable. Nothing in the
static picture blocks the bundle.

But the audit found that the design's own definition of the allow-list
would have produced a **false STOP**, so the verdict is conditional on the
correction that is now folded into `04-target-design.md` §3.1.

## The correction (this is the load-bearing finding)

`04-target-design.md` §3.1 specified:

> `packableType` reports whether `t` has **a named arm in
> `encodeValuePGCtx` / `decodePhysicalPGValueLowered`**

Read literally, that declines `text`, `varchar`, `character varying`,
`bpchar`, `character`, `json` and `jsonb` — **every text column in both
suites** (13 `varchar` columns in TPC-H, 50 in TPC-DS). Those types have no
*named encoder* arm because the encoder's shared default
(`internal/executor/codec.go:1055`) already **is** their correct encoder;
they round-trip `KindString → KindString`. An audit run against the literal
predicate would have reported a catastrophic decline rate that is an
artifact of how the list was derived.

The predicate is instead: the union of the two switches' named arms, plus
the text-like spellings the shared default handles correctly, minus the
arms that are not Kind-stable.

## Not Kind-stable (must be excluded from the allow-list)

The dynamic derivation (which extracts both switches mechanically rather
than transcribing them) found **51 packable spellings and 22 declining**,
and corrected the static list in two places:

- **`char` splits on `Args`.** `char(n)` is varlena text and Kind-stable;
  bare `"char"` is NOT — the encoder writes one raw byte and accepts
  `KindInt`, while the decoder always returns `KindString`. So
  `packableType` must read `t.Args`, not just `t.Name`.
- **The whole float family is not Kind-stable** (`float4`, `float8`,
  `real`, `double precision`, `double`, `float`): `floatTextDatum` returns
  `KindNumeric` for finite values but `KindString` for NaN and Infinity, so
  the decoder arm is kind-ambiguous. The static half had not identified
  this.

| group | names | round trip |
|---|---|---|
| encoder-only blob arms | `oid[]`/`_oid`, `int2[]`/`_int2`, `float4[]`/`_float4`, `anyarray`, `char[]`/`_char`, `oidvector`, `int2vector` | `KindBytes` in, `KindString` out |
| array text | `text[]`, `_text` | `KindBytes` in, `KindString` out |
| untyped | `unknown` | text both ways, so any non-`KindString` input retypes |
| content-dependent | `pg_node_tree` | `KindBytes` or `KindString` depending on content |
| user arrays (`IsArray`) | any element type | `KindBytes` blob in, `KindString` out |

The blob arms are a **deliberate convention**, not an unnoticed bug:
`internal/initdb/catalog_heap_reload.go:573-596` re-parses that raw payload
by hand (`parsePGVectorInt16Payload` for `pg_index.indkey`,
`parsePGCharArrayPayload` for `pg_proc.proargmodes`), and the comment there
explicitly names the varlena default arm as its input. So no on-disk data is
lost. They are unpackable for **intermediate** rows all the same.

## Types actually present in the suites

| suite | base column types | all packable? |
|---|---|---|
| TPC-H | `numeric` (28), `varchar(n)` (13), `char(n)` (16), `timestamp` (4) | yes |
| TPC-DS | `integer` (188), `char(n)` (98), `decimal(p,s)` (80), `varchar(n)` (50), `date` (12), `time` (1) | yes |

Derived output-column types reachable from the two query corpora:
`numeric`, `int8`, `int4`, `float8`, `text`, `bool`, `date`, plus
pass-through base types. All packable.

`CHAR(25)` keeps `Name == "char"` with `Args == [25]`; the parser
(`internal/parser/ddl.go:5311-5314`) only adds an implicit `Args=[1]` to a
bare `char`, it never renames to `bpchar`.

## The one open question the static half cannot close

`unknown` is producible by `exprType` from eight sites
(`internal/optimizer/planner.go:13304, 13395, 13435, 13453, 13767, 13777,
13796, 13798`) and is **not** Kind-safe. Statically, every `unknown`-producing
construct found in the two suites sits in **predicate** position — `upper(...)`
inside a `WHERE` in TPC-DS Q24, and `date ± interval` inside `BETWEEN` — and
predicates are evaluated by streaming nodes that retain nothing (04 §4). But
absence from a plan node's `Output()` schema cannot be proven by reading
source. That count is exactly what the dynamic half must produce.

Also unpackable but absent from both suites: `numeric[]` (from `array_agg`,
`planner.go:9260`), and `tid` / `point` / `void` / the six range types from
`exprType`.

## Latent bugs found (ledgered, not fixed here)

Every declining type is also a latent on-disk retyping bug on a path that
already ships (04 §3.1). Three rows filed in `.ralph/deferral_ledger.md`:

1. `take3-D-02-enum-encode` — enum values cannot be encoded at all. The
   index-scan output path (`operators_index.go:726-742`) converts enum
   columns to `KindEnum`, and `coerceTextLikeDatum` has no `KindEnum` arm,
   so a write path consuming such a row hits the hard error at
   `codec.go:156-157`. **Loud**, not silent — a lesser class than R-1, but
   real. Not executed (this audit was static).
2. `take3-D-02-float-spelling` — the bare `float` spelling is in the two
   codec switches but missing from `pgPhysicalTypeIsVarlena` and
   `catalog.PhysicalTypeAlign`, so it would be written as 8 fixed bytes
   while the descriptor claims varlena / 4-byte alignment. No live entry
   point (the parser normalises `float` to `float8`), so this is drift, not
   demonstrated corruption.
3. `take3-D-02-jsonb-text` — `json`/`jsonb` are stored as text varlena, not
   PG's binary jsonb. Kind-stable, so not R-1, but a fidelity divergence of
   the class already closed for `uuid` and `numeric`.

## A correction to 03 §5's premise

The design worries about **three** transcriptions of one type list
(encoder, decoder, allow-list). There are already **four** in tree —
`encodeValuePGCtx`, `decodePhysicalPGValueLowered`, `pgPhysicalTypeIsVarlena`
(`codec.go:1492`) and `catalog.PhysicalTypeAlign`
(`internal/catalog/physical_align.go:18`) — and they already disagree, which
is finding 2 above. `packableType` would be the fifth. This strengthens,
rather than weakens, 03 §5's argument that the list must be derived rather
than written beside the switches.

## Stale citations corrected in the design

| design said | actual |
|---|---|
| default arm at `codec.go:1039-1046` | `codec.go:1055-1063` |
| decoder comment at `codec.go:1981-1987` | `codec.go:2033-2047` |

Also added: the default arm is **not total** — `coerceTextLikeDatum`
(`codec.go:132`) errors on `KindEnum` and `KindToastPointer`
(`codec.go:156-157`), so those fail loudly instead of retyping.
