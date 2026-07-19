(idle — nothing in flight)

Last loop (#37): M0123-S4 sub-slice 23 — IMPLICIT numeric column length coercion
(`coerce_type_typmod`). LANDED + committed. Closes the COMMON case of sub-slice 22's
degrade: a `numeric(p,s)` column DEFAULT whose stored value lacks that typmod
(`numeric(10,2) DEFAULT 5.5`/`0`/`5000000000`/`5.5::numeric(8,1)`) now wraps in the
funcformat-**2** sibling of numeric(numeric,int4)=1703 with the COLUMN typmod Const,
byte-identical to PG18.3. A live 6-DEFAULT probe corrected the sub-slice-22 note:
RelabelType is ONLY the bare-`numeric`-column case, not every mismatch.

Files: internal/pgnodes/resolver_expr.go (ResolveForColumnTypmod rewritten around
coerce_type_typmod + new numericNodeTypmod + wrapNumericLengthCoercion);
rebuild.go (isImplicitNumericLengthCoercion → joins the implicit-cast unwrap block);
numeric_lencoerce_test.go (NEW: 6 goldens + no-wrap/degrade guards);
internal/testport/oracle_pgnodes_adbin_test.go (+5, now 57). NO executor change
(writer already threads the column typmod). Design 0123-0005 §"Sub-slice 23" +
README index + ledger 2026-07-20.

Gates GREEN: full pgnodes pkg; adbin oracle 57/57 byte-identical vs LIVE PG18.3
(1.58s); executor attrdef siblings; go build ./..., go vet, gofmt clean; pgbench
smoke via pre-commit.

Next (M0123-S4 REMAINING): (1) RelabelType IR node (ir.go codec Out/Read + rebuild)
so a bare-`numeric` column with a typmod'd cast default (`col numeric DEFAULT
5.5::numeric(8,1)`) canonicalizes — resume ResolveForColumnTypmod's
`targetTypmod < 0 && exprTypmod >= 0` branch (currently returns nil,false → degrade);
(2) float4-common (no float8) CASE result mix — int/numeric→float4 arms + outer
float8(float4) column cast (selectCaseCommonType/coerceCaseResult); (3) date-time-
family CASE coercion — needs a `date` OID-1082 datum (datum.go) + `::date`/
`::timestamptz` string-literal fold; (4) other length types (varchar(N)=CoerceViaIO,
timestamp(N), bit(N)); (5) operator-driven view-qual coercion.
