# EX1-03 Design — Bound-narrowed detoast + per-attribute resolver

Item: `TODO_EXECUTOR.md` EX1-03 (gate: TOAST contract tests per type;
values + pin). Status: design for review.

## 1. Witness correction (recorded, not hidden)

The TODO's witness clause ("TOAST-heavy TPC-DS shapes") is ungrounded:
TPC-DS max width is varchar(200), `ToastThreshold` is 2000 — zero toast
pointers exist at any SF (verified: no SF0.5 TSV line exceeds 1900 chars
total). The witness is a SYNTHETIC shape: `w(id, narrow text, wide
text)` with wide >2000 B; matrix {SELECT narrow/wide, WHERE wide,
ORDER BY wide, join on wide, index on wide} × values-diff vs PG oracle
+ `DetoastValue`-call counts. The TODO line is amended accordingly at
 close. Pointer-producing types: text/varchar/char/bpchar/bytea plus
 unknown/json/jsonb/xml where verified produced (the `isToastableType`
 set, not a subset; numeric/fixed-width never toast — negative test).

## 2. Mechanism (four sub-slices, one commit)

- (a) Bound-narrowed `DetoastRow`: pass the EX1-01/02 deform/survivor
  bound in; resolve only pointers at `i < bound`. Soundness comes
  from the walk alone — narrowed detoast is sound IFF the
  reference walk is complete ("same guarantee as narrowing" is the
  whole story; there is NO independent guard: poison is test-only
  and default-off, and when armed it OVERWRITES tail slots with an
  Int sentinel, masking pointers rather than detecting pointer
  reads). Pair with prefix-scoped `needsDetoast`-style scanning
  (`needsDetoastPrefix` pattern): whole-row `needsDetoast` on a
  narrowed row reads the previous tuple's tail and a stale-tail
  false positive hits `continue // skip undetoastable tuple` —
  i.e. could skip a LIVE tuple. Whole-row `DetoastRow` survives
  only where no bound exists (DML/EPQ/COPY paths — untouched).
- (b) `DetoastAttr` per-attribute resolver: NEW (exists nowhere yet) —
  thin wrapper over `DetoastValue` + kind-restore, to be ADOPTED by
  the row-lazy UPDATE SET-clause site (which is row-lazy, not
  attr-lazy today). Single-column resolution leaves sibling pointers
  untouched (contract test pins this new behavior).
- (d) Decode-path arm for json/jsonb/xml TOAST pointers
  (`codec.go`): the type-independent `encodeRowPGCtx` arm writes
  pointers for these types but the decoder's json/jsonb/xml arm
  raised "decode as varlena" on the 13-byte pointer blob and the
  scan silently skipped the tuple (measured: SELECT over a toasted
  json column returned 0 rows). Same 13-byte/`0x01` shape argument
  as the text arms.
- (c) Barrier audit, KEPT EAGER, tabulated loud-vs-silent (a walk
  miss at a silent sink is wrong answers; at a loud sink an error):
  SILENT — `compareDatum` mixed-kind (string-compares the
  placeholder), `datumKey` users (hash join/group-by keys, memoize
  probe key, window partition key), `rowKey` users (DISTINCT,
  recursive-UNION dedup), aggregate transfn inputs via
  `StringValue()`/`Format()` (raw pointer bytes concatenated),
  DISTINCT sort comparator, window peer compare. LOUD — COPY TO
  (errors), `pgIndexTupleKey` (refuses), `compareDatum` same-kind
  pointer-vs-pointer (42883). Spill is TRANSPORT, not a barrier
  (round-trips pointers opaquely; keys must be materialized BEFORE
  spill — restated, not assumed). No lazy movement at any silent
  sink — stated, not attempted; tests assert silent sinks never
  observe `KindToastPointer`.

## 3. Cost model honesty

Each resolved pointer costs one TOAST-relation sequential scan (no
chunk index). The win is purely fewer resolutions (∝ referenced cols),
never faster resolution. The witness matrix asserts `DetoastValue`-call
counts, not just time.

## 4. Verification (gate)

- Contract tests per pointer-producing type (text/varchar/char/
  bpchar/bytea; plus unknown/json/jsonb/xml iff verified produced —
  match the `isToastableType` set, not a subset): >2000 B round-trip
  restores value+kind; single-col resolve leaves siblings; narrow-only
  query performs zero `DetoastValue` calls (counter — run poison-OFF
  / assert counts, since armed poison masks pointers); barrier sites
  never see pointers; numeric negative test.
- Values: synthetic matrix in a SCRATCH database on both engines
  (goopg throwaway :5533/5534 preferred; PG ref :65432 `tpch` only if
  scratch-local schema) — row counts + checksums; TPC-H values-diff
  24/24 + TPC-DS PASS=95 (regression, not witness); plan-gate 22/22.
  `DetoastValue`-call counting needs a new atomic counter — stated.
