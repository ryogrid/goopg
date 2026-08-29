# M0134-0178 — `ALTER TEXT SEARCH CONFIGURATION` mapping: token-type validation and deduplication

*Case:* `postgres/src/test/regress/sql/tsdicts.sql` (M0134-0178).
*Status:* case **PARKED** (dominant cause is the tsearch dictionary engine, see
§4); the contained correctness bug below is **FIXED**.

## 1. What the case is, and why it is parked

`tsdicts.sql` exercises PostgreSQL's text-search *dictionary* layer end to end:
ispell/hunspell affix files, `synonym`, `thesaurus`, and the snowball
`english_stem` dictionary, driven through `ts_lexize()`, `to_tsvector()`,
`to_tsquery()` and `phraseto_tsquery()`.

Sized at HEAD: **899 diff lines / 100 `^+ERROR`**. The breakdown is almost
entirely one subsystem:

| count | `+ERROR` | cause |
|---|---|---|
| 71 | `function ts_lexize does not exist` | no dictionary lexize layer |
| 10 | `function to_tsvector does not exist` | same |
| 10 | `function to_tsquery does not exist` | same |
| 1 | `function phraseto_tsquery does not exist` | same |
| 3 | `text search dictionary "english_stem" does not exist` | initdb creates no snowball dictionaries |
| 2 | `text search dictionary "hunspell_err" already exists` | `CREATE TEXT SEARCH DICTIONARY` does not validate affix files, so an intentionally-invalid one is accepted |
| 6 | *(the mapping block — fixed here)* | see §2 |

goopg models text search as a **schema-dump round trip only**: the catalog
carries `pg_ts_dict` / `pg_ts_config` / `pg_ts_config_map` rows so `pg_dump`
re-emits the DDL, but nothing tokenizes or lexizes. Closing the remainder means
porting `src/backend/tsearch/` — `spell.c` (~1700 lines of affix/dictionary
file parsing), `dict_ispell.c`, `dict_synonym.c`, `dict_thesaurus.c`,
`regis.c`, plus the generated snowball stemmers and the `share/tsearch_data/`
sample files the case loads by name. That is REFACTOR-tier and is ledgered as
**0178a**; re-arm this case when it lands.

## 2. The contained bug: `getTokenTypes` was never implemented

The case's trailing "Test grammar for configurations" block is independent of
every dictionary above — it only checks how `ALTER TEXT SEARCH CONFIGURATION`
handles its `FOR tok [, ...]` token-type list. goopg diverged on **all six**
assertions in it.

Upstream routes every mapping form through one function,
`getTokenTypes(prsId, tokennames)` (`src/backend/commands/tsearchcmds.c:1229`),
called first by both `MakeConfigurationMapping` (ADD / ALTER / ALTER … REPLACE,
line 1310) and `DropConfigurationMapping` (DROP, line 1507). It does two things:

1. **Deduplicates** — "Duplicated entries list are removed from tokennames".
   A repeated name is skipped via `tstoken_list_member` *before* it is
   validated or acted on.
2. **Validates against the configuration's parser** — each name is matched
   against the parser's `lextype` alias table; a miss raises
   `ERRCODE_INVALID_PARAMETER_VALUE` (22023)
   `token type "%s" does not exist`.

Crucially the validation runs **before** the mapping lookup and **before**
`get_ts_dict_oid` resolves the dictionary names, and it is **not** downgraded by
`DROP MAPPING IF EXISTS`: `missing_ok` covers a missing *mapping*, never a token
type the parser cannot emit.

goopg's four mapping paths in `internal/executor/operators_ddl.go`
(`execAlterTSConfigAddMapping`, `execAlterTSConfigAlterMapping`,
`execAlterTSConfigDropMapping`, `execAlterTSConfigReplaceDict`) looped
`s.TokenTypes` directly. Neither behaviour existed — even though
`catalog.TSConfigMapping.TokenType` already carried the comment
*"validated against DefaultParserTokenTypes"* and `catalog.TSTokenTypeID` was
already the exact lextype lookup needed. (Third instance of the recurring
"doc comment describes behaviour the body never performs" shape, after
M0134-0171's `pkColumns`.)

Observable consequences, all six confirmed by revert-check:

| statement | PG 18.3 | goopg at HEAD |
|---|---|---|
| `ADD MAPPING FOR not_a_token WITH d` | `ERROR: token type "not_a_token" does not exist` | **silently created a mapping for a token type the parser can never produce** |
| `ALTER MAPPING FOR not_a_token WITH d` | same ERROR | silently accepted |
| `DROP MAPPING FOR not_a_token, not_a_token` | same ERROR | wrong error: 42704 `mapping for token type …` |
| `DROP MAPPING IF EXISTS FOR not_a_token, not_a_token` | same ERROR (IF EXISTS does not apply) | swallowed into two NOTICEs |
| `DROP MAPPING FOR word, word` | succeeds (deleted once) | 42704 on its own second pass |
| `ADD MAPPING FOR word, word WITH d` | inserts one row | 23505 collision with its own first insert |

The first row is the data-integrity half: an unvalidated token type is written
into `pg_ts_config_map` with `maptokentype = -1` (the ADD path's own
duplicate-detail lookup already returned `-1` for unknown aliases, which is how
the gap was visible in the code but never enforced), producing a catalog row no
real parser can ever match and which `pg_dump` would re-emit as an
un-restorable statement.

## 3. The fix

`resolveTSTokenTypes` (`internal/executor/operators_ddl.go`) mirrors
`getTokenTypes`: dedup-then-validate, returning the unique validated list, and
propagating an empty input unchanged — the bare
`ALTER MAPPING REPLACE old WITH new` form, where "no tokens named" means "every
mapped token" (`ReplaceTSConfigMappingDict`'s `len(tokenTypes) > 0` guard).

All four mapping paths now call it **first**, matching upstream's ordering: an
invalid token type must error before an unresolvable dictionary name does.

Token names arrive already lower-cased from the parser
(`parseTSTokenTypeCommaList`, `internal/parser/ddl.go:7320`), so the
case-insensitive `catalog.TSTokenTypeID` lookup and upstream's `strcmp` against
the lextype alias agree for every unquoted spelling. They diverge only for a
*quoted* mixed-case token name (`FOR "WORD"`), which upstream rejects and goopg
accepts — ledgered as **0178b**, a parser-level issue, not a mapping one.

## 4. Result and gates

`tsdicts.sql`: **899 → 863** diff lines, `^+ERROR` **100 → 97**, `^-ERROR`
**8 → 5**; the whole mapping block is byte-clean. The residual is exactly the
dictionary engine of §1.

Guard: `internal/executor/tsconfig_token_type_validation_test.go` — two tests,
one per half, both revert-checked against the pre-fix file (validation: 4/4
statements returned `nil`; dedup: `DROP MAPPING FOR word, word` failed 42704).
The existing `TestAlterTSConfigAddMappingDuplicateRaises23505` invariant is
re-pinned inside the dedup test: deduplication is **per statement**, so a
genuine re-ADD in a *separate* statement must still raise 23505.
