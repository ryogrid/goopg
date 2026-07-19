Task: M0123-S4 sub-slice 4b — canonical timestamptz (OID 1184) Const datums.
COMPLETE this loop (committing).

Landed: a timestamptz column DEFAULT literal now resolves to a canonical
by-value int64 Const of μs-since-2000 (constlen 8, consttype 1184) byte-for-byte
identical to PG18.3's pg_attrdef.adbin, instead of degrading to SQL text. PG
folds an "unknown" string literal to the target type at parse time
(coerce_type→timestamptz_in), so adbin is a folded Const not a cast FuncExpr.
- datum.go: NewTimestamptzConst, parseTimestamptzMicros (PG-exact integer date2j
  Julian-day math, no float), formatTimestamptzUTC (j2date inverse) for rebuild.
- resolver_expr.go: StringConst case gains expected==OidTimestamptz branch.
- rebuild.go: rebuildConst gains OidTimestamptz → StringConst UTC literal (fixed point).
DETERMINISTIC subset only (explicit offset / Z / 'epoch'); TimeZone-dependent
forms (no offset / bare date) degrade to SQL text.

Gates (GREEN): internal/pgnodes/timestamptz_test.go (4 live PG18.3 adbin goldens
+ parser math table + graceful-degradation matrix + codec/rebuild round-trip);
executor TestCanonicalAttrdefText (timestamptz-lit + no-offset cases);
initdb attrdef reload; go build ./... + vet clean; TestE2E_FailoverGoopgToPG PASS.
Design 0123-0005 §"Sub-slice 4b" + README index + ledger row.

Key symbols: parseTimestamptzMicros, NewTimestamptzConst, formatTimestamptzUTC,
date2j, j2date, rebuildConst (OidTimestamptz case).

Next step (next loop): M0123-S4 remaining — CASE/BooleanTest(IS TRUE/FALSE)/
IS DISTINCT FROM (each codec+resolver+rebuild+scalar live goldens), then the
byte-diff oracle gate. Lower: operator-driven implicit coercion in view quals
(unblocks int2/timestamptz string literals in view WHERE), int2→numeric.
Resume file: internal/pgnodes/ir.go + resolver_expr.go + datum.go.

In-flight: none. (Nightly AI-20260719-094219-* all [x] stale-verified.)
