# M0147-0052 — recon: why regress joins bypass the PG-shaped search

Date: 2026-10-27, planner code as of `5a9b2fb84`.

## Method (temporary probe, never committed)

One regress-suite run (`TestPort_RegressSuite`, which passes its environment to the servers) with three
append-to-file probes, recorded in `probe-regress-summary.txt`:
- every `traceSeamDecline(reason, nrels, …)` in `joinsearchseam.go`, which is the seam's existing decline vocabulary;
- every `tryPGShapedJoinSearch` exit (`used=true|false`);
- every unsearched `*Join` the post-search NLI walk reaches, with its JoinType (0 Inner, 1 Left, 2 Right, 3 Full,
  4 Cross, 5 Semi, 6 Anti).

## Result

- Seam calls: 18 754 searched (`used=true`), 10 869 declined.
- 9 716 of the declines are `size-or-no-joinlist nrels=0`: single-relation statements, which need no search, as in PG.

The remaining declines, by reason, against the unsearched joins the walk reached:

| decline reason | events | maps to |
|---|---|---|
| `lateral` (nrels 1–5) | 968 (918 at nrels=2) | the 889 unsearched CROSS joins: `FROM t, LATERAL …` |
| `leaf-count` | 87 | |
| `outer-on-qual` | 45 | LEFT/RIGHT joins whose ON carries a preserved-side-only conjunct or a constant; the M0147-0051 witness `o LEFT JOIN i ON i.k = o.k AND o.id > 0` |
| `prefix-exceeds-bindings` | 19 | |
| `outer-link-no-sjinfo` | 19 | |
| `chain-not-flattenable`, `semianti-on-qual`, `residual-hits-pad`, `inner-on-qual-above-outer` | 9 | |

Unsearched joins reached by type: CROSS 889, LEFT 159, FULL 103, INNER 75, SEMI 37, RIGHT 28, ANTI 15.

The decline events are per seam call, while the join counts are per final tree, so the two columns are not
one-to-one. They are close enough to route each class.

## Routing

- `lateral` and `leaf-count` are the classes banner S9(2) routes to M0145-0003/0010. They are not refiled here.
- `outer-on-qual` is filed as an impl child. PG keeps a preserved-side-only outer-join ON conjunct as a join
  clause of that outer join: `distribute_qual_to_rels`, `postgres/src/backend/optimizer/plan/initsplan.c`, leaves
  `is_pushed_down` false and the clause is evaluated in the join's restrictlist. So PG's search builds these joins,
  and goopg's `outerOnQualsOK` declines them. Fixing this class also gives the legacy NLI post-pass's known
  customer a searched plan, which unblocks M0147-0051.
- `prefix-exceeds-bindings`, `outer-link-no-sjinfo` and the 9-event tail stay listed here for a later recon. None
  carries a TPC-H or TPC-DS query; all three corpora are fully searched.
