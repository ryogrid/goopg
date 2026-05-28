# Symbol-Footprint Classifier — Design Skeleton

Date: 2026-05-27

A static-analysis tool that, per contrib extension, (1) extracts the backend API
surface its C sources touch, (2) classifies it **leaf** vs **non-leaf**, and
(3) for leaf extensions emits a **cxgo-feasibility verdict** (incl. macro/longjmp
dependency detection), then recommends a porting strategy. This turns the tiering
in [02](02-scope-mechanisms-and-tiers.md) from estimate into measurement and
produces the actionable porting queue.

This is a **design skeleton on paper**; the tool is not implemented here.

## Placement and shape

A read-only Go CLI under `cmd/gen-contrib-footprint/` (working name), following
the existing generators (`cmd/gen-oracle-port-status`,
`cmd/gen-regress-coverage`): `flag.String` for `--repo-root` / `--out-csv` /
`--out-md`, a package-local `fail(where string, err error)` helper
(stderr + `os.Exit(1)`), `encoding/csv` + piped-markdown emission, and a thin
`main.go` driver over a sibling testable package (mirroring how
`internal/testport/framework` backs those generators). It analyzes the **48
installable extensions** in `postgres/contrib/` — the immediate subdirectories
that contain at least one `*.control` file (59 subdirectories total; 11 carry no
control file: `auth_delay`, `auto_explain`, `basebackup_to_shell`,
`basic_archive`, `oid2name`, `passwordcheck`, `pg_overexplain`, `sepgsql`,
`start-scripts`, `test_decoding`, `vacuumlo`).

## Algorithm

### Step 0 — Discover extensions
Enumerate immediate subdirectories of `postgres/contrib/` containing at least one
`*.control` file (the authoritative installable-extension marker). Per extension
record: name, `.c` file list (`*.c` and `src/*.c`), total C LOC.

### Step 1 — Extract candidate identifiers per extension
Tokenize C identifiers (`[A-Za-z_][A-Za-z0-9_]*`) across the extension's `.c`
files, after stripping comments and string literals. This deliberately captures
**macro invocations**, which dominate the real surface: `citext`'s backend touch
is almost entirely macros (`PG_GETARG_TEXT_PP` ×30, `PG_FREE_IF_COPY` ×26,
`VARDATA`/`VARSIZE` ×10 each) plus a few bare backend calls (`str_tolower`,
`varstr_cmp`, `hash_any` — all leaf-class collation/varlena/hash helpers). A
function-call-only scan would miss the macros entirely. De-duplicate per extension; keep
occurrence counts (needed for blocker density and bucket weighting).

### Step 2 — Resolve identifiers (curated dictionary + GNU GLOBAL)
Two complementary mechanisms:

- **(a) Curated backend-API dictionary (primary signal).** A hand-maintained
  table mapping backend symbols/macros → buckets, because raw GLOBAL knows
  *where* a symbol lives but not its *intent* (`global -x palloc` resolves to two
  definitions; `global -x PG_GETARG_TEXT_PP` resolves to a `#define` in
  `fmgr.h`). The dictionary is defined by **~15 prefix families** (e.g. `SPI_`,
  `SearchSysCache`) and *materialized* against this tree with `global -c <prefix>`
  (e.g. `global -c SPI_` enumerates the full `SPI_*` family), so it cannot drift
  from the actual tags.
- **(b) GLOBAL definition lookup (classification of unknowns).** For an
  identifier not in the dictionary and not defined within the extension's own
  files, run `global -x <ident>` and bucket by where it is defined:
  - `#define` under `src/include/**` → backend macro; bucket by expansion family.
  - definition under `src/backend/**` → bucket by subsystem path
    (`src/backend/executor/spi.c` → spi; `utils/cache/syscache.c` → catalog;
    `utils/mmgr/**` → memory).
  - no definition, or under `src/common/**` / `src/port/**` / libc → neutral
    (not a backend-coupling signal).

  Subsystem-path→bucket is robust because PG's backend tree is cleanly
  partitioned by directory, making (b) self-maintaining for symbols the
  dictionary misses.

### Step 3 — Bucket the symbols

| Bucket | Leaf? | Detection (dictionary prefix / GLOBAL path) |
|---|---|---|
| `memory` | leaf | `palloc`/`palloc0`/`pfree`/`repalloc`/`MemoryContextAlloc*`; `src/backend/utils/mmgr/**` |
| `varlena` | leaf | `cstring_to_text`/`text_to_cstring`/`TextDatumGetCString`/`PG_DETOAST_DATUM*`/`pg_detoast_datum*`/`VARDATA*`/`VARSIZE*`/`DatumGetTextPP` |
| `error` | leaf | `ereport`/`elog`/`errmsg`/`errcode`/`errdetail` (also a cxgo varargs signal — Step 5) |
| `fmgr-glue` | leaf (boundary) | `PG_GETARG_*`/`PG_RETURN_*`/`PG_FUNCTION_INFO_V1`/`PG_FUNCTION_ARGS`/`PG_FREE_IF_COPY`/`PG_GET_COLLATION` — present in every extension, so **not** disqualifying |
| `fmgr-graph` | **non-leaf** | `DirectFunctionCall[0-9]`/`OidFunctionCall[0-9]`/`fmgr_info`/`FunctionCall[0-9]`/`InputFunctionCall`/`get_call_result_type` |
| `spi` | **non-leaf** | `SPI_*` |
| `catalog` | **non-leaf** | `SearchSysCache*`/`GetSysCache*`/`table_open`/`systable_*`/cache+catalog lookups under `utils/cache/**`, `catalog/**` |
| `guc-bgw-hooks` | **non-leaf** | `DefineCustom*Variable`/`RegisterBackgroundWorker`/`_PG_init`/`_PG_fini`/`*_hook` assignments/`ShmemInitStruct`/`RequestAddinShmemSpace` |
| `neutral` | n/a | libc, `src/common/**`, `src/port/**`, extension-local symbols |

### Step 4 — LEAF / NON-LEAF decision (hard gate)

> **NON-LEAF** if the extension touches **any** symbol in `fmgr-graph`, `spi`,
> `catalog`, or `guc-bgw-hooks`. **LEAF** otherwise — its entire backend
> footprint is within `{memory, varlena, error, fmgr-glue, neutral}`.

A hard gate (not a count threshold) is correct because these buckets puncture the
shim boundary: one `SearchSysCache1` drags in the relcache/syscache subsystem;
one `DirectFunctionCall2` drags in the fmgr dispatch graph plus the target's own
footprint. Validation against real sources:

| Extension | Decisive symbols | Result |
|---|---|---|
| `citext` | `pfree` + macros + `str_tolower` | **LEAF** |
| `fuzzystrmatch` | memory/varlena/error + `errmsg` | **LEAF** |
| `hstore` | `DirectFunctionCall2`×8, `DirectFunctionCall3`×1, `fmgr_info`×2, `get_call_result_type` → `fmgr-graph` | **NON-LEAF** |
| `postgres_fdw` | `SearchSysCache*` (+ `PG_TRY`×19) | **NON-LEAF** |
| `pg_stat_statements` | `DefineCustomIntVariable` + `_PG_init` | **NON-LEAF** |

> Correction to an earlier assumption: `hstore`'s non-leaf basis is the **fmgr
> graph**, not SPI — `grep SPI_ hstore/*.c` is empty. The classifier reports the
> actual basis (`leaf_gate_reason`) rather than a guessed one.

`backend_symbol_count` and `distinct_buckets` are emitted as **informational**
(non-gating) signals so reviewers can tell "barely leaf" from "deeply leaf."

### Step 5 — cxgo-feasibility verdict (LEAF only)
Non-leaf extensions defer regardless of cxgo, so the verdict is computed only for
leaves. Each blocker is a line/regex scan with an occurrence count:

| Blocker | Signal | Severity |
|---|---|---|
| `setjmp-longjmp` | `PG_TRY`/`PG_CATCH`/`PG_END_TRY`/`sigsetjmp`/`setjmp` | **blocked** (cxgo cannot model non-local control flow) |
| `varargs` | `errmsg(`/`errdetail(`/`errhint(`/other variadic backend calls | **needs-patch** (shim exposes fixed-arity wrappers) |
| `func-pointers` | `(*ident)(` declarators, callback typedefs, function names assigned to struct fields | **needs-patch** |
| `bitfields` | `: <int>;` inside a `struct` body | **needs-patch** |
| `gcc-extensions` | `__attribute__`/`__builtin_`/statement-exprs `({`/`asm` | **needs-patch** (or **blocked** in a hot path) |
| `macro-density` | local `#define` count + macro-token : LOC ratio over a tunable threshold (start ~0.15) | **needs-patch** (advisory) |

Verdict reduction: any `blocked` → **`blocked`**; else any `needs-patch` →
**`needs-patch`**; else **`clean`**. Validation: `fuzzystrmatch` has no
`PG_TRY`/setjmp/func-ptr/bitfield/`__attribute__`, only `errmsg(` ×3 →
**`needs-patch`** (varargs only); `citext` shows no setjmp/func-ptr/bitfield.

## Transitive closure: bounded, by design

**Direct scan + full union over the extension's own translation units; treat
backend symbols as opaque leaves (external depth 0).** Justification:

- Direct identifiers are exactly the coupling the porter must satisfy at the
  boundary — the load-bearing signal.
- Full `global -rx` closure is noisy, expensive, and the **wrong question**:
  `global -rx palloc` returns ~2317 referers; expanding through a backend
  function measures PG's internal complexity, not the extension's porting cost.
  Under the shim model, backend symbols are **opaque leaves** — the porter shims
  `text_to_cstring` at the boundary and never transpiles its internal call graph.
- One "hop" that *does* matter is into the extension's **own** helpers; unioning
  all identifiers across the extension's `.c` files achieves it without graph
  walking. Cost: O(files × identifiers) + at most one cached `global -x` per
  distinct unknown — versus the unbounded `global -rx` fan-out we reject.

## Output schema

### CSV (`docs/test-port/contrib-footprint.csv`, mirroring the existing flat style)
One row per extension:

```
extension,c_files,loc,backend_symbol_count,distinct_buckets,symbol_buckets,classification,leaf_gate_reason,cxgo_blockers,cxgo_verdict,recommended_strategy,rationale
```

- `symbol_buckets` — `bucket:count` pairs, desc, e.g. `varlena:42;memory:10;error:3;fmgr-glue:90`.
- `leaf_gate_reason` — for non-leaf, triggering bucket + sample symbol
  (`fmgr-graph:DirectFunctionCall2`); empty for leaf.
- `cxgo_blockers` — `blocker:count` pairs (`varargs:3`); empty/`n/a`.
- `cxgo_verdict` — `clean` | `needs-patch` | `blocked` | `n/a` (non-leaf).
- `recommended_strategy` — `native-go-port` | `cxgo+shim` | `defer`.
- `rationale` — short generated sentence.

### Companion markdown (`docs/test-port/contrib-footprint.md`)
Same generator emits, in `gen-regress-coverage` style: a provenance + timestamp
line, a summary count line
(`N extensions: X leaf / Y non-leaf; A native-go-port / B cxgo+shim / C defer`),
a legend defining each strategy/verdict, a pipe table
(`| extension | loc | classification | buckets | cxgo_verdict | strategy | rationale |`),
and a second table listing **LEAF only**, sorted by `cxgo_verdict` then LOC — the
actionable porting queue.

## Known limitations

**False negatives (looks LEAF but isn't):** a macro hiding a non-leaf call;
struct-layout/ABI coupling invisible to a symbol scan; dynamic dispatch
(SPI executing a built query string) where the symbol still appears but intent is
under-weighted.

**False positives (looks NON-LEAF but is portable):** a `DirectFunctionCall2`
whose target is itself trivial (could be inlined in a native port); `errmsg`
counted as both a leaf `error` member and a varargs blocker (correct but maybe
trivially shimmable).

**Tokenizer noise:** identifiers in comments/strings inflate counts — mitigated
by stripping comments/strings before tokenizing; conditional compilation
(`#if`) may include platform-specific symbols (minor over-count, acceptable).

**Human review (mitigation):** the CSV is a **triage** artifact, not a verdict of
record. The `leaf_gate_reason`, `symbol_buckets`, and `cxgo_blockers` columns
exist so a reviewer can audit *why* in seconds. Review concentrates on the ~5–15
LEAF rows and any `needs-patch`; non-leaf rows are auto-deferred and need no
review.

## Recommended-strategy decision matrix

| classification | cxgo_verdict | footprint shape | strategy | reasoning |
|---|---|---|---|---|
| leaf | clean | small (`< --native-loc-max`, ≤2 buckets) | **native-go-port** | hand-port is lower-risk than a transpile+shim pipeline (e.g. `citext`, 412 LOC) |
| leaf | clean | large / algorithmically dense | **cxgo+shim** | reuse upstream algorithm; clean to transpile, leaf at boundary |
| leaf | needs-patch | any | **cxgo+shim** | varargs/local-macros/func-ptrs are mechanically patchable (e.g. `fuzzystrmatch`) |
| leaf | blocked | any | **defer** (or **native-go-port** if small) | `PG_TRY`/setjmp defeats cxgo; a small clean algorithm is better rewritten by hand |
| non-leaf | n/a | — | **defer** | needs SPI/syscache/fmgr-graph/GUC-BGW-hook infra goopg v0 lacks (`hstore`, `postgres_fdw`, `pg_stat_statements`) |

Tunable flags keep the boundaries auditable rather than magic: `--native-loc-max`
(default 500), `--macro-density-max` (default 0.15).

## Reference implementation fixtures
- Templates: `cmd/gen-regress-coverage/main.go` (inline CSV + markdown +
  `fail()`), `cmd/gen-oracle-port-status/main.go` (thin driver + framework split).
- LEAF validation: `postgres/contrib/citext/citext.c`,
  `postgres/contrib/fuzzystrmatch/`.
- NON-LEAF validation: `postgres/contrib/hstore/hstore_op.c` (fmgr-graph basis;
  `DirectFunctionCall` also appears in `hstore_io.c`, `hstore_gin.c`),
  `postgres/contrib/postgres_fdw/`, `postgres/contrib/pg_stat_statements/`.
