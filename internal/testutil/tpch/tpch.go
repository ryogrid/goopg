// Package tpch provides shared test fixtures for the HammerDB TPC-H
// query suite: the eight-table catalog and the parameter-substituted
// Q1..Q22 SQL strings. Multiple test packages (`internal/planner`
// pinning plan-time coverage, `internal/executor` pinning Build
// coverage, future end-to-end harnesses) consume this fixture to
// avoid drift.
//
// Schema and queries are taken verbatim from
// HammerDB/src/postgresql/pgolap.tcl: CREATE TABLE strings on lines
// 130-137 and the 22 query templates from the `set_query` proc on
// lines 982-1003. Placeholder values mirror the defaults
// `sub_query()` picks at runtime.
package tpch

import (
	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// Catalog seeds an in-memory catalog with the eight TPC-H tables
// HammerDB creates. Returns the catalog and any error from
// `CreateTable` so callers can `t.Fatal` themselves.
func Catalog() (catalog.Catalog, error) {
	c := catalog.NewInMemory()
	for _, def := range tableDefs() {
		if _, err := c.CreateTable(parser.ObjectName{Name: def.Name}, def.Columns); err != nil {
			return nil, err
		}
	}
	return c, nil
}

// TableDef bundles a name with its column definitions. Exported so
// callers that build their own catalog (e.g., on top of a real
// storage stack) can iterate the same set.
type TableDef struct {
	Name    string
	Columns []catalog.Column
}

// Tables returns the eight TPC-H table definitions in HammerDB's
// declaration order (orders, partsupp, customer, part, supplier,
// nation, region, lineitem from pgolap.tcl). Callers receive a fresh
// slice on every call.
func Tables() []TableDef { return tableDefs() }

// DDL returns the eight CREATE TABLE statements that match HammerDB's
// pgolap.tcl declarations (lines 130-137). Order is the same as
// `Tables()`. A v0 caller can issue these against a live `goopg`
// cluster via psql or libpq to populate the catalog before running
// queries against `Queries()`.
func DDL() []string {
	return []string{
		`CREATE TABLE region (r_regionkey NUMERIC, r_name CHAR(25), r_comment VARCHAR(152))`,
		`CREATE TABLE nation (n_nationkey NUMERIC NOT NULL, n_name CHAR(25), n_regionkey NUMERIC, n_comment VARCHAR(152))`,
		`CREATE TABLE supplier (s_suppkey NUMERIC NOT NULL, s_nationkey NUMERIC, s_comment VARCHAR(102), s_name CHAR(25), s_address VARCHAR(40), s_phone CHAR(15), s_acctbal NUMERIC)`,
		`CREATE TABLE customer (c_custkey NUMERIC NOT NULL, c_mktsegment CHAR(10), c_nationkey NUMERIC, c_name VARCHAR(25), c_address VARCHAR(40), c_phone CHAR(15), c_acctbal NUMERIC, c_comment VARCHAR(118))`,
		`CREATE TABLE part (p_partkey NUMERIC NOT NULL, p_type VARCHAR(25), p_size NUMERIC, p_brand CHAR(10), p_name VARCHAR(55), p_container CHAR(10), p_mfgr CHAR(25), p_retailprice NUMERIC, p_comment VARCHAR(23))`,
		`CREATE TABLE partsupp (ps_partkey NUMERIC NOT NULL, ps_suppkey NUMERIC NOT NULL, ps_supplycost NUMERIC NOT NULL, ps_availqty NUMERIC, ps_comment VARCHAR(199))`,
		`CREATE TABLE orders (o_orderdate TIMESTAMP, o_orderkey NUMERIC NOT NULL, o_custkey NUMERIC NOT NULL, o_orderpriority CHAR(15), o_shippriority NUMERIC, o_clerk CHAR(15), o_orderstatus CHAR(1), o_totalprice NUMERIC, o_comment VARCHAR(79))`,
		`CREATE TABLE lineitem (l_shipdate TIMESTAMP, l_orderkey NUMERIC NOT NULL, l_discount NUMERIC NOT NULL, l_extendedprice NUMERIC NOT NULL, l_suppkey NUMERIC NOT NULL, l_quantity NUMERIC NOT NULL, l_returnflag CHAR(1), l_partkey NUMERIC NOT NULL, l_linestatus CHAR(1), l_tax NUMERIC NOT NULL, l_commitdate TIMESTAMP, l_receiptdate TIMESTAMP, l_shipmode CHAR(10), l_linenumber NUMERIC NOT NULL, l_shipinstruct CHAR(25), l_comment VARCHAR(44))`,
	}
}

// SampleInserts returns a tiny synthetic dataset that's referentially
// consistent enough for Q1..Q22 to *execute* without error. It does
// not aim to match upstream PG result sets — many queries will return
// zero rows because the synthetic data falls outside their date /
// brand / nation filters. The harness's job is to surface
// executor-time crashes, not result-set divergence; that comes with
// the HammerDB SF1 path.
//
// Counts: 5 regions, 5 nations, 5 suppliers, 5 customers, 5 parts,
// 10 partsupp rows, 8 orders, 16 lineitems. All amounts use string
// literals so the NUMERIC text codec accepts them, and all dates fall
// inside `1994-01-01..1998-12-31` so most TPC-H date filters match.
func SampleInserts() []string {
	return []string{
		// region: r_regionkey, r_name, r_comment
		`INSERT INTO region VALUES (0, 'AFRICA', 'a'), (1, 'AMERICA', 'b'), (2, 'ASIA', 'c'), (3, 'EUROPE', 'd'), (4, 'MIDDLE EAST', 'e')`,
		// nation: n_nationkey, n_name, n_regionkey, n_comment
		`INSERT INTO nation VALUES (0, 'ALGERIA', 0, 'a'), (1, 'BRAZIL', 1, 'b'), (2, 'CHINA', 2, 'c'), (3, 'FRANCE', 3, 'd'), (4, 'GERMANY', 3, 'e')`,
		// supplier: s_suppkey, s_nationkey, s_comment, s_name, s_address, s_phone, s_acctbal
		`INSERT INTO supplier VALUES (1, 3, 'sc', 's1', 'sa', '555-0001', '100.00'), (2, 4, 'sc', 's2', 'sa', '555-0002', '200.00'), (3, 1, 'sc', 's3', 'sa', '555-0003', '300.00'), (4, 0, 'sc', 's4', 'sa', '555-0004', '400.00'), (5, 2, 'sc', 's5', 'sa', '555-0005', '500.00')`,
		// customer: c_custkey, c_mktsegment, c_nationkey, c_name, c_address, c_phone, c_acctbal, c_comment
		`INSERT INTO customer VALUES (1, 'BUILDING', 3, 'c1', 'a', '13-1', '100.00', 'cm'), (2, 'AUTOMOBILE', 1, 'c2', 'a', '31-1', '200.00', 'cm'), (3, 'BUILDING', 4, 'c3', 'a', '23-1', '300.00', 'cm'), (4, 'MACHINERY', 2, 'c4', 'a', '29-1', '400.00', 'cm'), (5, 'HOUSEHOLD', 0, 'c5', 'a', '30-1', '500.00', 'cm')`,
		// part: p_partkey, p_type, p_size, p_brand, p_name, p_container, p_mfgr, p_retailprice, p_comment
		`INSERT INTO part VALUES (1, 'PROMO BURNISHED COPPER', 15, 'Brand#12', 'green forest', 'SM CASE', 'm1', '1000.00', 'pc'), (2, 'STANDARD ANODIZED BRASS', 14, 'Brand#23', 'forest spring', 'MED BAG', 'm2', '2000.00', 'pc'), (3, 'ECONOMY ANODIZED STEEL', 23, 'Brand#34', 'spring summer', 'LG CASE', 'm3', '3000.00', 'pc'), (4, 'PROMO PLATED TIN', 49, 'Brand#45', 'summer forest', 'SM BOX', 'm4', '4000.00', 'pc'), (5, 'LARGE BRUSHED BRASS', 9, 'Brand#11', 'forest green', 'MED PKG', 'm5', '5000.00', 'pc')`,
		// partsupp: ps_partkey, ps_suppkey, ps_supplycost, ps_availqty, ps_comment
		`INSERT INTO partsupp VALUES (1, 1, '10.00', 100, 'p'), (1, 2, '11.00', 200, 'p'), (2, 2, '12.00', 300, 'p'), (2, 3, '13.00', 400, 'p'), (3, 3, '14.00', 500, 'p'), (3, 4, '15.00', 600, 'p'), (4, 4, '16.00', 700, 'p'), (4, 5, '17.00', 800, 'p'), (5, 5, '18.00', 900, 'p'), (5, 1, '19.00', 1000, 'p')`,
		// orders: o_orderdate, o_orderkey, o_custkey, o_orderpriority, o_shippriority, o_clerk, o_orderstatus, o_totalprice, o_comment
		`INSERT INTO orders VALUES (timestamp '1995-03-10 00:00:00', 1, 1, '1-URGENT', 0, 'Clerk#1', 'O', '1000.00', 'oc'), (timestamp '1995-03-20 00:00:00', 2, 2, '2-HIGH', 0, 'Clerk#2', 'F', '2000.00', 'oc'), (timestamp '1993-08-01 00:00:00', 3, 3, '3-MEDIUM', 0, 'Clerk#3', 'F', '3000.00', 'oc'), (timestamp '1994-01-15 00:00:00', 4, 4, '4-NOT SPECIFIED', 0, 'Clerk#4', 'O', '4000.00', 'oc'), (timestamp '1996-01-15 00:00:00', 5, 5, '5-LOW', 0, 'Clerk#5', 'F', '5000.00', 'oc'), (timestamp '1995-09-15 00:00:00', 6, 1, '1-URGENT', 0, 'Clerk#6', 'F', '6000.00', 'oc'), (timestamp '1993-10-15 00:00:00', 7, 2, '2-HIGH', 0, 'Clerk#7', 'F', '7000.00', 'oc'), (timestamp '1994-07-01 00:00:00', 8, 3, '3-MEDIUM', 0, 'Clerk#8', 'O', '8000.00', 'oc')`,
		// lineitem: l_shipdate, l_orderkey, l_discount, l_extendedprice, l_suppkey, l_quantity, l_returnflag, l_partkey, l_linestatus, l_tax, l_commitdate, l_receiptdate, l_shipmode, l_linenumber, l_shipinstruct, l_comment
		`INSERT INTO lineitem VALUES (timestamp '1995-04-15 00:00:00', 1, '0.05', '1500.00', 1, 10, 'N', 1, 'O', '0.08', timestamp '1995-03-15 00:00:00', timestamp '1995-04-25 00:00:00', 'AIR', 1, 'DELIVER IN PERSON', 'lc'), (timestamp '1995-04-20 00:00:00', 1, '0.06', '2500.00', 2, 20, 'N', 2, 'O', '0.07', timestamp '1995-04-01 00:00:00', timestamp '1995-05-01 00:00:00', 'MAIL', 2, 'DELIVER IN PERSON', 'lc'), (timestamp '1995-05-01 00:00:00', 2, '0.04', '3500.00', 3, 30, 'R', 3, 'F', '0.06', timestamp '1995-04-15 00:00:00', timestamp '1995-05-15 00:00:00', 'SHIP', 1, 'DELIVER IN PERSON', 'lc'), (timestamp '1993-09-01 00:00:00', 3, '0.03', '4500.00', 4, 5, 'R', 4, 'F', '0.05', timestamp '1993-08-15 00:00:00', timestamp '1993-09-15 00:00:00', 'AIR', 1, 'DELIVER IN PERSON', 'lc'), (timestamp '1994-02-01 00:00:00', 4, '0.05', '5500.00', 5, 15, 'A', 5, 'F', '0.07', timestamp '1994-01-25 00:00:00', timestamp '1994-02-15 00:00:00', 'AIR REG', 1, 'DELIVER IN PERSON', 'lc'), (timestamp '1996-02-01 00:00:00', 5, '0.07', '6500.00', 1, 25, 'N', 1, 'O', '0.09', timestamp '1996-01-25 00:00:00', timestamp '1996-02-15 00:00:00', 'AIR', 1, 'DELIVER IN PERSON', 'lc'), (timestamp '1995-10-01 00:00:00', 6, '0.05', '7500.00', 2, 12, 'R', 2, 'F', '0.06', timestamp '1995-09-25 00:00:00', timestamp '1995-10-10 00:00:00', 'MAIL', 1, 'DELIVER IN PERSON', 'lc'), (timestamp '1993-11-01 00:00:00', 7, '0.04', '8500.00', 3, 8, 'R', 3, 'F', '0.05', timestamp '1993-10-25 00:00:00', timestamp '1993-11-10 00:00:00', 'SHIP', 1, 'DELIVER IN PERSON', 'lc'), (timestamp '1994-08-01 00:00:00', 8, '0.06', '9500.00', 4, 18, 'N', 4, 'O', '0.07', timestamp '1994-07-25 00:00:00', timestamp '1994-08-10 00:00:00', 'AIR', 1, 'DELIVER IN PERSON', 'lc'), (timestamp '1995-04-25 00:00:00', 1, '0.02', '1100.00', 3, 7, 'N', 3, 'O', '0.04', timestamp '1995-04-15 00:00:00', timestamp '1995-05-05 00:00:00', 'AIR', 3, 'DELIVER IN PERSON', 'lc'), (timestamp '1996-02-15 00:00:00', 5, '0.05', '6700.00', 2, 22, 'A', 2, 'O', '0.07', timestamp '1996-02-01 00:00:00', timestamp '1996-02-25 00:00:00', 'AIR REG', 2, 'DELIVER IN PERSON', 'lc'), (timestamp '1995-09-25 00:00:00', 6, '0.04', '7800.00', 4, 14, 'A', 4, 'F', '0.05', timestamp '1995-09-15 00:00:00', timestamp '1995-10-05 00:00:00', 'AIR', 2, 'DELIVER IN PERSON', 'lc'), (timestamp '1993-10-25 00:00:00', 7, '0.03', '8200.00', 5, 6, 'A', 5, 'F', '0.04', timestamp '1993-10-15 00:00:00', timestamp '1993-11-01 00:00:00', 'SHIP', 2, 'DELIVER IN PERSON', 'lc'), (timestamp '1994-07-25 00:00:00', 8, '0.05', '9300.00', 1, 19, 'N', 1, 'O', '0.06', timestamp '1994-07-15 00:00:00', timestamp '1994-08-01 00:00:00', 'AIR', 2, 'DELIVER IN PERSON', 'lc'), (timestamp '1995-04-01 00:00:00', 2, '0.03', '3200.00', 4, 11, 'A', 4, 'O', '0.05', timestamp '1995-03-20 00:00:00', timestamp '1995-04-10 00:00:00', 'AIR REG', 3, 'DELIVER IN PERSON', 'lc'), (timestamp '1995-09-30 00:00:00', 6, '0.02', '7100.00', 5, 9, 'N', 5, 'O', '0.04', timestamp '1995-09-20 00:00:00', timestamp '1995-10-08 00:00:00', 'AIR', 3, 'DELIVER IN PERSON', 'lc')`,
	}
}

// Queries returns Q1..Q22 SQL strings keyed by query number.
// Q15 is the first of three statements (CREATE OR REPLACE VIEW);
// the SELECT and DROP VIEW pieces are exercised separately because
// HammerDB's runner submits them as a `\;` split inside the same
// query slot.
func Queries() map[int]string {
	return map[int]string{
		1:  "select l_returnflag, l_linestatus, sum(l_quantity) as sum_qty, sum(l_extendedprice) as sum_base_price, sum(l_extendedprice * (1 - l_discount)) as sum_disc_price, sum(l_extendedprice * (1 - l_discount) * (1 + l_tax)) as sum_charge, avg(l_quantity) as avg_qty, avg(l_extendedprice) as avg_price, avg(l_discount) as avg_disc, count(*) as count_order from lineitem where l_shipdate <= date '1998-12-01' - interval '90 day' group by l_returnflag, l_linestatus order by l_returnflag, l_linestatus",
		2:  "select s_acctbal, s_name, n_name, p_partkey, p_mfgr, s_address, s_phone, s_comment from part, supplier, partsupp, nation, region where p_partkey = ps_partkey and s_suppkey = ps_suppkey and p_size = 15 and p_type like '%BRASS' and s_nationkey = n_nationkey and n_regionkey = r_regionkey and r_name = 'EUROPE' and ps_supplycost = ( select min(ps_supplycost) from partsupp, supplier, nation, region where p_partkey = ps_partkey and s_suppkey = ps_suppkey and s_nationkey = n_nationkey and n_regionkey = r_regionkey and r_name = 'EUROPE') order by s_acctbal desc, n_name, s_name, p_partkey",
		3:  "select l_orderkey, sum(l_extendedprice * (1 - l_discount)) as revenue, o_orderdate, o_shippriority from customer, orders, lineitem where c_mktsegment = 'BUILDING' and c_custkey = o_custkey and l_orderkey = o_orderkey and o_orderdate < date '1995-03-15' and l_shipdate > date '1995-03-15' group by l_orderkey, o_orderdate, o_shippriority order by revenue desc, o_orderdate",
		4:  "select o_orderpriority, count(*) as order_count from orders where o_orderdate >= date '1993-07-01' and o_orderdate < date '1993-07-01' + interval '3 month' and exists ( select * from lineitem where l_orderkey = o_orderkey and l_commitdate < l_receiptdate) group by o_orderpriority order by o_orderpriority",
		5:  "select n_name, sum(l_extendedprice * (1 - l_discount)) as revenue from customer, orders, lineitem, supplier, nation, region where c_custkey = o_custkey and l_orderkey = o_orderkey and l_suppkey = s_suppkey and c_nationkey = s_nationkey and s_nationkey = n_nationkey and n_regionkey = r_regionkey and r_name = 'ASIA' and o_orderdate >= date '1994-01-01' and o_orderdate < date '1994-01-01' + interval '1 year' group by n_name order by revenue desc",
		6:  "select sum(l_extendedprice * l_discount) as revenue from lineitem where l_shipdate >= date '1994-01-01' and l_shipdate < date '1994-01-01' + interval '1 year' and l_discount between 0.05 - 0.01 and 0.05 + 0.01 and l_quantity < 24",
		7:  "select supp_nation, cust_nation, l_year, sum(volume) as revenue from ( select n1.n_name as supp_nation, n2.n_name as cust_nation, extract(year from l_shipdate) as l_year, l_extendedprice * (1 - l_discount) as volume from supplier, lineitem, orders, customer, nation n1, nation n2 where s_suppkey = l_suppkey and o_orderkey = l_orderkey and c_custkey = o_custkey and s_nationkey = n1.n_nationkey and c_nationkey = n2.n_nationkey and ( (n1.n_name = 'FRANCE' and n2.n_name = 'GERMANY') or (n1.n_name = 'GERMANY' and n2.n_name = 'FRANCE')) and l_shipdate between date '1995-01-01' and date '1996-12-31') shipping group by supp_nation, cust_nation, l_year order by supp_nation, cust_nation, l_year",
		8:  "select o_year, sum(case when nation = 'BRAZIL' then volume else 0 end) / sum(volume) as mkt_share from ( select extract(year from o_orderdate) as o_year, l_extendedprice * (1 - l_discount) as volume, n2.n_name as nation from part, supplier, lineitem, orders, customer, nation n1, nation n2, region where p_partkey = l_partkey and s_suppkey = l_suppkey and l_orderkey = o_orderkey and o_custkey = c_custkey and c_nationkey = n1.n_nationkey and n1.n_regionkey = r_regionkey and r_name = 'AMERICA' and s_nationkey = n2.n_nationkey and o_orderdate between date '1995-01-01' and date '1996-12-31' and p_type = 'ECONOMY ANODIZED STEEL') all_nations group by o_year order by o_year",
		9:  "select nation, o_year, sum(amount) as sum_profit from ( select n_name as nation, extract(year from o_orderdate) as o_year, l_extendedprice * (1 - l_discount) - ps_supplycost * l_quantity as amount from part, supplier, lineitem, partsupp, orders, nation where s_suppkey = l_suppkey and ps_suppkey = l_suppkey and ps_partkey = l_partkey and p_partkey = l_partkey and o_orderkey = l_orderkey and s_nationkey = n_nationkey and p_name like '%green%') profit group by nation, o_year order by nation, o_year desc",
		10: "select c_custkey, c_name, sum(l_extendedprice * (1 - l_discount)) as revenue, c_acctbal, n_name, c_address, c_phone, c_comment from customer, orders, lineitem, nation where c_custkey = o_custkey and l_orderkey = o_orderkey and o_orderdate >= date '1993-10-01' and o_orderdate < date '1993-10-01' + interval '3 month' and l_returnflag = 'R' and c_nationkey = n_nationkey group by c_custkey, c_name, c_acctbal, c_phone, n_name, c_address, c_comment order by revenue desc",
		11: "select ps_partkey, sum(ps_supplycost * ps_availqty) as value from partsupp, supplier, nation where ps_suppkey = s_suppkey and s_nationkey = n_nationkey and n_name = 'GERMANY' group by ps_partkey having sum(ps_supplycost * ps_availqty) > ( select sum(ps_supplycost * ps_availqty) * 0.0001000000 from partsupp, supplier, nation where ps_suppkey = s_suppkey and s_nationkey = n_nationkey and n_name = 'GERMANY') order by value desc",
		12: "select l_shipmode, sum(case when o_orderpriority = '1-URGENT' or o_orderpriority = '2-HIGH' then 1 else 0 end) as high_line_count, sum(case when o_orderpriority <> '1-URGENT' and o_orderpriority <> '2-HIGH' then 1 else 0 end) as low_line_count from orders, lineitem where o_orderkey = l_orderkey and l_shipmode in ('MAIL', 'SHIP') and l_commitdate < l_receiptdate and l_shipdate < l_commitdate and l_receiptdate >= date '1994-01-01' and l_receiptdate < date '1994-01-01' + interval '1 year' group by l_shipmode order by l_shipmode",
		13: "select c_count, count(*) as custdist from ( select c_custkey, count(o_orderkey) as c_count from customer left outer join orders on c_custkey = o_custkey and o_comment not like '%special%requests%' group by c_custkey) c_orders group by c_count order by custdist desc, c_count desc",
		14: "select 100.00 * sum(case when p_type like 'PROMO%' then l_extendedprice * (1 - l_discount) else 0 end) / sum(l_extendedprice * (1 - l_discount)) as promo_revenue from lineitem, part where l_partkey = p_partkey and l_shipdate >= date '1995-09-01' and l_shipdate < date '1995-09-01' + interval '1 month'",
		15: "create or replace view revenue0 (supplier_no, total_revenue) as select l_suppkey, sum(l_extendedprice * (1 - l_discount)) from lineitem where l_shipdate >= to_date( '1996-01-01', 'YYYY-MM-DD') and l_shipdate < (to_date ('1996-01-01', 'YYYY-MM-DD') + interval '3 month') group by l_suppkey",
		16: "select p_brand, p_type, p_size, count(distinct ps_suppkey) as supplier_cnt from partsupp, part where p_partkey = ps_partkey and p_brand <> 'Brand#45' and p_type not like 'MEDIUM POLISHED%' and p_size in (49, 14, 23, 45, 19, 3, 36, 9) and ps_suppkey not in ( select s_suppkey from supplier where s_comment like '%Customer%Complaints%') group by p_brand, p_type, p_size order by supplier_cnt desc, p_brand, p_type, p_size",
		17: "select sum(l_extendedprice) / 7.0 as avg_yearly from lineitem, part where p_partkey = l_partkey and p_brand = 'Brand#23' and p_container = 'MED BOX' and l_quantity < ( select 0.2 * avg(l_quantity) from lineitem where l_partkey = p_partkey)",
		18: "select c_name, c_custkey, o_orderkey, o_orderdate, o_totalprice, sum(l_quantity) from customer, orders, lineitem where o_orderkey in ( select l_orderkey from lineitem group by l_orderkey having sum(l_quantity) > 313) and c_custkey = o_custkey and o_orderkey = l_orderkey group by c_name, c_custkey, o_orderkey, o_orderdate, o_totalprice order by o_totalprice desc, o_orderdate",
		19: "select sum(l_extendedprice* (1 - l_discount)) as revenue from lineitem, part where ( p_partkey = l_partkey and p_brand = 'Brand#12' and p_container in ('SM CASE', 'SM BOX', 'SM PACK', 'SM PKG') and l_quantity >= 1 and l_quantity <= 1 + 10 and p_size between 1 and 5 and l_shipmode in ('AIR', 'AIR REG') and l_shipinstruct = 'DELIVER IN PERSON') or ( p_partkey = l_partkey and p_brand = 'Brand#23' and p_container in ('MED BAG', 'MED BOX', 'MED PKG', 'MED PACK') and l_quantity >= 10 and l_quantity <= 10 + 10 and p_size between 1 and 10 and l_shipmode in ('AIR', 'AIR REG') and l_shipinstruct = 'DELIVER IN PERSON') or ( p_partkey = l_partkey and p_brand = 'Brand#34' and p_container in ('LG CASE', 'LG BOX', 'LG PACK', 'LG PKG') and l_quantity >= 20 and l_quantity <= 20 + 10 and p_size between 1 and 15 and l_shipmode in ('AIR', 'AIR REG') and l_shipinstruct = 'DELIVER IN PERSON')",
		20: "select s_name, s_address from supplier, nation where s_suppkey in ( select ps_suppkey from partsupp where ps_partkey in ( select p_partkey from part where p_name like 'forest%') and ps_availqty > ( select 0.5 * sum(l_quantity) from lineitem where l_partkey = ps_partkey and l_suppkey = ps_suppkey and l_shipdate >= date '1994-01-01' and l_shipdate < date '1994-01-01' + interval '1 year')) and s_nationkey = n_nationkey and n_name = 'CANADA' order by s_name",
		21: "select s_name, count(*) as numwait from supplier, lineitem l1, orders, nation where s_suppkey = l1.l_suppkey and o_orderkey = l1.l_orderkey and o_orderstatus = 'F' and l1.l_receiptdate > l1.l_commitdate and exists ( select * from lineitem l2 where l2.l_orderkey = l1.l_orderkey and l2.l_suppkey <> l1.l_suppkey) and not exists ( select * from lineitem l3 where l3.l_orderkey = l1.l_orderkey and l3.l_suppkey <> l1.l_suppkey and l3.l_receiptdate > l3.l_commitdate) and s_nationkey = n_nationkey and n_name = 'SAUDI ARABIA' group by s_name order by numwait desc, s_name",
		22: "select cntrycode, count(*) as numcust, sum(c_acctbal) as totacctbal from ( select substr(c_phone, 1, 2) as cntrycode, c_acctbal from customer where substr(c_phone, 1, 2) in ('13', '31', '23', '29', '30', '18', '17') and c_acctbal > ( select avg(c_acctbal) from customer where c_acctbal > 0.00 and substr(c_phone, 1, 2) in ('13', '31', '23', '29', '30', '18', '17')) and not exists ( select * from orders where o_custkey = c_custkey)) custsale group by cntrycode order by cntrycode",
	}
}

func tableDefs() []TableDef {
	return []TableDef{
		{
			Name: "region",
			Columns: []catalog.Column{
				{Name: "r_regionkey", Type: catalog.Type{Name: "numeric"}},
				{Name: "r_name", Type: catalog.Type{Name: "char", Args: []int64{25}}},
				{Name: "r_comment", Type: catalog.Type{Name: "varchar", Args: []int64{152}}},
			},
		},
		{
			Name: "nation",
			Columns: []catalog.Column{
				{Name: "n_nationkey", Type: catalog.Type{Name: "numeric"}, NotNull: true},
				{Name: "n_name", Type: catalog.Type{Name: "char", Args: []int64{25}}},
				{Name: "n_regionkey", Type: catalog.Type{Name: "numeric"}},
				{Name: "n_comment", Type: catalog.Type{Name: "varchar", Args: []int64{152}}},
			},
		},
		{
			Name: "supplier",
			Columns: []catalog.Column{
				{Name: "s_suppkey", Type: catalog.Type{Name: "numeric"}, NotNull: true},
				{Name: "s_nationkey", Type: catalog.Type{Name: "numeric"}},
				{Name: "s_comment", Type: catalog.Type{Name: "varchar", Args: []int64{102}}},
				{Name: "s_name", Type: catalog.Type{Name: "char", Args: []int64{25}}},
				{Name: "s_address", Type: catalog.Type{Name: "varchar", Args: []int64{40}}},
				{Name: "s_phone", Type: catalog.Type{Name: "char", Args: []int64{15}}},
				{Name: "s_acctbal", Type: catalog.Type{Name: "numeric"}},
			},
		},
		{
			Name: "customer",
			Columns: []catalog.Column{
				{Name: "c_custkey", Type: catalog.Type{Name: "numeric"}, NotNull: true},
				{Name: "c_mktsegment", Type: catalog.Type{Name: "char", Args: []int64{10}}},
				{Name: "c_nationkey", Type: catalog.Type{Name: "numeric"}},
				{Name: "c_name", Type: catalog.Type{Name: "varchar", Args: []int64{25}}},
				{Name: "c_address", Type: catalog.Type{Name: "varchar", Args: []int64{40}}},
				{Name: "c_phone", Type: catalog.Type{Name: "char", Args: []int64{15}}},
				{Name: "c_acctbal", Type: catalog.Type{Name: "numeric"}},
				{Name: "c_comment", Type: catalog.Type{Name: "varchar", Args: []int64{118}}},
			},
		},
		{
			Name: "part",
			Columns: []catalog.Column{
				{Name: "p_partkey", Type: catalog.Type{Name: "numeric"}, NotNull: true},
				{Name: "p_type", Type: catalog.Type{Name: "varchar", Args: []int64{25}}},
				{Name: "p_size", Type: catalog.Type{Name: "numeric"}},
				{Name: "p_brand", Type: catalog.Type{Name: "char", Args: []int64{10}}},
				{Name: "p_name", Type: catalog.Type{Name: "varchar", Args: []int64{55}}},
				{Name: "p_container", Type: catalog.Type{Name: "char", Args: []int64{10}}},
				{Name: "p_mfgr", Type: catalog.Type{Name: "char", Args: []int64{25}}},
				{Name: "p_retailprice", Type: catalog.Type{Name: "numeric"}},
				{Name: "p_comment", Type: catalog.Type{Name: "varchar", Args: []int64{23}}},
			},
		},
		{
			Name: "partsupp",
			Columns: []catalog.Column{
				{Name: "ps_partkey", Type: catalog.Type{Name: "numeric"}, NotNull: true},
				{Name: "ps_suppkey", Type: catalog.Type{Name: "numeric"}, NotNull: true},
				{Name: "ps_supplycost", Type: catalog.Type{Name: "numeric"}, NotNull: true},
				{Name: "ps_availqty", Type: catalog.Type{Name: "numeric"}},
				{Name: "ps_comment", Type: catalog.Type{Name: "varchar", Args: []int64{199}}},
			},
		},
		{
			Name: "orders",
			Columns: []catalog.Column{
				{Name: "o_orderdate", Type: catalog.Type{Name: "timestamp"}},
				{Name: "o_orderkey", Type: catalog.Type{Name: "numeric"}, NotNull: true},
				{Name: "o_custkey", Type: catalog.Type{Name: "numeric"}, NotNull: true},
				{Name: "o_orderpriority", Type: catalog.Type{Name: "char", Args: []int64{15}}},
				{Name: "o_shippriority", Type: catalog.Type{Name: "numeric"}},
				{Name: "o_clerk", Type: catalog.Type{Name: "char", Args: []int64{15}}},
				{Name: "o_orderstatus", Type: catalog.Type{Name: "char", Args: []int64{1}}},
				{Name: "o_totalprice", Type: catalog.Type{Name: "numeric"}},
				{Name: "o_comment", Type: catalog.Type{Name: "varchar", Args: []int64{79}}},
			},
		},
		{
			Name: "lineitem",
			Columns: []catalog.Column{
				{Name: "l_shipdate", Type: catalog.Type{Name: "timestamp"}},
				{Name: "l_orderkey", Type: catalog.Type{Name: "numeric"}, NotNull: true},
				{Name: "l_discount", Type: catalog.Type{Name: "numeric"}, NotNull: true},
				{Name: "l_extendedprice", Type: catalog.Type{Name: "numeric"}, NotNull: true},
				{Name: "l_suppkey", Type: catalog.Type{Name: "numeric"}, NotNull: true},
				{Name: "l_quantity", Type: catalog.Type{Name: "numeric"}, NotNull: true},
				{Name: "l_returnflag", Type: catalog.Type{Name: "char", Args: []int64{1}}},
				{Name: "l_partkey", Type: catalog.Type{Name: "numeric"}, NotNull: true},
				{Name: "l_linestatus", Type: catalog.Type{Name: "char", Args: []int64{1}}},
				{Name: "l_tax", Type: catalog.Type{Name: "numeric"}, NotNull: true},
				{Name: "l_commitdate", Type: catalog.Type{Name: "timestamp"}},
				{Name: "l_receiptdate", Type: catalog.Type{Name: "timestamp"}},
				{Name: "l_shipmode", Type: catalog.Type{Name: "char", Args: []int64{10}}},
				{Name: "l_linenumber", Type: catalog.Type{Name: "numeric"}, NotNull: true},
				{Name: "l_shipinstruct", Type: catalog.Type{Name: "char", Args: []int64{25}}},
				{Name: "l_comment", Type: catalog.Type{Name: "varchar", Args: []int64{44}}},
			},
		},
	}
}
