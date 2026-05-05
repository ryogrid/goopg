// hammerdb_load is a standalone Go loader that reproduces HammerDB's
// TPC-H schema-build INSERT shape against a goopg cluster, without
// the HammerDB TCL toolchain.
//
// The TPC-H spec for ORDERS / LINEITEM is well-defined; HammerDB
// (HammerDB/src/postgresql/pgolap.tcl:454) drives the load via batched
// multi-row INSERT statements. The same shape against goopg
// reproduces the symptom captured in
// analysis/tpch-hammerdb-run-002.md (loader connection drops at
// ~430 k orders / 1 M lineitems on SF=1).
//
// This harness:
//   - generates synthetic ORDERS+LINEITEM rows with the right schema
//     and column types (values are random but type-stable; the goal
//     is INSERT throughput, not query-result correctness — for that,
//     HammerDB's own dbgen is still authoritative).
//   - issues `INSERT INTO ORDERS (...) VALUES (...),(...)…` and the
//     LINEITEM equivalent in batches matching --batch-rows (HammerDB
//     defaults to ~10).
//   - commits every --commit-interval batches so we can match
//     HammerDB's commit cadence.
//
// Usage example:
//
//	go run ./bench/tpch/cmd/hammerdb_load \
//	    --addr 127.0.0.1:65433 --user tpch --db tpch \
//	    --scale 1 --batch-rows 10 --commit-interval 100 \
//	    --limit-orders 100000
//
// The companion `bench/tpch/profile_load.sh` wraps this with a
// goopg pprof capture; the resulting CPU+heap profiles feed the
// M0032-0005 throughput-fix work.
package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"log"
	"math/rand"
	"strings"
	"time"

	_ "github.com/lib/pq"
)

// HammerDB ships SF=1 with these row counts (see TPC-H spec §4.2.5):
const (
	ordersPerSF   = 1_500_000
	lineitemsAvg  = 4 // average lineitems per order; per-order varies 1..7
	suppliersPerSF = 10_000
	partsPerSF     = 200_000
	customersPerSF = 150_000
)

func main() {
	var (
		addr           = flag.String("addr", "127.0.0.1:65433", "host:port of the goopg cluster")
		user           = flag.String("user", "postgres", "database role")
		password       = flag.String("password", "", "role password (empty = trust)")
		dbname         = flag.String("db", "postgres", "target database name")
		scale          = flag.Int("scale", 1, "TPC-H scale factor (orders ≈ scale * 1.5M)")
		batchRows      = flag.Int("batch-rows", 10, "rows per INSERT statement (HammerDB default ~10)")
		commitInterval = flag.Int("commit-interval", 100, "commit every N orders (HammerDB default 100)")
		limitOrders    = flag.Int("limit-orders", 0, "stop after N orders (0 = full SF)")
		seed           = flag.Int64("seed", 1, "RNG seed for synthetic data")
		skipCreate     = flag.Bool("skip-create", false, "skip CREATE TABLE (assume schema exists)")
	)
	flag.Parse()

	dsn := fmt.Sprintf("host=%s port=%s user=%s dbname=%s sslmode=disable",
		hostOnly(*addr), portOnly(*addr), *user, *dbname)
	if *password != "" {
		dsn += " password=" + *password
	}

	db, err := sql.Open("postgres", dsn)
	if err != nil {
		log.Fatalf("sql.Open: %v", err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Hour)
	defer cancel()

	if err := db.PingContext(ctx); err != nil {
		log.Fatalf("ping: %v", err)
	}

	if !*skipCreate {
		if err := createSchema(ctx, db); err != nil {
			log.Fatalf("createSchema: %v", err)
		}
	}

	rng := rand.New(rand.NewSource(*seed))
	totalOrders := *scale * ordersPerSF
	if *limitOrders > 0 && *limitOrders < totalOrders {
		totalOrders = *limitOrders
	}

	loader := &loader{
		db:             db,
		rng:            rng,
		batchRows:      *batchRows,
		commitInterval: *commitInterval,
		scale:          *scale,
	}
	if err := loader.run(ctx, totalOrders); err != nil {
		log.Fatalf("load: %v", err)
	}
}

func hostOnly(addr string) string {
	i := strings.LastIndex(addr, ":")
	if i < 0 {
		return addr
	}
	return addr[:i]
}

func portOnly(addr string) string {
	i := strings.LastIndex(addr, ":")
	if i < 0 {
		return ""
	}
	return addr[i+1:]
}

type loader struct {
	db             *sql.DB
	rng            *rand.Rand
	batchRows      int
	commitInterval int
	scale          int
}

func (l *loader) run(ctx context.Context, totalOrders int) error {
	start := time.Now()
	log.Printf("loading orders=%d, batch-rows=%d, commit-interval=%d",
		totalOrders, l.batchRows, l.commitInterval)

	// Pin one underlying connection so explicit BEGIN/COMMIT
	// statements stay on the same backend goroutine. lib/pq's
	// db.BeginTx() emits `BEGIN READ WRITE` which goopg's parser
	// rejects today; the plain `BEGIN` / `COMMIT` shape that
	// HammerDB's pg_exec uses works fine.
	conn, err := l.db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("acquire conn: %w", err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "BEGIN"); err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	commitNow := func() error {
		if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
			return fmt.Errorf("commit: %w", err)
		}
		if _, err := conn.ExecContext(ctx, "BEGIN"); err != nil {
			return fmt.Errorf("begin (after commit): %w", err)
		}
		return nil
	}

	var orderBuf, lineBuf orderLineBatch
	orderBuf.reset()
	lineBuf.reset()

	insertedOrders := 0
	insertedLines := 0
	for okey := 1; okey <= totalOrders; okey++ {
		ord := l.genOrder(okey)
		orderBuf.appendOrder(ord)
		nLines := 1 + l.rng.Intn(7)
		for ln := 1; ln <= nLines; ln++ {
			line := l.genLineitem(okey, ln)
			lineBuf.appendLine(line)
			if lineBuf.lines == l.batchRows {
				if err := flushLines(ctx, conn, &lineBuf); err != nil {
					return err
				}
				insertedLines += l.batchRows
			}
		}
		if orderBuf.orders == l.batchRows {
			if err := flushOrders(ctx, conn, &orderBuf); err != nil {
				return err
			}
			insertedOrders += l.batchRows
		}
		if okey%l.commitInterval == 0 {
			if err := flushLines(ctx, conn, &lineBuf); err != nil {
				return err
			}
			insertedLines += lineBuf.lines
			lineBuf.reset()
			if err := flushOrders(ctx, conn, &orderBuf); err != nil {
				return err
			}
			insertedOrders += orderBuf.orders
			orderBuf.reset()
			if err := commitNow(); err != nil {
				return err
			}
		}
		if okey%10000 == 0 {
			elapsed := time.Since(start).Seconds()
			rate := float64(okey) / elapsed
			log.Printf("orders=%d lineitems=%d elapsed=%.1fs orders/s=%.0f",
				okey, insertedLines+lineBuf.lines, elapsed, rate)
		}
	}
	// Final flush.
	if err := flushLines(ctx, conn, &lineBuf); err != nil {
		return err
	}
	insertedLines += lineBuf.lines
	if err := flushOrders(ctx, conn, &orderBuf); err != nil {
		return err
	}
	insertedOrders += orderBuf.orders
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return fmt.Errorf("final commit: %w", err)
	}
	elapsed := time.Since(start).Seconds()
	log.Printf("done: orders=%d lineitems=%d elapsed=%.1fs orders/s=%.0f lineitems/s=%.0f",
		insertedOrders, insertedLines, elapsed,
		float64(insertedOrders)/elapsed, float64(insertedLines)/elapsed)
	return nil
}

// execer captures the subset of *sql.Conn / *sql.Tx the loader
// uses so the flush helpers stay polymorphic across the
// pinned-connection-with-explicit-BEGIN model and a future
// stmt-cached path.
type execer interface {
	ExecContext(ctx context.Context, query string, args ...interface{}) (sql.Result, error)
}

func flushOrders(ctx context.Context, ex execer, b *orderLineBatch) error {
	if b.orders == 0 {
		return nil
	}
	stmt := "INSERT INTO orders (o_orderdate, o_orderkey, o_custkey, o_orderpriority, o_shippriority, o_clerk, o_orderstatus, o_totalprice, o_comment) VALUES " + b.orderValues.String()
	if _, err := ex.ExecContext(ctx, stmt); err != nil {
		return fmt.Errorf("insert orders (rows=%d): %w", b.orders, err)
	}
	b.resetOrders()
	return nil
}

func flushLines(ctx context.Context, ex execer, b *orderLineBatch) error {
	if b.lines == 0 {
		return nil
	}
	stmt := "INSERT INTO lineitem (l_shipdate, l_orderkey, l_discount, l_extendedprice, l_suppkey, l_quantity, l_returnflag, l_partkey, l_linestatus, l_tax, l_commitdate, l_receiptdate, l_shipmode, l_linenumber, l_shipinstruct, l_comment) VALUES " + b.lineValues.String()
	if _, err := ex.ExecContext(ctx, stmt); err != nil {
		return fmt.Errorf("insert lineitem (rows=%d): %w", b.lines, err)
	}
	b.resetLines()
	return nil
}

// orderLineBatch holds the per-batch VALUES strings for orders and
// lineitems. We build the SQL text directly (matching HammerDB's
// shape — pg_exec on a single prepared string) instead of using
// driver-side parameters so the server-side parser/planner cost is
// representative of the HammerDB load.
type orderLineBatch struct {
	orderValues strings.Builder
	orders      int
	lineValues  strings.Builder
	lines       int
}

func (b *orderLineBatch) reset() {
	b.resetOrders()
	b.resetLines()
}
func (b *orderLineBatch) resetOrders() {
	b.orderValues.Reset()
	b.orders = 0
}
func (b *orderLineBatch) resetLines() {
	b.lineValues.Reset()
	b.lines = 0
}
func (b *orderLineBatch) appendOrder(o order) {
	if b.orders > 0 {
		b.orderValues.WriteString(",")
	}
	fmt.Fprintf(&b.orderValues,
		"(timestamp '%s', %d, %d, '%s', %d, '%s', '%s', '%s', '%s')",
		o.orderdate, o.orderkey, o.custkey, escape(o.orderpriority),
		o.shippriority, escape(o.clerk), escape(o.orderstatus),
		o.totalprice, escape(o.comment))
	b.orders++
}
func (b *orderLineBatch) appendLine(l lineitem) {
	if b.lines > 0 {
		b.lineValues.WriteString(",")
	}
	fmt.Fprintf(&b.lineValues,
		"(timestamp '%s', %d, '%s', '%s', %d, %d, '%s', %d, '%s', '%s', timestamp '%s', timestamp '%s', '%s', %d, '%s', '%s')",
		l.shipdate, l.orderkey, l.discount, l.extendedprice, l.suppkey,
		l.quantity, escape(l.returnflag), l.partkey, escape(l.linestatus),
		l.tax, l.commitdate, l.receiptdate, escape(l.shipmode), l.linenumber,
		escape(l.shipinstruct), escape(l.comment))
	b.lines++
}

func escape(s string) string {
	return strings.ReplaceAll(s, "'", "''")
}
