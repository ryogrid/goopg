# Scope: contrib Tiers and the Framework Mechanisms Required

Date: 2026-05-27

The cxgo+shim pipeline ([01](01-approach-cxgo-sdk-shim-marshaling.md)) only
implements the **function bodies** of leaf extensions. Two things still bound
what is actually achievable: (1) the **tier** an extension falls into, and
(2) the **framework mechanisms** that must exist regardless of porting method.

## contrib tiering

The differentiator is *what backend surface each extension's C symbols require*.
The classifier ([03](03-symbol-footprint-classifier.md)) makes this exact and
measured; the tiers below are the qualitative map.

### Tier 1 — Pure-scalar leaf (highest feasibility)
Self-contained string/number/bytes computation; no new type, operator, index,
or hook.
- `intagg` — **pure SQL** (no C); runs unmodified once `CREATE EXTENSION` works.
- `fuzzystrmatch` — `levenshtein`/`soundex`/`metaphone`/`dmetaphone`; scalar.
- `uuid-ossp` — UUID generators (goopg already has `gen_random_uuid`).
- `pgcrypto` core — `digest`/`hmac`/`crypt`/`gen_salt` over bytea (Go stdlib
  covers most; `pgp_*` excluded as large).

Needs only the install runtime + binding-keyed dispatch (+ trivial scalar
marshaling). This is the cxgo+shim / native-port sweet spot.

### Tier 2 — Type + operator (+ optional index)
- `citext`, `hstore`, `ltree`, `cube`, `seg`, `isn`, `intarray`, `pg_trgm`.
- Type, operator, and equality/btree parts are implementable; the GiST/GIN
  operator-class parts are blocked until those AMs exist. Realistic outcome:
  **partial (no index acceleration)**. Note that several of these are **non-leaf**
  at the C level (e.g. `hstore` uses the fmgr graph) and thus defer under the
  classifier even though their *concept* is Tier 2.

### Tier 3 — Introspection / hooks / AM
- `pg_buffercache`, `pg_freespacemap`, `pg_visibility`, `pgstattuple`,
  `pg_prewarm` (read goopg-owned subsystems — feasible natively),
  `pg_stat_statements` (needs query normalization + a hook), `auto_explain` /
  `passwordcheck` / `auth_delay` (need a hook framework), `btree_gin` /
  `btree_gist` (need GIN/GiST AMs).
- `pageinspect` / `pg_walinspect` / `amcheck` are byte-format-specific; goopg's
  physical formats differ from PG, so unmodified semantic compatibility is
  effectively impossible.

### Tier 4 — Impractical unmodified
- `postgres_fdw` / `dblink` (libpq client + FDW framework), `file_fdw` (FDW
  framework), `test_decoding` (logical-decoding output-plugin API), `bloom`
  (custom AM), `xml2` (libxml), `unaccent` / `dict_*` (text-search dictionary
  framework).

## Framework mechanisms required regardless of porting method

These are **orthogonal to cxgo vs. native-port**. They are the cost of extension
support itself, and most are prerequisites for Tier 1, the easiest tier.

1. **`CREATE EXTENSION` install runtime** (gates everything)
   - Execute `CREATE/ALTER/DROP EXTENSION` (parser stubs exist; no execution).
   - Read unmodified `<name>.control` + `<name>--X.Y.sql` from a SHAREDIR; run the
     install script in a transaction; record into `pg_extension` (seeded empty at
     OID 3079) and `pg_depend`.
   - Resolve `MODULE_PATHNAME` to a **native provider**, not a `.so` path.

2. **Binding-keyed function dispatch (fmgr substitution)** — the keystone.
   - Replace the name-`switch` in `internal/executor/expr.go` with a registry
     keyed by the routine's binding (builtin handler **or** `(module, symbol)`),
     so builtin / user-defined / extension functions share one path.
   - Stop rejecting `LANGUAGE C`; instead bind `(module, symbol)` → a registered
     Go function (the shim output from [01](01-approach-cxgo-sdk-shim-marshaling.md)).

3. **Extensible type system** — `pg_type` write path + varlena/opaque (bytea-backed)
   type representation + I/O functions registered as shims. Unlocks Tier 2 types
   even before index support.

4. **Operator registration** — `CREATE OPERATOR` → `pg_operator` write path +
   planner/executor operator resolution beyond builtins. Needed for Tier 2
   operators.

5. **Pluggable index AM + opclass registration** (large) — an abstract AM
   interface + **GiST/GIN** implementations + `CREATE OPERATOR CLASS` /
   `pg_amop` / `pg_amproc` write paths. Gates Tier 2 index acceleration and
   `btree_gin`/`btree_gist`/`bloom`.

6. **Hook framework** — Go-native attach points (post-parse, planner, executor
   start/run/end, `ProcessUtility`, `ClientAuthentication`) for Tier 3
   observation/policy extensions.

7. **FDW framework + libpq-compatible client** — for Tier 4 federation.

8. **Conformance harness** — run each contrib's own `sql/` + `expected/`
   regression files unmodified against goopg as the acceptance gate. This ties
   directly into the currently-deferred D-006 (`src/test/modules`) and D-007
   (`contrib`) suites in `docs/test-port/`.

## The key separation

```
                 cxgo+shim / native-port             ←  per-extension, leaf only
                 (function implementation slice)         [chapter 01]
─────────────────────────────────────────────────────────────────────────────
                 install runtime + binding-keyed       ←  shared, one-time,
                 dispatch + types/operators/AM/             unavoidable
                 hooks/FDW (framework slice)               [this chapter]
```

A correct mental model: **the porting method (cxgo vs. native) is a small,
per-leaf-extension decision; the framework is the large, shared investment.**
Choosing cxgo does not reduce the framework cost, and the framework is needed
even for the trivial pure-SQL `intagg`.
