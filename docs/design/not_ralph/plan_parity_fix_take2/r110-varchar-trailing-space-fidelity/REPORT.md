# R110 result: varchar preserves in-range trailing whitespace

R110 changes only assignment/input varchar typmod coercion. Goopg now retains
in-range trailing spaces, while still dropping only excess trailing spaces to
fit a varchar typmod. Explicit cast truncation was untouched.

## PG18.3 witness

A fresh private PG18.3 cluster on port 65444 produced these results for
`v varchar(3)`: assignment `x ` returned `x ` with octet length 2;
assignment `abc   ` returned `abc` with length 3; assignment `abcd` failed
SQLSTATE 22001; and `'abcdef'::varchar(3)` returned `abc` with length 3.
The multibyte explicit cast `'é  '::varchar(2)` returned `é ` with octet
length 3. The cluster was stopped after collection.

## Implementation and verification

`coerceTextLikeDatum` in `internal/executor/codec.go` now calls
`TrimRight(s, " ")` only after the original varchar value exceeds its rune
length typmod. The existing explicit-cast truncation in `expr.go` is unchanged.
`TestEncodeValuePGVarcharCoercesIntAndTrimsOnlyExcessSpaces` covers preserved
in-range and discarded excess spaces. The protocol-level
`TestPGHeapEncodingPreservesTextLikeInsertCoercions` now asserts that an
in-range `varchar(6)` value `c     ` survives INSERT and SELECT.

`go test ./internal/executor` and `go test ./internal/postmaster` pass;
`git diff --check` passes. A fresh private Goopg cluster `/tmp/r110-goopg-q96`
was loaded from R107's original four TSVs. Its ordered full `store` raw COPY
SHA-256 was `1e9bcb45cc785950df369793c32397fd2e2f99bb7fdb90852321d90bd91a9d86`;
the expected raw mismatch remains because bpchar/numeric presentation differs.
Its type-normalized SHA-256 was
`040c1a49656da7c6052629f42d0bf0858220da6df38f8a4a782bbb58d4e51696`, exactly
matching PG's R109 witness. The private service was stopped.

This repairs data fidelity, not a cost formula or join election. R107's
common-data measurement must be rerun under a separate scope before R108.
