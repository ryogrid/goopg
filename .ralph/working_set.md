Task: M0123-S4 sub-slice 40 — broader date input forms (infinity, BC years)

Files:
  - internal/pgnodes/datum.go: stripEraSuffix helper, infinity sentinel constants
    (dateInfinity/NegInfinity, tsInfinity/NegInfinity), extended parseDateDays/
    parseTimestamptzMicros/parseTimestampMicros for infinity+BC, extended
    formatDate/formatTimestamp/formatTimestamptzUTC for infinity+BC output
  - internal/pgnodes/bc_infinity_test.go: new — 12 PG18.3 goldens (4 date + 4
    timestamp + 4 timestamptz), codec/rebuild round-trips, format sentinel,
    edge-case, no-regression checks (all PASS)
  - internal/testport/oracle_pgnodes_adbin_test.go: 12 new oracle cases (now 130)
    all byte-identical vs live PG18.3
  - .ralph/fix_plan.md: sub-slice 40 marked LANDED

Key symbols:
  - stripEraSuffix (BC/AD → astronomical year conversion)
  - dateInfinity/dateNegInfinity (int32 sentinels)
  - tsInfinity/tsNegInfinity (int64 sentinels)

Hypothesis/Findings:
  - PG stores infinity as INT32_MAX/INT64_MAX (hardware limits); -infinity as
    INT32_MIN/INT64_MIN — verified byte-for-byte against live PG18.3
  - BC year conversion: 1 BC → year 0 (astronomical), 2 BC → year −1
    (= 1 - original_year)
  - Timestamptz BC requires explicit offset for determinism (same policy as
    non-BC forms); no-offset BC forms still degrade to SQL text
  - `now`/`today`/`tomorrow`/`yesterday` degrade (require runtime context)

Next step: M0123-S4 IS CLOSED. All M0123 items complete. Next priority per
banner: M-NIGHTLY backlog (since M0124/M0125/M0127/M0128 are all closed).

Gates run:
  - go test ./internal/pgnodes/ PASS (all 36 new + all existing)
  - go vet ./internal/pgnodes/ PASS
  - go build ./... PASS
  - go test -run TestOraclePgnodesAdbinBytesMatchPG ./internal/testport/ PASS
    (130 cases, all byte-identical vs live PG18.3)

In-flight: none
