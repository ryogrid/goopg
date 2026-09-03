# TAP Inventory: `pass + failed > port` Discrepancy

## Observation

In `docs/test-port/postgres-oracle-target-inventory.md`, every `*-tap` suite
shows `pass + failed > port`:

| suite | total | pass | failed | port | pass+failed | port |
|-------|------:|-----:|-------:|-----:|------------:|-----:|
| recovery-tap | 51 | 8 | 43 | 8 | **51** | 8 |
| subscription-tap | 40 | 7 | 33 | 7 | **40** | 7 |
| client-tools-tap | 94 | 39 | 3 | 39 | **42** | 39 |

The column note in the markdown (lines 11–14) explains the `port`/`pass`
double-count for TAP suites (`port` + `pass_required=yes` counted under both
columns), but it does **not** explain why `failed` inflates beyond `port`.

## Investigation

### Root cause: commit `8597fbdee` (2026-08-14)

The inventory CSV was consolidated from three legacy CSVs in commit
`e57622021` (2026-08-14). In that initial consolidation, every non-ported
recovery-tap row (43 files) and subscription-tap row (33 files) had
`status=defer` with `deferred_to=M0094` — "Requires replication/failover/
recovery semantics not yet fully implemented in goopg v0."

**Later the same day**, commit `8597fbdee` ("test-port(reclassify): mark
replication/WAL-off/pg_amcheck defer rows as failed") reclassified **79 rows**
from `defer` to `failed`:

- 43 recovery-tap rows (replication infrastructure gap)
- 33 subscription-tap rows (logical replication gap)
- 2 pg_waldump rows (canonical WAL emission — client-tools-tap)
- 1 pg_amcheck row (index AM coverage — client-tools-tap)

The commit message rationale: *"For test-first tracking, reclassify the defer
rows whose blocker is an active in-scope gap (not a genuinely separate
milestone) to `failed`."*

### Semantic overload of `failed`

The status vocabulary (defined in `.ralph/PROMPT.md` §"Status vocabulary" and
`docs/test-port/README.md`) assigns:

| status | meaning |
|--------|---------|
| `failed` | in-scope case, **currently diverging** |
| `defer` | In scope, not yet pass-required. Promote when the prerequisite lands. |

`failed` implies the case was attempted (ported or run) and its output diverges
from PostgreSQL. But the 76 recovery-tap + subscription-tap rows were **never
ported** — no Go test exists, no `TestPort_*` function names them. Their
`deferred_to` column still reads `M0094` (a property of `defer` rows per the
README schema, though the validator doesn't enforce it). They are blocked by
missing replication infrastructure, not attempted-and-diverging.

The reclassify commit deliberately overloaded `failed` to mean "active
in-scope blocker" — a tracking distinction orthogonal to the ported/attempted
state. In the `defer` taxonomy, the before/after split was:

| original status | meaning |
|---------|---------|
| `defer` (to M0094) | deferred to a separate milestone |
| `defer` (to M0060-0005) | deferred to a genuinely separate milestone (extension framework) |

The reclassify kept `defer` for the extension-framework blocker (modules/
contrib) and moved the replication/subscription rows to `failed` to signal
"in-scope active gap, not a separate milestone."

### How the workflow allows this to persist

Four factors in the harness workflow let the anomaly go undetected:

1. **Vocabulary definition is ambiguous.** `.ralph/PROMPT.md` §"Status
   vocabulary" defines `failed` as "in-scope case, currently diverging". The
   phrase "currently diverging" could be read as "output diverges when run"
   (the intended reading) or "will diverge until the blocker is fixed" (the
   reclassify reading). No explicit rule says a TAP `failed` row must name a
   ported test function.

2. **Validator does not enforce the invariant.**
   `internal/testport/framework/status.go` (`ValidateStatusRows`) checks:
   - status vocabulary
   - `excluded` may not be must-pass
   - `defer` requires `deferred_to`
   - `port` rationale must name a `TestPort_*`/`TestE2E_*` func
   - no duplicate ids

   It does **not** check:
   - that a `failed` row in a `*-tap` suite has `deferred_to == "-"` (the
     README says `deferred_to` is "for `defer` rows, `-` otherwise")
   - that `pass + failed <= total` for TAP suites (the `failed` bucket
     should represent tests that were attempted, not missing infrastructure)
   - that `failed` tap rows reference a ported test function

3. **Generator is purely aggregative.** `cmd/gen-oracle-inventory/main.go`
   counts `failed` status rows verbatim into the `failed` column. It has no
   suite-specific sanity checks. The `pass` column for TAP rows correctly
   double-counts `port`+`pass_required=yes`, but the `failed` column is
   blindly summed.

4. **Markdown rendering note is incomplete.** The note at lines 11–14 of
   `postgres-oracle-target-inventory.md` explains the `port`/`pass`
   double-count but does not mention that `failed` for TAP suites includes
   unported, never-attempted rows. A reader reasonably infers that `failed`
   means "ported but failing output" — which is false for recovery-tap and
   subscription-tap.

### Concrete impact

| consequence | detail |
|-------------|--------|
| Misleading `pass_rate` | 76 never-ported tests inflate the `failed` denominator, pushing `pass_rate` artificially low: recovery-tap 15.7%, subscription-tap 17.5%. |
| Schema violation | 76 rows carry `deferred_to=M0094` while `status=failed`; the README specifies `deferred_to` is "milestone/task reference for `defer` rows, `-` otherwise". |
| Governance confusion | A human reading the table sees `failed=43` and concludes 43 tests were attempted and failed, when in fact 43 were never attempted. |

## Correction

### Option A (recommended): Restore `defer` for blocked-but-unported rows

Revert the reclassification for the 76 recovery-tap/subscription-tap rows:
`status` → `defer`, keeping `deferred_to=M0094`. This restores the original
classification from the consolidation commit `e57622021`.

After this change:
- `pass = port` (all ported TAP tests pass)
- `failed = 0` for recovery-tap and subscription-tap (no ported test fails)
- `pass + failed = port` for these two suites
- client-tools-tap: 3 `failed` rows remain (pg_waldump, pg_amcheck — these
  are genuinely ported tests whose execution is deferred by a runtime
  condition, so `failed` is arguably correct there)

**Precedent:** The `defer` → `failed` reclassify was motivated by tracking
("active in-scope gap"), not by test outcome. The `defer` status already
carries the `deferred_to` column to track which blocker applies. Switching
back to `defer` does not lose any information — the blocker reference
(`M0094`) is already in the `deferred_to` column.

### Option B: Tighten the validator

Add checks to `ValidateStatusRows`:
- For `kind == "tap"` and `status == "failed"`, require `deferred_to == "-"`
  (a `failed` TAP row must not carry a deferred-to reference — that is a
  `defer` property).
- Optionally, for `*-tap` suites, warn if `pass + failed > port` (the
  `failed` column exceeds the number of ported tests).

### Option C: Update the markdown note

Expand the note in `postgres-oracle-target-inventory.md` to explain that
`failed` for TAP suites may also include in-scope, blocked-but-unported
cases (not just ported-and-diverging tests). This is the minimal fix.

### Recommended combined approach

1. **Reclassify the 76 rows back to `defer`** (Option A).
2. **Add a validator check** (Option B) that `kind == "tap"` and
   `status == "failed"` implies `deferred_to == "-"` — preventing future
   overload.
3. **Update the markdown note** (Option C) to document the invariant:
   `pass + failed <= port` for `*-tap` suites, with any deviation explicitly
   annotated.

These changes require no modification to the TAP test harness itself — only
the inventory CSV, the validator, and the rendering note.