package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

// TestTpchSupplementaryIndexesAllSucceed verifies M0044-0006's primary
// gate: all 16 supplementary TPC-H indexes build successfully against
// the HammerDB schema. Before M0044 (varchar/char/timestamp key
// encoding), 8 of the 16 would abort with "btree v0 only supports
// int4 / numeric keys". After M0044-0001 through 0003, all 16 must
// succeed.
//
// Type map:
//   numeric   — always supported (int4/int8 too)
//   varchar   — M0044-0001
//   char      — M0044-0002
//   timestamp — M0044-0003
func TestTpchSupplementaryIndexesAllSucceed(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	// Create the TPC-H tables that have supplementary indexes.
	tables := []string{
		`CREATE TABLE region   (r_regionkey NUMERIC, r_name CHAR(25), r_comment VARCHAR(152))`,
		`CREATE TABLE nation   (n_nationkey NUMERIC NOT NULL, n_name CHAR(25), n_regionkey NUMERIC, n_comment VARCHAR(152))`,
		`CREATE TABLE supplier (s_suppkey NUMERIC NOT NULL, s_nationkey NUMERIC, s_comment VARCHAR(102), s_name CHAR(25), s_address VARCHAR(40), s_phone CHAR(15), s_acctbal NUMERIC)`,
		`CREATE TABLE customer (c_custkey NUMERIC NOT NULL, c_mktsegment CHAR(10), c_nationkey NUMERIC, c_name VARCHAR(25), c_address VARCHAR(40), c_phone CHAR(15), c_acctbal NUMERIC, c_comment VARCHAR(118))`,
		`CREATE TABLE part     (p_partkey NUMERIC NOT NULL, p_type VARCHAR(25), p_size NUMERIC, p_brand CHAR(10), p_name VARCHAR(55), p_container CHAR(10), p_mfgr CHAR(25), p_retailprice NUMERIC, p_comment VARCHAR(23))`,
		`CREATE TABLE partsupp (ps_partkey NUMERIC NOT NULL, ps_suppkey NUMERIC NOT NULL, ps_supplycost NUMERIC NOT NULL, ps_availqty NUMERIC, ps_comment VARCHAR(199))`,
		`CREATE TABLE orders   (o_orderdate TIMESTAMP, o_orderkey NUMERIC NOT NULL, o_custkey NUMERIC NOT NULL, o_orderpriority CHAR(15), o_shippriority NUMERIC, o_clerk CHAR(15), o_orderstatus CHAR(1), o_totalprice NUMERIC, o_comment VARCHAR(79))`,
		`CREATE TABLE lineitem (l_shipdate TIMESTAMP, l_orderkey NUMERIC NOT NULL, l_discount NUMERIC NOT NULL, l_extendedprice NUMERIC NOT NULL, l_suppkey NUMERIC NOT NULL, l_quantity NUMERIC NOT NULL, l_returnflag CHAR(1), l_partkey NUMERIC NOT NULL, l_linestatus CHAR(1), l_tax NUMERIC NOT NULL, l_commitdate TIMESTAMP, l_receiptdate TIMESTAMP, l_shipmode CHAR(10), l_linenumber NUMERIC NOT NULL, l_shipinstruct CHAR(25), l_comment VARCHAR(44))`,
	}
	for _, ddl := range tables {
		if err := runDDL(t, ctx, ddl); err != nil {
			t.Fatalf("schema DDL: %v", err)
		}
	}

	// All 16 supplementary indexes (analysis/tpch-additional-indexes.md).
	type indexSpec struct {
		sql    string
		name   string
		kind   string
	}
	indexes := []indexSpec{
		{"CREATE INDEX idx_nation_regionkey    ON nation   (n_regionkey)",   "idx_nation_regionkey",    "numeric"},
		{"CREATE INDEX idx_part_type           ON part     (p_type)",         "idx_part_type",           "varchar"},   // M0044-0001
		{"CREATE INDEX idx_part_size           ON part     (p_size)",         "idx_part_size",           "numeric"},
		{"CREATE INDEX idx_supplier_nationkey  ON supplier (s_nationkey)",    "idx_supplier_nationkey",  "numeric"},
		{"CREATE INDEX idx_customer_nationkey  ON customer (c_nationkey)",    "idx_customer_nationkey",  "numeric"},
		{"CREATE INDEX idx_customer_mktsegment ON customer (c_mktsegment)",   "idx_customer_mktsegment", "char"},      // M0044-0002
		{"CREATE INDEX idx_orders_custkey      ON orders   (o_custkey)",      "idx_orders_custkey",      "numeric"},
		{"CREATE INDEX idx_orders_orderdate    ON orders   (o_orderdate)",    "idx_orders_orderdate",    "timestamp"}, // M0044-0003
		{"CREATE INDEX idx_lineitem_orderkey   ON lineitem (l_orderkey)",     "idx_lineitem_orderkey",   "numeric"},
		{"CREATE INDEX idx_lineitem_partkey    ON lineitem (l_partkey)",      "idx_lineitem_partkey",    "numeric"},
		{"CREATE INDEX idx_lineitem_suppkey    ON lineitem (l_suppkey)",      "idx_lineitem_suppkey",    "numeric"},
		{"CREATE INDEX idx_lineitem_shipdate   ON lineitem (l_shipdate)",     "idx_lineitem_shipdate",   "timestamp"}, // M0044-0003
		{"CREATE INDEX idx_lineitem_commitdate ON lineitem (l_commitdate)",   "idx_lineitem_commitdate", "timestamp"}, // M0044-0003
		{"CREATE INDEX idx_lineitem_receiptdate ON lineitem (l_receiptdate)", "idx_lineitem_receiptdate","timestamp"}, // M0044-0003
		{"CREATE INDEX idx_partsupp_partkey    ON partsupp (ps_partkey)",     "idx_partsupp_partkey",    "numeric"},
		{"CREATE INDEX idx_partsupp_suppkey    ON partsupp (ps_suppkey)",     "idx_partsupp_suppkey",    "numeric"},
	}

	succeeded := 0
	for _, idx := range indexes {
		if err := runDDL(t, ctx, idx.sql); err != nil {
			t.Errorf("FAIL %s (%s): %v", idx.name, idx.kind, err)
		} else {
			succeeded++
			t.Logf("OK   %s (%s)", idx.name, idx.kind)
		}
	}
	t.Logf("supplementary indexes: %d/%d succeeded", succeeded, len(indexes))
	if succeeded != len(indexes) {
		t.Fatalf("expected all %d indexes to succeed, got %d", len(indexes), succeeded)
	}

	// Verify each index appears in the catalog.
	for _, idx := range indexes {
		if _, ok := ctx.Catalog.LookupIndex(parser.ObjectName{Name: idx.name}); !ok {
			t.Errorf("index %q not in catalog after CREATE INDEX", idx.name)
		}
	}
}
