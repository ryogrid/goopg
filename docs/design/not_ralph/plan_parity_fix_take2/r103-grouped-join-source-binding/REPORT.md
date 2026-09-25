# R103 result: `USING` lateral output binding blocks an end-to-end fix

R103 followed the PG18.3 witnesses recorded in R102 and changed no committed
production code. The generated-parser source discipline was exercised during
the temporary attempt (`grammar/pg_grammar.y`, `make gen-parser`); generated
files were not directly edited.

## Positive evidence

The temporary parser marker and planner source-binding map made the two
unchanged R101 forms execute on the isolated Goopg SF0.25 clone
`/tmp/r97goopg/ds025` at port 5562 with
`GOOPG_GATHER_PATHS=top` and `GOOPG_PARTIAL_AGG_PATHS=on`:

* hdem-first returned **266**; and
* store-first returned **266**.

Their EXPLAINs retained the requested explicit first-two hash-join leaves and
the final parameterized `time_dim_pkey` probe. The hdem-first plan starts with
`store_sales × household_demographics`, then joins `store`; store-first does
the inverse, followed in both cases by `Nested Loop` to `time_dim`.

## Stop condition

The full R103 controls did not pass. For an unaliased grouped
`JOIN ... USING (aid)`, PostgreSQL accepts qualified `a.aid` and `b.aid` plus
the merged unqualified `aid`, and rejects the non-USING duplicate `bid` as
42702. Goopg's temporary parent source-binding map instead failed inside the
LATERAL child with `42703: column "aid" does not exist` for both qualified
forms. Removing the parent's `usingHidden` marker did not change that error.

This identifies a second boundary: the LATERAL child output/re-resolution path
does not preserve the source-to-merged-column mapping of a synthetic grouped
JOIN. Shipping the partial map would make Q96 work but leave PostgreSQL
`USING` semantics incorrect. R103 therefore applied its explicit stop rule;
all temporary parser, analyzer, planner, generated-file, and test changes
were reverted. Both temporary services were stopped.

## Conclusion

No production change is authorized by R103. A successor must carry a durable
grouped-source binding map through the synthetic subquery's output schema and
LATERAL child re-resolution, with the R102 exact values and SQLSTATE controls.
Only after that semantic work can R101's forced-order measurements be resumed;
their PG cost margin remains non-evidence for a Goopg cost change.
