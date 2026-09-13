# R109 result: normalized witness exposes varchar data loss

R109 used the stopped R107 disposable clusters only. Its type-aware witness
does not rescue the common-data oracle: after allowed `char(n)` and numeric
normalization, `store` still differs in a `varchar` field. The stop rule
applies; R108 and cost attribution remain blocked on a data-fidelity fix.

## Method

The client-side `/tmp/r109_copy_canonicalize.py` source SHA-256 was
`1e8a8a925844b61ed57a0094751a404623ce048a76d15c5703a8fc310b6959d6`.
It parses PostgreSQL text-COPY `\N` and backslash escapes before emitting a
length-framed canonical byte stream. It removes trailing ASCII spaces only
from DDL `char(n)` fields and trailing fractional zeros only from finite DDL
`numeric` fields; every `varchar` byte is retained. The schema SHA-256 is
R107's `508f77e089457098bd22d4e6e80ec1cf5f624e16e2eba482f4f5c4451269f3ae`.

Both connections reported UTF8, `DateStyle=ISO, MDY`, `IntervalStyle=postgres`,
and `extra_float_digits=1`. Each statement was `COPY (SELECT all declared
columns FROM table ORDER BY declared_primary_key) TO STDOUT` over the R107
row counts and schema. Services on private ports 65443 and 5564 were stopped
after collection.

| table | PG normalized SHA-256 | Goopg normalized SHA-256 | verdict |
| --- | --- | --- | --- |
| store_sales | `27b58afeba052316b70171975efaa5004fe7f3970575b084a5e6f9d23d6271b0` | `27b58afeba052316b70171975efaa5004fe7f3970575b084a5e6f9d23d6271b0` | match |
| household_demographics | `429944eff57e3d75231f127181df05d42e57f974d969aedd3abe3abc020c29b5` | `429944eff57e3d75231f127181df05d42e57f974d969aedd3abe3abc020c29b5` | match |
| time_dim | `9cb0088158a0601f400b4654234b585818a2fa37e80bbd55973c7fc016b0f011` | `9cb0088158a0601f400b4654234b585818a2fa37e80bbd55973c7fc016b0f011` | match |
| store | `040c1a49656da7c6052629f42d0bf0858220da6df38f8a4a782bbb58d4e51696` | `9d6a3122916f32c4b05f502fc925c1e16d169f5c647a3ab4f91d29aa8f7763de` | mismatch |

The first `store` difference is `s_street_name varchar(60)`: PG preserves
`Spring ` while Goopg emits `Spring`. This field is deliberately not
normalized; trailing whitespace in `varchar` is semantic data, unlike
`char(n)` padding. Thus matching Q96-only projections and the three matching
relations do not establish full relation equivalence.

## Ruling

**STOP: normalized full relation witness fails.** This result neither proves
general SQL semantic equivalence nor supplies a common-data cost oracle. Fix
or separately scope Goopg's `varchar` trailing-space ingestion/output fidelity,
then rebuild a full witness before R108's PG hash-size experiment.
