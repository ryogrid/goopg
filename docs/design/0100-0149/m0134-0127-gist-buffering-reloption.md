# M0134-0127 — `gist.sql` sizing + `WITH (buffering=...)` enum validation

Status: PARKED (`failed`), one contained fix shipped, case does not flip green.

## Sizing

`scripts/pg-regress-runner.sh gist` at HEAD (before this loop): 0% parity,
433 diff lines against `postgres/src/test/regress/expected/gist.out`
(188-line source). Three independent root causes:

1. **No GiST physical-index plan integration.** Every predicate on a
   `USING gist` index (`p <@ box(...)`, `b <@ box(...)`, `circle(p,1) @>
   circle(...)`) EXPLAINs as `Seq Scan` with a `Filter` in goopg instead of
   PG's `Index Scan`/`Index Only Scan`. A GiST index is catalog-only today —
   the same shape as the already-documented GIN catalog-only gap
   (`docs/design/m0134-0126-gin-lateral-srf-arg.md`) and the SPGiST gap
   (`create_index_spgist.sql`, M0134-0111). REFACTOR-tier, its own
   subsystem — candidate for a dedicated GIN/GiST/SPGiST physical-index
   milestone once all three are sized (now true: gin.sql M0134-0126,
   create_index_spgist.sql M0134-0111, gist.sql M0134-0127).
2. **Geometry type-system gap.** `circle(point(...), 1.0)` raises `function
   circle does not exist` — the same already-tracked blocker documented in
   `docs/design/m0134-0125-geometry-sizing.md` (box.sql/circle.sql/
   geometry.sql shared blocker).
3. **The KNN distance operator `<->` is not lexed/parsed at all.**
   `order by p <-> point(0.201, 0.201)` raises a syntax error
   (`expected expression (got ->)`) rather than resolving to the distance
   operator; `grep -rn '"<->"' internal/parser/` returns zero hits anywhere
   in the codebase. This is a new confirmation of the geometry
   operator-lexer-family gap named in the M0134-0125 sizing doc ("point/
   lseg/line/path/polygon typed-literal parsing + operator lexer family"),
   not a fresh root cause — `<->` is PG's GiST/SP-GiST KNN ordering operator
   (`amcanorderbyop`), squarely in that family. Fixing it requires both the
   lexer/parser change and a KNN ORDER-BY plan path, which depends on (1)
   above to be useful (no physical index to order by).

## Contained fix shipped this loop

While sizing the file, a fourth, independent, and genuinely narrow bug
surfaced in the diff's very first hunk — unrelated to the three REFACTOR-tier
blockers above and cleanly fixable in-loop:

```sql
create index gist_pointidx5 on gist_point_tbl using gist(p) with (buffering = invalid_value);
-- PG: ERROR:  invalid value for enum option "buffering": invalid_value
--     DETAIL:  Valid values are "on", "off", and "auto".
-- goopg (before fix): silently succeeds — the index gets created
create index gist_pointidx5 on gist_point_tbl using gist(p) with (fillfactor=9);
-- PG: ERROR:  value 9 out of bounds for option "fillfactor" (this is what the test is really probing)
-- goopg (before fix): ERROR: relation "gist_pointidx5" already exists  -- wrong error, masks the real check
```

Root cause: `execCreateIndex` (`internal/executor/operators_ddl.go`) already
range-validated `fillfactor` and `gin_pending_list_limit`/`pages_per_range`
(DU-002 slices 221/222) but had **no check at all** for the GiST `buffering`
enum storage parameter (PG's `RELOPT_TYPE_ENUM`, `reloptions.c`
`parse_one_reloption`), and the parser's `WITH (...)` loop
(`internal/parser/ddl.go`) didn't even recognize `buffering` as a known key —
it fell through the catch-all `p.advance()` and was silently discarded. The
invalid-valued `CREATE INDEX gist_pointidx5 ... WITH (buffering =
invalid_value)` therefore succeeded and created the index under that name;
the test's very next statement — a *different* invalid-option probe reusing
the same index name to exercise the fillfactor bounds check — then failed
with `42P07 relation "gist_pointidx5" already exists` instead of PG's real
`22023` fillfactor-bounds error, corrupting the rest of that diff hunk.

Fix (three sites, matching the existing `fillfactor`/`gin_pending_list_limit`
pattern exactly):

- `internal/parser/ast.go`: added `CreateIndexStmt.Buffering string` (raw
  lowercased value, empty = unset).
- `internal/parser/ddl.go`: added a `buffering` arm to the `WITH (...)`
  parse loop, capturing the raw identifier value (including invalid ones —
  validation happens at execution time, matching PG's semantic-not-syntax
  error).
- `internal/executor/operators_ddl.go` `execCreateIndex`: added an enum
  check immediately after the existing fillfactor-bounds check — `s.Buffering
  != "" && s.Buffering not in {on, off, auto}` raises PG's exact `22023`
  message and detail text.

goopg still doesn't build a buffering-aware GiST tree (out of scope — no
physical GiST index build exists per root cause 1 above), but the storage
parameter is now validated PG-faithfully at CREATE INDEX time regardless,
matching how `fillfactor`/`gin_pending_list_limit`/`pages_per_range` are
already handled as catalog/dump-only values with real bounds checking.

Verified live: diff line count dropped 433→412; the first hunk (buffering/
fillfactor probes) is now byte-identical to the PG oracle. Landed with
`TestCreateIndexBufferingEnumValidation`
(`internal/executor/operators_fillfactor_reloptions_test.go`) — pins the
22023 rejection with PG's exact message, and that a rejected attempt does
NOT create the index (so a following same-named CREATE INDEX with a valid
option succeeds rather than hitting the masking 42P07).

## Resume points

- **GiST physical-index plan integration**: `internal/optimizer` index
  selection path. Parallels the GIN (M0134-0126) and SPGiST (M0134-0111)
  resume points — worth unifying into one milestone; all three catalog-only
  index AMs share the identical gap shape (no `IndexPath`/`BitmapPath`
  ever considers them).
- **Geometry type system**: see `docs/design/m0134-0125-geometry-sizing.md`
  for the full resume-point breakdown (typed literals, `circle()` and
  sibling constructor functions).
- **`<->` KNN operator**: extend the geometry operator lexer (currently a
  fixed 2/3-char whitelist per `create_index_spgist.sql`'s M0134-0111
  sizing note — `docs/design/m0134-0111-*` if filed, else the M0134-0125
  doc) to recognize PG's general graphic-operator-char grammar, then wire a
  KNN `ORDER BY` plan path (PG's `amcanorderbyop` — pathkeys derived from an
  index's distance ops, `create_index.c`/`indxpath.c`
  `match_pathkeys_to_index`). Depends on GiST physical-index integration to
  have an index to order by.

## Ledger

`.ralph/deferral_ledger.md`, row dated 2026-08-24, task-id `M0134-0127`.
