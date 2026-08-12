(idle — nothing in flight)

M0119-0006 41st slice landed and pushed. Item stays UNCHECKED (it is the standing
slice-by-slice cluster; 3 new ledger rows filed, 1 flipped to resolved).

Selection note for the next loop: banner order re-verified this loop. M-NIGHTLY
filing done (action-items.md still run `20260812-005501`, all four `## AI-` items
already filed — nothing new). **M0131 has no runnable item** (S9 CLOSED — its
successor M0133 is filed but deliberately NOT promoted; S24 deferred-with-ledger),
and M0130 has zero unchecked items. Beware: `awk '/^## M0130/,/^## M0131/'` on
fix_plan.md is a FALSE reading — the M0131 section is at the END of the file, so
that range swallows all of M0119/M0122 and reports 14. Fall-through lands on
M0119-0006 again.

Landed this loop:
- `internal/executor/copy_binary.go` (`copyBinaryToDatum`), `copy_text.go`
  (`copyTextToDatum`), `operators_indexonly.go` (`decodeIndexKeyColumn` +
  `decodeBTreeKeyToDatum`), `pg_authid_sync.go` (`buildAuthidUserRow`) — the five
  doors that KNOW the declared SQL type now mint `NewTimestampTZDatum` /
  `NewDateDatum` instead of a bare `NewTimeDatum`.
- New `internal/executor/timestamptz_origin_tag_test.go` (3 tests), design doc
  `docs/design/0119-0006-timestamptz-datum-origin-tags.md`, README row
  `0119-0006y`.

Worth carrying:
- **Ask the negative question too.** The ledger row named only the timestamptz
  gap; writing "what must NOT be tagged?" exposed that `date`'s behavioural
  `TimeSubDate` was set by the heap decode and by NONE of the four decoders — a
  bigger divergence than the one being fixed, in the same arms.
- **The heap decode is the model.** `codec.go`'s `decodeValuePG` was already
  correct for both subtypes; every other decoder is a sibling of it (Rule #2),
  so "does this door do what codec.go does?" is a mechanical audit question.
- A ledger row's SUSPECTS are hypotheses, not findings: the row's fourth suspect
  (pgoutput decode) had no `NewTimeDatum` site at HEAD and was retracted, not
  implemented.

Gates: `internal/executor` PASS, units PASS, `TestPort_RegressSuite` PASS (175 s),
`scripts/tpch-spotcheck.sh` PASS (Q12=2, Q13=35), pgbench smoke via the commit
hook PASS, `make ralph-state-guard` OK (auto-repaired the stale marker).

In-flight: none.
