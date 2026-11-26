# M0149\-0049 — Port subscription/t/012\_collation.pl

Status: in progress. TestPort\_Subscription012Collation is written \(three pairings\) and gated by GOOPG\_TAP\_012.

- PG 18.3 publisher → goopg subscriber: passes 3/3.
- goopg publisher → PG 18.3 subscriber: skips, as the .pl does without ICU. The PG 18.3 build is configured without
  `--with-icu`, and the subscriber is the node that creates the ICU collation.
- all\\-goopg: the replica identity index case passes; the REPLICA IDENTITY FULL case waits on M0149\\-0049b.

## Landed — M0149\\-0049a, the apply row lookup honours a nondeterministic collation

Under a nondeterministic collation, PG's texteq asks the ICU collator \(varlena.c texteq → pg\_strncoll\). The apply
worker locates the target row by that equality: through the replica identity index's opclass under the column
collation, or through tuples\_equal for REPLICA IDENTITY FULL. So the publisher's U&'\\00E4bc' finds the subscriber's
canonically equivalent U&'\\0061\\0308bc'. goopg's applyDeleteByKey and applyScanFirstMatch compared bytes; the UPDATE
missed the row and installed a second one.

- `nondeterministicCollators` builds, per column, an equality collator for a column whose collation is an ICU
  collation with deterministic = false \(resolved through FindCollation on the underlying InMemory\). It uses
  golang.org/x/text/collate at the collation's locale and default \(tertiary\) strength.
- `rowMatchesKeyCollated` compares such columns by the collator and the rest as before.

Test: TestApplyKeyMatchNondeterministicCollation.

## Open

- **M0149\\-0049b**: a goopg publisher logs the old tuple of an UPDATE under REPLICA IDENTITY FULL
  \(XLH\_UPDATE\_CONTAINS\_OLD\_TUPLE in xl\_heap\_update at wal\_level = logical; heap\_update → ExtractReplicaIdentity\).
  Without it the subscriber has no pre\\-image and locates the row by the new values.
