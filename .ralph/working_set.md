Task: M-NIGHTLY "store NULL-keyed index entries" — design slice done this loop
(docs/design/0100-0149/m-nightly-store-null-keyed-index-entries.md); slices
S1/S2/S3 filed under the task in fix_plan.
Key symbols: operators_index.go lookupRangeBounds (hiKey nil when open),
operators_indexonly.go lookupRangeBounds, operators_bitmap.go lookupBounds,
pgindex_btree.go indexBuildEntryKey/indexRowKey/arbiterKey,
operators_storage.go indexRowKeyValues (keep for probes), amcheck
operators_bt_index_check.go ~530, pgindex_keydesc.go buildPGIndexKeyDesc.
Findings: probes already skip NULL values; the leak risk is open-ended
ranges; old indexes need the cluster flag (global/pg_goopg_features).
Next step: S1+S2 together probably (S1 needs real NULL entries to test):
start with the marker file (initdb write, open read, process flag).
Gates run: none (docs only).
In-flight: none.
