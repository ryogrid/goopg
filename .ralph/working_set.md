(idle — nothing in flight)

## Loop summary (2026-07-12, loop #63)

**Nightly triage:** action-items batch `20260711-011536` (same as #58–#62) —
all 3 AI items already `[x]` in M-NIGHTLY. No new nightly work.

**Task — m0003 / KindDate carrier (0003-0013): storage-decoded dates now carry
`flagDate`.** Dates share the `KindTime` carrier with timestamps; only the
`flagDate` bit distinguishes them for `Datum.Format()` (date shape `MM-DD-YYYY`
vs timestamp `YYYY-MM-DD HH:MM:SS.ffffff`). `decodePhysicalPGValueMctx`'s `date`
case returned a **flagless** `NewTimeDatum`, so a date read back from on-disk
storage rendered via `Datum.Format()` as a full timestamp
(`2001-02-16 00:00:00.000000`) — hitting `date::text` casts, string concat, and
array/composite element rendering. (Plain `SELECT date_col` was already correct:
the wire encoder in server/dispatch.go re-derives format from the column type.)

Landed:
- internal/executor/datum.go: new `NewDateDatum(t)` (sets flagDate).
- internal/executor/codec.go: decode `date` case uses `NewDateDatum`.
- internal/executor/codec_date_test.go: `TestDateDecodeCarriesDateFlag` (+neg
  case: decoded timestamp not tagged).
- unimplemented_feat.json m0003 KindDate entry open→resolved (surgical Edit,
  JSON revalidated).
- docs/design/0003-0013-between-operator.md new Follow-up section; the deferred
  bullet flipped to "Closed 2026-07-12".
- deferral_ledger.md `resolved` row.

Verified flag-agnostic: compareDatum + datumKey key on the KindTime instant only
(no ordering/grouping/join impact). Encode stays type-driven (encodeValuePG keys
on catalog.Type) — encode↔decode sibling pair agree.

Gates: go build ./... clean; internal/executor + internal/server suites PASS;
scripts/tpch-spotcheck.sh PASS (Q12=2/Q13=33 — l_shipdate/o_orderdate exercise
the decode path). pgbench smoke via pre-commit hook at commit.

**Stale bookkeeping noticed (NOT fixed — different milestone, one-task rule):**
fix_plan.md M0122-0004 lines ~4305-4320 still say "RANGE value-offset … still
rejected 0A000", but commit 3b98c119 CLOSED it. A future M0122-0004 loop should
reconcile that "Still open in this bucket" note (only sub-day/multi-component
intervals actually remain).

Next loop: unimplemented_feat.json still ~81 open. Bounded candidates: per-slot
catalog-xmin retention hook; `pg_get_expr()` stub (returns empty string).

In-flight: none
