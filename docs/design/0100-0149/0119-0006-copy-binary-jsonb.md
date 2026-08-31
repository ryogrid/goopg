# 0119-0006 — Binary `COPY` of `jsonb` (M0119-0006, 56th slice)

Status: implemented (2026-08-13)
Scope: `internal/executor/copy_binary.go`, `internal/executor/copy_binary_jsonb_test.go`

## The defect

`copy_binary.go` had no arm for `jsonb` in either direction, so both halves fell
through to the default's `KindString` case — goopg carries `json`/`jsonb` as a
`KindString` Datum holding the JSON text (`expr.go:evalJSONArrow`). The default
ships that text verbatim and hands the received bytes back as a string, which
for `jsonb` is wrong by exactly one byte at each end:

| direction | goopg at HEAD | upstream |
|---|---|---|
| out | `{"a": 1}` | `\x01` + `{"a": 1}` (`jsonb_send`, `postgres/src/backend/utils/adt/jsonb.c:124`) |
| in | column gets `\x01{"a": 1}` | version checked and stripped (`jsonb_recv`, `jsonb.c:89`) |

Both are real breakage, and the pair is *symmetric*, which is why it survived:
goopg talking to goopg round-trips fine, so only a comparison against a real PG
exposes it. Verified on the oracle (PG 18.3, port 65432) by stripping the version
byte out of a PG-authored stream and feeding it back:

```
ERROR:  unsupported jsonb version number 123
CONTEXT:  COPY zz_jsonb_one, line 1, column j
```

123 is `0x7b`, i.e. `{` — every binary `COPY` of a `jsonb` column goopg produced
was unreadable by PostgreSQL. In the other direction a PG-authored stream landed
in a goopg `jsonb` column as `\x01{...}`, text that is no longer valid JSON, so
every later `->`/`->>` raised 22P02 at read time.

## Upstream shape

```c
jsonb_send:  pq_sendint8(&buf, version /* 1 */);
             pq_sendtext(&buf, jtext->data, jtext->len);   /* JsonbToCString */
jsonb_recv:  int version = pq_getmsgint(buf, 1);
             if (version == 1) str = pq_getmsgtext(...);
             else elog(ERROR, "unsupported jsonb version number %d", version);
             return jsonb_from_cstring(str, nbytes, false, NULL);
```

Two things follow. First, **the jsonb binary wire format is textual after the
version byte** — `jsonb_send` serialises the tree back to a C string rather than
shipping the `JsonbContainer`. That is what makes this slice possible at all:
goopg's storage is not PG's (below), but its *wire* bytes can be exact.
Second, `jsonb_from_cstring` **parses**, so a malformed body is an error at COPY
time, not a poisoned column.

`json` is deliberately left with no arm: `json_send` **is** `textsend` — bare
text, no version byte — which is precisely what the default arm already emits.
`TestCopyBinaryJSONHasNoVersionByte` pins that, so a later edit cannot
"helpfully" give `json` the version byte and break it the way `jsonb` was broken.

## The change

- `datumToCopyBinary`: `case "jsonb"` → `jsonbBinaryVersion` (1) + the text.
- `copyBinaryToDatum`: `case "jsonb"` → length ≥ 1, version must be 1 (else the
  upstream wording), then `isValidJSONText` on the remainder before
  `NewStringDatum`.
- `isValidJSONText` is `jsonb_from_cstring`'s check: `encoding/json` with
  `UseNumber` (same configuration `evalJSONArrow` uses, so a number never
  round-trips through `float64`) **plus** a trailing-token check — `Decode` alone
  stops at the end of the first value and would accept `{} {}`, which PG rejects.

## Sibling-path audit (Hard-won Rule #2)

`TestCopyBinaryJsonbAgreesWithHeapEncode` is the pin that found the adjacent heap
defect in the 53rd slice (the `float` spelling) and the 54th (the halved `xid8`).
Here it compares the heap varlena's *body* against the COPY payload after the
version byte, and confirms `physicalPGTypeAlign(jsonb) == 4` (pg_type 3802's
typalign `'i'`). Both agree.

That agreement is also the slice's honest limit, and it is worth stating rather
than reading as a clean bill of health: **goopg's heap image for `jsonb` is
varlena TEXT, while upstream's is a `JsonbContainer`/`JEntry` tree** (`typstorage
'x'`, `postgres/src/include/utils/jsonb.h`). The two twins agree with each other
because they are wrong together. A hosted real PG reading a goopg `jsonb` column
off disk would read the JSON text as a JEntry header. That is a large, separate
slice and is ledgered under M0119-0006, not fixed here — the COPY wire format is
textual, so it is genuinely independent.

The working-set rule of thumb from the 55th slice ("expect the adjacent defect
when the type SHARES another type's heap arm") predicted a defect here, since
`jsonb` rides the default varlena-text arm. It was right about *where*, and wrong
only about *size*: the defect is not a byte-level slip that this slice could
absorb, it is the storage format itself.

## Verification

- `TestCopyBinaryJsonb*` / `TestCopyBinaryJSONHasNoVersionByte` — 5 of the 7 fail
  against HEAD-minus-the-change. The two that pass are the round-trip pins, which
  is the symmetric-bug signature above: the shape pins (`SendShape`,
  `AgreesWithHeapEncode`, `RowFraming`) are what actually catch it.
- Oracle E2E vs PG 18.3 (port 65432), goopg on a capped throwaway server (5533):
  `\copy … TO (FORMAT binary)` of a 7-row `(int, jsonb)` table including a NULL —
  **byte-identical** files; and cross-ingest in both directions (`goopg.bin` into
  PG, `pg.bin` into goopg) renders identically.

## Deferred (ledger rows filed under M0119-0006)

1. **Heap `jsonb` is text, not a JEntry tree** — the storage-format gap above.
2. **goopg does not canonicalise `jsonb` on input.** PG stores a tree, so
   `jsonb_out` always emits canonical text (object keys sorted by length then
   bytewise, duplicate keys collapsed last-wins, whitespace normalised, numbers
   through `numeric`). goopg echoes the input text verbatim, so
   `'{"b":1,"a":2}'::jsonb` renders as itself where PG renders `{"a": 2, "b": 1}`.
   This slice's E2E therefore used already-canonical literals, which isolates the
   COPY arm from that gap rather than hiding it.
3. `bpchar` is now the last type named in the outstanding binary-COPY arm list.
