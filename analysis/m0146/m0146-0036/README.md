# M0146-0036 — TPC-DS SF1 Q74 timing on PG's plan shape (recon, 2026-10-05)

The question was whether goopg executes Q74's SF1 plan, which matches PG's
since M0146-0005ap, slower than it should. The fire set saw 617-654 s
against a 600 s limit.

Measured at HEAD `92df3ef69` on a private `cp -a` clone of the SF1 cluster
(:5533, work_mem 512MB, max_parallel_workers_per_gather 4), with the PG
reference `:65438` read-only:

| engine | plain execution | EXPLAIN ANALYZE |
|---|---|---|
| goopg | 512 s | 505 s (`q74-sf1-goopg-explain-analyze.txt`) |
| PG 18.3 | 891 s | 933 s (`q74-sf1-pg-explain-analyze.txt`) |

- Both outputs are identical: 100 rows, psql output md5
  `a6418b9910b207e34fbcbe24c0358e59`.
- The plans are the same, and both spend almost all their time in the top
  Nested Loops' Join Filters over CTE Scans of `year_total`. Every CTE Scan
  is estimated at rows=1 (PG's own estimate), so the joins are nested loops,
  and the first evaluates its filter 1,442,391,911 times (37,708 × 38,252).
  The later ones add 163M and 17.7M evaluations. Rows-removed counts are
  equal on both engines.
- goopg runs that loop about 1.7× faster than PG.

Conclusion: no executor gap. The 5-9% delta was goopg's own previous
(non-PG) plan against PG's plan. The fire set's 600 s SF1 limit is below
what PG itself needs for this query (891 s), so a Q74 timeout there is a
gate artefact, not a regression.
