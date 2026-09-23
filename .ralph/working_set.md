Task: M-NIGHTLY "store NULL-keyed index entries" — S1+S2 landed 83f5635c8;
next S3: relax the planner guard for tuple-format indexes in capable clusters.
Key symbols: optimizer pathindexrestrict.go indexUnboundKeysNullSafe /
indexUnboundKeysNotNull (all producers call these), catalog.NullKeyedIndexEntries
(the capability flag, readable from optimizer), executor pgindex_keydesc.go
buildPGIndexKeyDesc (the format test; optimizer cannot import executor).
Findings: the format predicate must move to a package the optimizer imports
(catalog, or nbtree if its comparator table lives there). Keep DESC columns
guarded (ledgered: no NULL stop for DESC, range producers ignore DESC).
Next step: find what buildPGIndexKeyDesc depends on (comparator table
package) and expose an eligibility predicate the optimizer can call.
Gates run (S1+S2): units, spotcheck, sf025, acceptance arm, regress subset.
In-flight: none.
