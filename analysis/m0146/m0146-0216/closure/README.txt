# M0146-0216 closure census (2026-10-20, HEAD b0c982657, corpora rebuilt by the owner in bb7b36ceb)
# census.sh <sf>: fresh clone of the shipped corpus on a private port; pg_class relpages / relallvisible / reltuples
# and pg_relation_size(c.oid) for every public table and index, against :65438 (SELECT only).
# census-sf025-diff.txt: 0 of 49 common relations differ.
# census-sf1-diff.txt:   1 of 49 differ — dbgen_version phys 0/1: the rebuild script's TABLES list omits
#   dbgen_version, so the goopg corpus has 0 rows where PG's has 1 (relpages/reltuples match only because they
#   are PG's imported statistics). No TPC-DS query reads the table.
# Not relations of the corpus: six goopg system-catalog indexes listed under public (ledgered oddity), and `bt`, a
#   table present only in PG's tpcds database.
# Probe note: in goopg pg_relation_size(c.oid::regclass) returns NULL where pg_relation_size(c.oid) and
#   pg_relation_size(c.relname::regclass) work (ledger, same family as pg_relation_size('name')).
