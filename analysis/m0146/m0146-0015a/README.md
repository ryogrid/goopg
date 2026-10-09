# M0146-0015a evidence

Throwaway clusters seeded like regress `test_setup` + `create_index`, but
without the explicit `int4_ops` opclass (see "explicit-opclass restart
defect" below). PG 18.3 on port 5535 for every expected value.

- **Regress `subselect` query** (`analysis/m0146/m0146-0015/query.sql`):
  - Before the fix: pre-flip 207 s, HEAD over 1 h.
  - After (`5dd5144e1`): about 197 s standalone, the pre-flip level. Both
    SubPlans use index keys again.
  - In `TestPort_RegressSuite` the whole `subselect` file now completes in
    11.9 s (`regress-suite-result.txt`); it stays skipped for other
    divergences.
  - PG's 4 ms needs the sublink pull-up (M0146-0015b).
- **One-level plan:** goopg now matches PG,
  `Index Only Scan … Index Cond: (thousand = a.thousand)`.
- **`probes-correlated.sql`:** 12 correlated shapes. The fixed build matches
  PG on all except probe 6 (an outer reference in a LEFT JOIN ON clause),
  which errors identically at HEAD (`probes-correlated-goopg-head.out`). It
  is pre-existing and filed.
- **`nested-exists-*.sql`:** the shapes an intermediate draft got wrong
  (goopg returned 0 / 0 / 299 against PG's 4 / 0 / 297). The landed change
  matches PG.
- **Gates:**
  - `sf025.txt`: the sweep after the fix.
  - `tpcds-fireset.txt`: fires Q6 only, cost-only, parity unchanged.

## Explicit-opclass restart defect (found here, pre-existing)

`opclass-restart-repro.sh`: create an index with `USING btree (a int4_ops)`,
stop cleanly, start. `count(*) … where a < 50` then returns 0, where it
should return 49. Without the explicit opclass the result is correct. It
reproduces on `b4154eddf`, before this loop's changes. Filed as an S2
wrong-results task.
