CREATE TABLE tenk1 (unique1 int4, unique2 int4, two int4, four int4, ten int4, twenty int4, hundred int4, thousand int4, twothousand int4, fivethous int4, tenthous int4, odd int4, even int4, stringu1 name, stringu2 name, string4 name);
\copy tenk1 FROM 'postgres/src/test/regress/data/tenk.data'
VACUUM ANALYZE tenk1;
CREATE INDEX tenk1_unique1 ON tenk1 USING btree(unique1 int4_ops);
CREATE INDEX tenk1_unique2 ON tenk1 USING btree(unique2 int4_ops);
CREATE INDEX tenk1_hundred ON tenk1 USING btree(hundred int4_ops);
CREATE INDEX tenk1_thous_tenthous ON tenk1 (thousand, tenthous);
