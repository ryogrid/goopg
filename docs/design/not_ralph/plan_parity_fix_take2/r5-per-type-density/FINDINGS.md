# R5 — per-type storage: one hypothesis confirmed, one falsified

*Round 5 of `../TODO.md`. Findings round, no code change. Converts
K14's two hypotheses into facts by measuring, as R4 §2 required.*

## 1. Method

Single-purpose tables, 200,000 rows each, built and `VACUUM ANALYZE`d
identically on both engines (goopg TPC-H clone :5543, PG :65432 — same
`work_mem`/`shared_buffers` era, and page counts do not depend on
either), then `relpages` compared. All probe tables dropped afterwards
from both clusters.

This is the measurement R4 said to do *instead of* inferring from the
whole-table ratios — and it is why one of R4's two hypotheses did not
survive.

## 2. Results

| table | columns | PG pages | goopg pages | verdict |
|---|---|---|---|---|
| `d_int` | `integer` | 885 | 885 | identical |
| `d_num` | `numeric(7,2)`, values `X.00` | 885 | 885 | identical |
| `d_vc` | `varchar(200)` | 885 | 885 | identical |
| `d_char` | `character(10)` | **1082** | **885** | **DIVERGES** |
| `d_intwide` | 7 x `integer` | 1471 | 1471 | identical |
| `d_multi` | 2 int + 5 `numeric(7,2)` | 1667 | 1667 | identical |
| `d_null` | same, 4 of 5 numerics NULL | 1082 | 1082 | identical |
| `d_frac1` | 1 int + 5 numerics, `X.00` | 1471 | 1471 | identical |
| `d_frac` | 1 int + 5 numerics, `X.37` | 1667 | 1667 | identical |

### 2.1 CONFIRMED — `character(N)` is not blank-padded

PG stores `bpchar` padded to the declared length; goopg does not. A
`character(10)` column holding 4-character values costs PG 1082 pages
and goopg 885 — goopg's is the same size as the `varchar` table.

This is a real on-disk PG-compatibility divergence and it fully explains
R4's two sub-1.0 tables (`item` 0.573, `customer` 0.712 — the only two
carrying `character(N)`).

### 2.2 FALSIFIED — `numeric` storage is identical

R4 hypothesised goopg's `numeric` was larger, because every table above
ratio 1.0 was numeric-bearing. **It is not.** `numeric` matched PG
exactly in every configuration probed, and the probes were chosen to
attack the hypothesis rather than confirm it:

- one column and five columns — identical;
- values with no fractional digits (`X.00`, one `NumericDigit`) **and**
  with them (`X.37`, two) — identical, 1471 vs 1667 pages on both
  engines, so both encode the extra digit and both pay the same for it;
- 4-of-5 columns NULL, which exercises the null bitmap and `t_hoff`
  alignment — identical.

The correlation R4 saw was real and the causal reading of it was wrong.

## 3. What the fact-table gap actually is

`store_sales` remains 14.8% larger in goopg (29,761 pages vs 25,928) and
now has **no representation explanation**: it is `integer` +
`numeric(7,2)` only, with 21 nullable columns at ~4.5% NULL — every one
of those properties measured identical above.

The gap is real, not a statistics artefact: goopg's heap file on disk is
243,802,112 bytes = exactly 29,761 pages, and PG's
`pg_relation_size/8192` is exactly 25,928 with `n_dead_tup = 0`. Both
`relpages` values are true file sizes.

Since the same tuples occupy the same bytes on both engines, the
difference must be **how full the pages are** — the load path leaving
free space PG's does not. ~21.9 bytes per row of unused space, on a
~147-byte PG tuple, is ≈15% — consistent with a fill difference rather
than any per-tuple cost.

That is now the only open component of K14, and it is a **heap
insert/fill** question, not a type-encoding one.

## 4. Correction to K14

K14 recorded two hypotheses. One is confirmed (§2.1), one is
falsified (§2.2), and the fact-table gap is reassigned to page fill
(§3). The K14 framing that "part of this goal is on-disk representation
work" survives but narrows sharply: the *representation* is right
almost everywhere, and what remains is `character(N)` padding plus a
fill/packing difference.

This is the fourth hypothesis of mine falsified in this workstream —
and the first one that was **labelled a hypothesis in advance and
falsified by the check it asked for**. That is the process working
rather than failing, and the difference is worth noting: R4 wrote down
"stated as hypotheses consistent with the data, not as verified
mechanisms … confirming them is the first step of any follow-up", and
that is exactly what caught it.

## 5. Filed

- **`character(N)` blank-padding** — an on-disk PG-compat defect
  independent of this goal. Affects `relpages` (hence every page-priced
  cost) on any `bpchar` table, and is a stored-representation
  difference in a project whose PG compatibility is absolute.
- **Heap page fill on bulk load** (§3) — the remaining 15%. Next step
  is to compare free space per page directly (a page-level dump on both
  engines) rather than to infer again from totals.
