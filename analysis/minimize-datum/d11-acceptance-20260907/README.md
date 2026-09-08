# D-11 — MD acceptance over the 2026-09-07 end state

TODO_ALL row D-11, re-scoped 2026-09-07 onto D-06 (the only conversion
site still in scope). D-06 has now landed as a measured negative with
its switch OFF (`dc85ea8f9`), D-10 is out of scope on verified grounds
(ledger `take3-D-10-out-of-scope`), and this file is the acceptance the
row demands: the 06 §5 six conditions checked against the tree as it
stands, plus the ownerless ledger rows the row files.

Date: 2026-09-07. Verdict: **ACCEPTED with the OFF caveat stated, not
waived** — every condition holds, two of them vacuously, and the two
vacuous ones name exactly what would un-vacate them.

## 1. Values unchanged every commit, both suites — HOLDS

- E-01 (B): TPC-DS SF0.5 sweep **PASS=95 MISMATCH=0 CKMISMATCH=0
  ERROR=0 TIMEOUT=0 SKIP=4** on the (B) binary; TPC-H digest 22/22 OK
  with canonical row counts; spotcheck Q12=2/Q13=34 PASS.
- D-06: TPC-H digest **24/24 MATCH** switch-OFF vs switch-ON (ordered +
  unordered + column signatures) — the packed path is values-identical
  suite-wide; executor suite + units scope green; spotcheck PASS on the
  landing binary.

## 2. One retention format — HOLDS (production)

Production retention is `[]Row` of 48 B Datums at every site. The only
other format in the tree is D-06's packed sort path, which is
default-OFF, env-gated, and unreachable without `GOOPG_SORT_PACKED=on`.
R-4 (two formats) is closed by construction **in production**; the
OFF-gated second format is the measured-negative instrument §7 keeps
for the revert story, not a second live format. If the switch ever
flips, this condition re-opens.

## 3. Model matches storage — HOLDS

`hashsize.EntryBytes` prices `48·ncols + 24` (`TestEntryBytesUsesGoopgWidths`
pins it), and production storage IS `[]Row` of 48 B Datums — model and
storage agree by construction and by test. The D-06 design's §5.1
follow-up (price the RETAINED width) is filed against a conversion that
did not land in production; it is not owed by this acceptance.

## 4. Batch-count witness moved and recorded — VACUOUS, with reason

No production conversion landed (D-06 OFF; hash sites out of scope on
the parallel-cost decision), so no batch count could move and none did.
This is not a gap in the acceptance — it is the consequence the D-06
measurement (`wall +103 %`, DESIGN §8b) forces. Un-vacated by: a
production conversion that moves an `nbatch`.

## 5. `Datum` still 48 bytes — HOLDS

Compile-time pinned (`datum.go`: `const _ uintptr = 48 -
unsafe.Sizeof(Datum{})`, M0107-0002). Untouched by every commit in this
session.

## 6. Byte-format fidelity stated honestly — HOLDS

- D1 goldens: D-09 landed with live-PG-18.3 byte-identical goldens
  (328 B tuple).
- D2 (PG-format TOAST pointers): out of scope, ledgered open with
  resume points (`take3-D-02-enum-encode` et al.; TOAST-heavy corpus
  does not exist — TPC-DS max varchar(200) < threshold).
- D3–D4: non-issues in goopg's type system (03 §8).

## 7. Ownerless rows filed by this acceptance

- The PG-format TOAST-pointer gap: out of scope, resume point required
  — carried by the rows above, nothing new filed.
- D-10's out-of-scope verdict: ledger `take3-D-10-out-of-scope` (no-win
  by construction + TD-5 premise inverted + D-04 rule binds).
- No D-08 slice was ever started, so no keep-open row is owed.

## 8. No time target — per 06 §5, none set, none claimed
