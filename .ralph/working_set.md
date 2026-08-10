(idle — nothing in flight)

Last loop: M0119-0006 **16th slice landed** — `boolean` input from an unknown
literal. `INSERT INTO t(b) VALUES ('true')` raised `XX000 expected bool, got
kind 3`; new `pgBoolIn` reproduces upstream `boolin` and is now the single
source of the spelling table for all four sites that had copied it. `boolean[]`
columns were unwritable by any spelling and are fixed by the same arm. Audit
result worth carrying: bool was the ONLY strict-Kind holdout among the 15
`expected …, got kind` arms in `encodeValuePG` — everything else already routes
`KindString`, so no sweep is owed.

Banner state (re-read this loop): **M0130 is now fully checked** (zero unchecked
items in its whole section) and M-NIGHTLY has no open items — all 6
`AI-20260810-011258-*` are filed and checked. So the banner falls through to
M0119, then M0122.

Next loop: continue M0119-0006 (largest open cluster). Remaining named in the
task: posting-list duplicate coverage in the checkunique tier,
`box`/`int4range`/`interval` key encodings, and the whole-database (unscoped)
pg_amcheck run. Also newly filed: the ASCII-vs-Unicode whitespace-trim
divergence across ALL type input functions (one answer owed, not per-type).
