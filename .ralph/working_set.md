Task: M0123-S4 sub-slice 39 — timetz(N) length coercion

Files:
  - internal/pgnodes/datum.go: OidTimeTZ=1266, NewTimeTZConst, decodeTimeTZDatum,
    parseTimeTZMicros, formatTimeTZ, caseTypeMeta entry
  - internal/pgnodes/resolver_expr.go: resolveStringLiteral OidTimeTZ case,
    wrapTimeTZLengthCoercion (funcid 1969), ResolveForColumnTypmod case,
    ColumnTypmod "timetz"/"time with time zone" entries
  - internal/pgnodes/rebuild.go: isImplicitTimeTZLengthCoercion, rebuildConst
    OidTimeTZ case, implicit-coercion unwrap chain
  - internal/pgnodes/timetz_lencoerce_test.go: new — structure + round-trip +
    no-wrap + ColumnTypmod tests (all PASS)
  - internal/testport/oracle_pgnodes_adbin_test.go: 8 new timetz oracle cases

Key symbols:
  - OidTimeTZ=1266, NewTimeTZConst, parseTimeTZMicros, formatTimeTZ,
    decodeTimeTZDatum
  - wrapTimeTZLengthCoercion (funcid 1969), isImplicitTimeTZLengthCoercion

Hypothesis/Findings:
  - PG stores timetz zone offset with opposite sign: east of UTC is NEGATIVE
    (verified via oracle test byte comparison)
  - timetz is by-reference (constlen 12, constbyval false)
  - All 118 oracle adbin cases PASS byte-for-byte against live PG18.3

Next step:
  M0123-S4 sub-slice 40: broader date input forms (infinity, BC years,
  DateStyle-dependent) — the last REMAINING item

Gates run:
  - go test ./internal/pgnodes/ PASS
  - go vet ./internal/pgnodes/ PASS
  - go build ./... PASS
  - go test -run TestOraclePgnodesAdbinBytesMatchPG ./internal/testport/ PASS (118 cases)
  - make ralph-state-guard PASS (with auto-repair)

In-flight: none
