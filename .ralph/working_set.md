Task: **M0127-P5.9-u** — `Datum.Flags` was a serialization contract nobody
declared. COMMITTED; DS05 SF0.5 confirmation run is the only thing outstanding.

**What landed.** `flagDate` is retired for `Datum.TimeSub` (`TimeSubtype`:
Timestamp/Date/TimestampTZ/Time/TimeTZ), carved out of the existing alignment
pad so Datum stays 48 B. The rename is not the point — a *field* with a declared
value space can be enumerated, a flag bit cannot.

**The audit found two more breaks, not one.** The spill codec is goopg's ONLY
Datum-level serializer (storage/wire codecs read a declared column type
alongside the bytes and re-derive the type; a spill frame comes back as a bare
`Row`). Beyond P5.9-s's lost DATE flag:
- **`timetz` lost its UTC offset** (`Datum.Scale`, minutes). SILENT and a live
  wrong answer: `compareDatum` normalises to UTC through it (upstream
  `timetz_cmp`), so spilled timetz sorted by LOCAL time. Measured — with the
  `Scale` write reverted, the guard's compare of `12:00-07` vs `13:00+00` flips
  `+1`→`-1`.
- **`KindEnum` / `KindToastPointer` had no arm at all** — encode wrote a bare
  kind byte, decode rejected the frame. Loud, but a query spilling an enum
  column simply could not run.

**The fix is the contract.** `TestSpillDatumRoundTripCoversEveryKind` /
`…EveryTimeSubtype` walk `datumKindCount` / `timeSubtypeCount` and FAIL on any
kind or subtype the codec has no arm for; `decodeDatum` REJECTS an out-of-range
subtype instead of quietly widening it to a bare timestamp. `Datum.Flags` stays
unserialized on purpose — `flagBigNumeric` is *representation* state the decoder
re-establishes (`newNumeric`), and carrying it would forge an arena mantissa.
Both new guards were proven to bite by reverting each fix in turn.

Files: `internal/executor/{datum,spill,expr,codec,copy_text}.go`, new
`spill_datum_contract_test.go`, 4 touched test files; `09-verification-and-
acceptance.md` §3.21 + `docs/design/README.md`; ledger row + fix_plan.

Gates run: `go build ./...`; units (0 FAIL); regress-port BASELINE-RELATIVE in a
worktree off clean HEAD — 56 tests, **identical verdicts and diff line counts on
both arms** (absolute parity is 1/52 and means nothing; the worktree needs the
untracked `postgres` symlink or `pg_isready` is missing); `tpch-spotcheck` Q12=2
Q13=35 PASS; pgbench smoke via the commit hook.

NEXT LOOP (banner in `.ralph/fix_plan.md` wins — M0127 is #4 and current):
**-t** (port `reduce_outer_joins` so a RIGHT arrives as a LEFT, then widen
`spineLinkSearchable`; full 09 §3 bar), **-p** (searched-arm hash batch-growth
fixture, units only). Larger: 03 §4.4 `SpecialJoinInfo` inference for the outer
link buried below an inner one (Q78). Ledger P5.9-u follow-up: populate
`TimeSubTime`/`TimeSubTimestampTZ` at their producers and switch `compareDatum`
off the `Scale != 0` timetz inference (it mis-reads a `+00` timetz as a plain
`time`) — behaviour change, own bar.

Nightly triage: `ci/logs/action-items.md` is still run 20260806-011323; all 18
subjects already filed under M-NIGHTLY. Nothing new.

In-flight: see the status block — DS05 SF0.5 result is recorded there.
