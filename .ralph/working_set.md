(idle — nothing in flight)

Last loop: M0119-0006 **20th slice landed** — the `numeric` COLUMN stops being
the decimal string and gets PG's base-10000 `NumericData`. This was the LAST
member of the heap-side-divergence class (interval → uuid → numeric).

Findings worth carrying:

- The serializer was already in the tree. `internal/pgnodes/datum.go` had a
  full `numeric_in`/`numeric_out` port written for pg_node_tree (a numeric
  `Const`'s constvalue IS the on-disk varlena). The slice EXPORTED it
  (`internal/pgnodes/numeric_storage.go`) rather than writing a second port —
  worth checking pgnodes first for any future type flip.
- Unlike uuid/interval, pre-flip DATA exists everywhere (every bench cluster),
  so the decoder reads BOTH forms. The discrimination is exact, not heuristic:
  a payload spellable from `[0-9+-.eE]` is always legacy text (short/special
  headers have a high byte ≥ 0x80, long-form digits ≤ 0x27, long-form zero
  0x00). Proof + sweep tests in the design doc.
- Flipping `pgIndexKeyImageIsPGFaithful` changes the ON-DISK index key format
  for that type, and the format is recomputed at open time from the catalog —
  so numeric-keyed indexes built earlier need REINDEX (ledger row). Six
  executor tests that probed numeric indexes with hand-built BLOB keys had to
  move onto the engine funnels; the new `indexProbeMultiForTest` is the
  compound-key twin of the existing `indexProbeForTest`.

Banner state (re-read this loop): M-NIGHTLY's six 20260810-011258 items are all
filed AND checked; M0130 fully checked; banner falls through to M0119, then
M0122.

Next loop: continue M0119-0006. Candidates: `numeric[]`/`uuid[]`/`interval[]`
array elements (three ledger rows now want the SAME array-codec slice — highest
value), posting-list duplicate coverage in the checkunique tier,
`box`/`int4range` key encodings, the whole-database (unscoped) pg_amcheck run.

Gates run: `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` PASS;
`scripts/tpch-spotcheck.sh` PASS (Q12=2, Q13=35) on the pre-flip cluster;
`scripts/tpcds-sf05-regression.sh sweep` PASS=95 MISMATCH=0 CKMISMATCH=0
ERROR=0 TIMEOUT=0 SKIP=4 (57 ck-verified, log /tmp/sf05_m0119_numeric.log);
pre-commit pgbench smoke PASS.

In-flight: none
