(idle — nothing in flight)

Completed this loop: **M0119-0006 65th slice** — `octet_length()`/`bit_length()`
on a `bpchar` answer from the declared width, not the trimmed heap image
(ledger row 1314 resolved). `declaredBpcharTypmod` + `PadBpchar` for
octet_length, trimmed-×-8 for bit_length (implicit bpchar→text cast trims),
`42883` guards. 19 oracle-pinned cells; `expr.go` +
`bpchar_declared_width_test.go` + design
`docs/design/0119-0006-bpchar-octet-bit-length.md` + README row + ledger row
1314 + fix_plan 65th-slice entry all committed.

**Known pre-existing blocker for the next loop:** the untracked
`internal/executor/reg_identifier.go` + `reg_identifier_test.go` (and the
modified `codec.go`/`copy_binary.go`/`copy_binary_oid_test.go`/
`operators_storage.go`) reg_identifier WIP from a prior loop has a genuinely
failing test — `TestRegIdentifierInputResolvesRegtypeName` expects `regtypein`
to raise 42704 on `no_such_type`, but `catalog.TypeNameToOID`'s
`default: return OIDText` fallback resolves it to OID 25 (no 0 sentinel), so
the miss-path never fires. This makes the pre-commit `units` gate fail (only
failure in the whole run). Fix requires changing the `TypeNameToOID` contract
or adding a found-flag lookup — a different subsystem, not this slice's scope.
