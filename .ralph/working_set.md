Task: M0130-S11.4 slice 3b-2c-ii-B2-c — THE FLIP. DONE, committed (fea5e8dd)
+ pushed. A follow-up docs-only commit records the REINDEX-debt finding.

Landed: `pgIndexTupleKeys = true`. Describable indexes are now PG index tuples
on disk; refused indexes keep the blob path, so key format is a per-INDEX
catalog property with nothing on disk recording it (metapage version must stay
4) — hence REINDEX-required. The eight prior slices had covered every key
PRODUCER; the flip's real content was four CONSUMERS:
- `(*BTree).Search`: full-key equality is unsatisfiable once the heap TID is in
  the key (every unique probe said "no such key"); now `compareKeyAttrs` + a
  `_bt_stepright`-style right step (a zero-TID probe descends LEFT of a group
  that starts at a page boundary).
- `indexFormat.compareHigh`: weighed the TID, so every real entry read as ABOVE
  a bound naming its exact key → equality scans returned zero rows. Now
  key-attributes-only. The LOW end keeps the tiebreak deliberately.
- index-only scan: decoded a tuple image with the blob running-offset walk; now
  `pgIndexTupleKeyDatums` (DeformPGIndexTuple + decodePhysicalPGValueMctx).
- amcheck `checkunique`: compared bytewise, had silently STOPPED detecting
  duplicates; now `IndexFormat.CompareKeyAttrs`.

Gates: units PASS; tpch-spotcheck PASS (Q12=2, Q13=35); pgbench smoke PASS;
TPC-DS SF0.5 **PASS=95 ERROR=0 MISMATCH=0 CKMISMATCH=0, plans identical**.

BIGGEST FINDING (not the flip): the first SF0.5 sweep returned 42 ERRORs that
reproduce IDENTICALLY on a gate-OFF rebuild. Both bench clusters had been
carrying un-remediated REINDEX debt from S11.2/S11.3 (page shape + metapage)
since before the last green sweep. REINDEXing all 24 SF0.5 PKs (46s) restored
the baseline exactly. TPC-H is STILL un-remediated: `REINDEX` inside db `tpch`
reports "relation does not exist" (per-DB catalog scoping gap, same one the
ledger records for ANALYZE), so SF=1 index behaviour is ungated and
tpch-spotcheck's Q12/Q13 are seq-scan plans that never touch an index.

Next step (re-read the fix_plan banner first; M-NIGHTLY filing is unconditional
and the six AI-20260810-011258-* items are already filed, left unchecked):
1. **3b-3 — collect the deferrals**: blob MAXALIGN, `_bt_keep_natts` suffix
   truncation, `MaxHighKeyLen`/`bulkHighKeyReserve` → `BTMaxItemSize`, dead
   `backfillBTree`, dead `appendTIDToPosting`/`promoteSingleToPosting`.
2. Candidate ahead of it: fix the REINDEX per-DB scoping gap, then REINDEX the
   TPC-H cluster and add ONE index-driven query to tpch-spotcheck.sh — without
   it the next REINDEX-required slice repeats this exact four-slice blind spot.

In-flight: none. (Bench servers all stopped; /tmp/goopg-preflip is a scratch
gate-OFF build, safe to delete.)
