Task: M-NIGHTLY "store NULL-keyed index entries" — S1+S2 83f5635c8; DESC range
wrong-results fixed 63d720bd2 (found preparing S3). Next: S3, relax the
planner guard for tuple-format indexes in capable clusters.
Key symbols: optimizer pathindexrestrict.go indexUnboundKeysNullSafe /
indexUnboundKeysNotNull (every producer calls these), catalog.NullKeyedIndexEntries,
executor pgindex_keydesc.go buildPGIndexKeyDesc (format test; depends on
executor-internal type/comparator tables — do NOT move it).
Plan: dependency injection — catalog holds a func var
IndexStoresNullKeyedEntries(idx) registered by executor at init
(func(idx) bool { _, err := buildPGIndexKeyDesc(idx); return err == nil });
the guard returns true when the capability is on and that predicate holds.
Then expect regress btree_index (SAOP composite probe) and limit
(GroupAggregate over IOS) to recover their index plans on capable clusters;
re-pin the NULL-key guard tests to cover both capability states.
Gates run: units, spotcheck, sf025, acceptance arm, regress subset (DESC fix).
In-flight: none.
