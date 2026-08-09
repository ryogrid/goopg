Task: M0122-0008 client_encoding GUC validation (LANDED)

Files:
- internal/config/guc.go: Added CheckFn func(string) error to Variable struct;
  called in canonicalizeFrom TypeString case
- internal/config/encoding_guc.go: New — pgEncNames (42-entry encoding name table),
  pgEncAliases, cleanEncName, encodingNameToCanonical, checkClientEncoding
- internal/config/encoding_guc_test.go: New — TestEncodingTableIntegrity
- internal/config/guc_test.go: Added TestClientEncodingValidation
- internal/config/defaults.go: Registered CheckFn: checkClientEncoding for
  client_encoding GUC
- .ralph/fix_plan.md: M0122-0008 status updated

Key symbols:
- Variable.CheckFn func(string) error — optional post-canonicalisation hook
- checkClientEncoding(value) error — validates against PG 18.3 pg_enc2name_tbl
- encodingNameToCanonical(name) string — cleaned-name resolution + aliases

Hypothesis/Findings:
- client_encoding GUC already existed (ContextUserset, FlagReport) but had
  zero validation — any arbitrary string was accepted
- CheckFn is the minimal mechanism; full assign/check hooks (PG's check_assign/
  check_client_encoding) would be overengineered for one GUC
- Encoding table is duplicated in config (leaf package) to avoid importing
  catalog; the table is an immutable PG constant, guarded by
  TestEncodingTableIntegrity

Next step:
- Per priority banner: M0130 is COMPLETE, M0119 is next but M0119-0005/0006/0007
  are blocked/huge. Most tractable remaining M0122-0008 item: bootstrap encoding
  enforcement (validate --encoding flag in initdb). Channel binding still blocked
  on TLS.

Gates run:
- go build ./internal/...: OK
- go test ./internal/config/...: PASS (all tests incl. new)
- go test ./internal/server/...: PASS
- go test ./internal/catalog/...: PASS
- RALPH_PRECOMMIT_SCOPE=units: ALL PASS

In-flight: none
