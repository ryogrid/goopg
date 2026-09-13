# R118 result: lower Hash Joins attributed to estimateJoin nd-branch nullfrac inputs

R118 implemented the approved producer-audit sidecar, measured, and removed
all temporary source (this report's evidence is retained paths/checksums, not
code). No production cardinality, schema, Datum, cost, statistics, Gather,
executor, or search behavior was changed at any point; the final tree is
byte-identical to its pre-diagnostic state modulo the removed diagnostic.

## Controlled measurement

Private Goopg cluster `tmp/codex-to-other-260913/data/r111-goopg-q96` at
`127.0.0.1:5568`, binary `tmp/codex-to-other-260913/bin/r118-goopg`
(SHA-256 `056bfe68f14f722f64ab2525cc2cbdb3c2218626176ee7b0efa39fb6a92384bf`),
session `work_mem=64MB`, `join_collapse_limit=1`,
`from_collapse_limit=1`, server `GOOPG_GATHER_PATHS=top`,
`GOOPG_PARTIAL_AGG_PATHS=on`, `GOGC=off`, `GOMEMLIMIT=12GiB`.
Both R111 forced forms, TEXT and JSON, OFFx2 and ONx2
(`GOOPG_Q96_PRODUCER_AUDIT=1`), values `266` both forms.

Controls: OFF pairs byte-identical with zero audit output; ON pairs
byte-identical modulo the documented unique per-statement/per-render
tokens (`token=paN`, `render=rX` — uniqueness is itself a requirement);
ON-body content equals OFF content (psql rule/count framing only);
TEXT JSON-key sets agree (`Plan` vs `Plan`+`ProducerAudit`).

Retained artefacts (`tmp/r118/`, fresh for R118):

| file | SHA-256 |
| --- | --- |
| hdem-off-1.plan | `55e91b90fd50fc71ebf2dbaf110662675cc70c9187e31a0d5d21d3287a23c2bf` |
| store-off-1.plan | `e2e425453508b8d331f774199ce8bb0cdd00a9457d0253a36db55f1dae09aecc` |
| hdem-off-1.json | `4062ac3fc2c7d4110d1cadd8a5811f7ad5afcc9dda79e6310d6261727e91ff8c` |
| store-off-1.json | `1d45e82df60731e9c76c5f62ae2a1c7b620d9fcb6eed1441d57820d4dfe42d22` |

The OFF TEXT checksums reproduce R117's retained values exactly —
cross-round output stability independent of this diagnostic.

## Producer attribution (both lower joins)

Route: `legacy-direct` (planner.go join site), established live by the
R118 route probe (3× `legacy-join`, zero path-backed firings on either
forced form) before any recording was built. Census: 3 Join occurrences
per form; lateral probe observed as `lateral-out-of-population`
(R117 ledger=none precedent, not a falsifier); upper + lower Hash Joins
`one-to-one`, equations reproduce from the final nodes, schemas match
construction snapshots. Stop field empty both forms.

Lower-join equations (frozen, byte-rendered in the ON captures):

| forced order | l | r | superkey | pair (nd-branch) | residual | rowsfloat | final |
| --- | --- | --- | --- | --- | --- | --- | --- |
| hdem-first | 719876 | 7200 | sel=1, bound proven, non-binding (bound=719876) | 0.0001328287035640743 = 0.956366666/7200 | 1 | 688465.41 | 688465 |
| store-first | 719876 | 12 | sel=1, bound proven, non-binding (bound=719876) | 0.07965277787297964 = 0.955833334/12 | 1 | 688081.48 | 688081 |

No MCV involvement, no superkey firing, residual 1, no saturation, no
fallback. The 384-row divergence is 100% the pair null-selectivity
factor, and since r == nd exactly in both joins it reduces further to
the outer-key nullfrac alone:

- `(1 - nullfrac(ss_hdemo_sk)) = 1 - 0.043633334 = 0.956366666`
- `(1 - nullfrac(ss_store_sk)) = 1 - 0.044166666 = 0.955833334`
- inner nullfracs both 0 (hd_demo_sk, s_store_sk), read live from
  `pg_stats` on the measurement cluster.

719876 × (0.956366666 − 0.955833334) = 719876 × 0.000533332 = 384.
A 0.05% ANALYZE-measured nullfrac difference, amplified by the
719,876-row outer, is the entire lower-join cardinality divergence.
`pairNullSelectivity` (`cardinality.go`) multiplies measured per-side
`(1 - nullfrac)` exactly as priced; no transcription gap is visible at
this site — the inputs differ because the COLUMNS differ (forced-order
construction), and the statistics are measurements, not estimates.

Schema/width side: both lowers carry `mergedSchema@planFromItem`
lineage with single-writer snapshots matching finals (SchemaOK).
476 vs 1104 is structural — different right relations by forced-order
construction (ss 428 + hdem 48 vs ss 428 + store 676), both columns
listed per position in the frozen rows — not a writer divergence.

## Disposition

R118 attributes the first differing selected ledger to ANALYZE-measured
outer-key nullfrac inputs of the `nd` equi-selectivity branch, with no
model gap at the pricing site. A successor may scope a production change
only with a PG18.3 comparison showing PG prices THESE inputs differently;
on their face the inputs are faithful measurements of differing columns,
and "fixing" them would mean overriding statistics. The Datum-size
investigation stays closed per the user's R114 stop. All temporary
diagnostic source (implementation, tests, flag registration, renderer
hooks, refactor) was removed; the tree diff for this report is docs only.
