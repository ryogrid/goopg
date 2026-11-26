# M0149\-0049 — Port subscription/t/012\_collation.pl

Status: done. TestPort\_Subscription012Collation passes 3/3 in all\-goopg and in PG 18.3 publisher → goopg subscriber. The
goopg publisher → PG 18.3 subscriber pairing skips, as the .pl does without ICU: the PG 18.3 build is configured without
`--with-icu`, and the subscriber is the node that creates the ICU collation.

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

## Landed — M0149\-0049b, a goopg publisher logs the old tuple of a REPLICA IDENTITY FULL HOT update

heap\_update logs ExtractReplicaIdentity's old tuple in xl\_heap\_update's main data with XLH\_UPDATE\_CONTAINS\_OLD\_TUPLE
when the relation's replica identity is FULL at wal\_level = logical, and DecodeUpdate passes it to the output plugin.
goopg's HOT update record carried no old tuple, so a REPLICA IDENTITY FULL subscriber had no pre\-image and keyed the
lookup by the new row. goopg's non\-HOT user UPDATE is a delete \+ insert pair whose delete already carries the old
tuple.

- `hotUpdateLogsOldTuple` gates on wal\_level = logical, a permanent user table and REPLICA IDENTITY FULL. A HOT update
  changes no key column, so DEFAULT and USING INDEX need nothing.
- tryApplyHOTUpdate captures the old tuple before stamping it. It travels through LogHeapHotUpdateFunc to
  `EncodeHeapHotUpdatePGOld`, which appends xl\_heap\_header \+ tuple data to main data and sets the flag. Redo ignores
  it, as heap\_xlog\_update does.
- classifyDecodedXLog's HOT\-update arm decodes it into Change.OldTuple when CONTAINS\_OLD\_TUPLE or \_OLD\_KEY is set.

Test: TestClassifyPGHeapHotUpdateOldTuple. TestPort\_Subscription012Collation is ungated.
