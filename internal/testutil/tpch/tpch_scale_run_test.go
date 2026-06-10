package tpch_test

import (
	"context"
	"database/sql"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	_ "github.com/lib/pq"

	"github.com/goopg/goopg/internal/testutil/cluster"
	"github.com/goopg/goopg/internal/testutil/tpch"
)

// TestTPCHScaleLoadAndQueryRun is the scale-grade sibling of
// TestRunTPCHQueriesAgainstSyntheticData. Instead of the ~75-row
// SampleInserts dataset, it generates and bulk-loads a *2,000,000
// order* TPC-H dataset across all eight tables (orders + ~8 M
// lineitems plus SF-scaled region/nation/supplier/customer/part/
// partsupp), then runs each Q1..Q22 against the loaded cluster.
//
// What it asserts: every selected query *executes* against the
// 2 M-order dataset without raising an executor error (an empty
// result set is acceptable — random data may fall outside a query's
// filters). It does NOT assert result-set parity against upstream
// PostgreSQL: at this scale the data is randomly generated, so exact
// row equality is neither defined nor the point. Parity at small
// scale is covered by TestTPCHResultParity; this test's job is to
// surface scale-dependent failures (hash-join / sort spill, NUMERIC
// overflow on large aggregates, memory blowups, planner cardinality
// cliffs) that the tiny synthetic dataset cannot reach.
//
// The dataset is referentially consistent enough for the joins to
// connect: nations carry valid region keys, suppliers/customers
// carry valid nation keys, every lineitem (l_partkey, l_suppkey)
// pair exists in partsupp, and every order/lineitem references a
// live customer/part. Categorical columns use the canonical TPC-H
// value vocabularies (the 25 nations, the five market segments, the
// brand/container/type syllables, phone country codes) so the
// literal filters inside Q1..Q22 actually match rows.
//
// This test is cluster-backed and deliberately heavy; it is skipped
// under -short and lives behind an explicit `-run` in CI (the broad
// unit-test step excludes internal/testutil/tpch). Tunables:
//
//	GOOPG_TPCH_ORDERS=<n>      override the 2,000,000 order count
//	                          (dimension tables scale with SF=n/1.5M)
//	GOOPG_TPCH_QUERIES=1,6,12  run only these query numbers (default: all 22)
func TestTPCHScaleLoadAndQueryRun(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping cluster-backed TPC-H scale run in short mode")
	}

	orders := scaleEnvInt("GOOPG_TPCH_ORDERS", 2_000_000)
	sf := float64(orders) / 1_500_000.0
	suppliers := int(10_000 * sf)
	if suppliers < 4 {
		suppliers = 4
	}
	customers := int(150_000 * sf)
	if customers < 1 {
		customers = 1
	}
	parts := int(200_000 * sf)
	if parts < 1 {
		parts = 1
	}

	t.Logf("TPC-H scale run target: orders=%d (SF≈%.3f) suppliers=%d customers=%d parts=%d partsupp=%d",
		orders, sf, suppliers, customers, parts, parts*4)

	repoRoot := repoRoot(t)
	base := t.TempDir()
	c, err := cluster.New("tpch-scale", cluster.Options{
		RepoRoot:     repoRoot,
		DataDir:      filepath.Join(base, "data"),
		StartupWait:  30 * time.Second,
		ShutdownWait: 30 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Init(); err != nil {
		t.Fatal(err)
	}
	// Size the buffer pool for the 2 M-order working set. The value is
	// appended after init (which writes postgresql.conf) and before
	// start, so the server boots with it. "5GB" is PG's memory-unit
	// notation: a plain integer with a case-insensitive GB suffix and no
	// space — goopg's GUC parser converts it to shared_buffers' native
	// kB unit (see internal/config guc.go unitFromSuffix).
	if err := c.AppendPostgresqlConf("shared_buffers = 5GB"); err != nil {
		t.Fatal(err)
	}
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()

	// Bulk load goes through a direct libpq connection (batched
	// multi-row INSERT); queries and DDL go through the cluster's
	// psql helper. Both talk to the same server, so committed load
	// data is visible to the query path.
	dsn := fmt.Sprintf("postgres://postgres@%s/postgres?sslmode=disable", c.ListenAddr())
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer db.Close()
	db.SetMaxOpenConns(2)

	{
		pctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		if err := db.PingContext(pctx); err != nil {
			cancel()
			t.Fatalf("ping: %v", err)
		}
		cancel()
	}

	// --- schema ---
	for _, ddl := range tpch.DDL() {
		dctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		_, err := c.Query(dctx, ddl)
		cancel()
		if err != nil {
			t.Fatalf("DDL %q: %v", firstWords(ddl, 6), err)
		}
	}

	// --- load (no artificial deadline; `go test -timeout` is the backstop) ---
	l := &scaleLoader{
		db:        db,
		rng:       rand.New(rand.NewSource(20260609)),
		t:         t,
		suppliers: suppliers,
		customers: customers,
		parts:     parts,
		orders:    orders,
	}
	loadStart := time.Now()
	if err := l.loadAll(context.Background()); err != nil {
		t.Fatalf("load: %v", err)
	}
	t.Logf("load complete: %d orders in %s", orders, time.Since(loadStart).Round(time.Second))

	// --- ANALYZE so the planner has stats for the big joins ---
	for _, tbl := range []string{"region", "nation", "supplier", "customer",
		"part", "partsupp", "orders", "lineitem"} {
		actx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		if _, err := c.Query(actx, "ANALYZE "+tbl); err != nil {
			t.Logf("ANALYZE %s: %v (continuing)", tbl, err)
		}
		cancel()
	}

	// --- sanity: the order count actually landed ---
	{
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		rows, err := c.Query(sctx, "SELECT count(*) FROM orders")
		cancel()
		if err != nil {
			t.Fatalf("count orders: %v", err)
		}
		if len(rows) != 1 || len(rows[0]) != 1 {
			t.Fatalf("count orders: unexpected shape %v", rows)
		}
		got, err := strconv.Atoi(strings.TrimSpace(rows[0][0]))
		if err != nil {
			t.Fatalf("count orders: parse %q: %v", rows[0][0], err)
		}
		if got != orders {
			t.Errorf("orders count = %d, want %d", got, orders)
		}
		t.Logf("orders row count verified: %d", got)
	}

	// --- run the selected queries; assert each executes ---
	selected := scaleEnvQueries()
	queries := tpch.Queries()
	type qres struct {
		ok      bool
		rows    int
		elapsed time.Duration
		err     string
	}
	results := make(map[int]qres, len(selected))
	for _, q := range selected {
		qctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
		start := time.Now()
		rows, err := c.Query(qctx, queries[q])
		elapsed := time.Since(start)
		cancel()
		if err != nil {
			results[q] = qres{false, 0, elapsed, truncate(err.Error(), 240)}
			t.Logf("  Q%-2d FAIL (%s): %s", q, elapsed.Round(time.Millisecond), results[q].err)
			continue
		}
		results[q] = qres{true, len(rows), elapsed, ""}
		t.Logf("  Q%-2d OK   (%d rows, %s)", q, len(rows), elapsed.Round(time.Millisecond))
	}

	ran := 0
	for _, q := range selected {
		if results[q].ok {
			ran++
		}
	}
	t.Logf("TPC-H scale coverage: %d/%d queries ran OK at %d orders", ran, len(selected), orders)

	// Fail-closed: every selected query must execute without an
	// executor error. Zero rows is acceptable; a `pq:` error or a
	// timeout is a real scale regression to fix.
	for _, q := range selected {
		if !results[q].ok {
			t.Errorf("Q%d expected to execute at %d orders but errored: %s", q, orders, results[q].err)
		}
	}
}

// ---------------------------------------------------------------------------
// Environment knobs
// ---------------------------------------------------------------------------

func scaleEnvInt(key string, def int) int {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return def
	}
	return n
}

// scaleEnvQueries returns the query numbers to run. Default is all of
// Q1..Q22; GOOPG_TPCH_QUERIES="1,6,12" restricts to a subset (handy
// for bisecting a scale failure or trimming CI wall-clock).
func scaleEnvQueries() []int {
	all := make([]int, 0, 22)
	for q := 1; q <= 22; q++ {
		all = append(all, q)
	}
	v := strings.TrimSpace(os.Getenv("GOOPG_TPCH_QUERIES"))
	if v == "" {
		return all
	}
	var out []int
	seen := make(map[int]bool)
	for _, p := range strings.Split(v, ",") {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		n, err := strconv.Atoi(p)
		if err != nil || n < 1 || n > 22 || seen[n] {
			continue
		}
		seen[n] = true
		out = append(out, n)
	}
	if len(out) == 0 {
		return all
	}
	return out
}

// ---------------------------------------------------------------------------
// Reference vocabularies (canonical TPC-H value sets)
// ---------------------------------------------------------------------------

var scaleRegions = []string{"AFRICA", "AMERICA", "ASIA", "EUROPE", "MIDDLE EAST"}

type scaleNation struct {
	name      string
	regionkey int
}

// The 25 TPC-H nations with their region keys (dbgen dists.dss).
var scaleNations = []scaleNation{
	{"ALGERIA", 0}, {"ARGENTINA", 1}, {"BRAZIL", 1}, {"CANADA", 1}, {"EGYPT", 4},
	{"ETHIOPIA", 0}, {"FRANCE", 3}, {"GERMANY", 3}, {"INDIA", 2}, {"INDONESIA", 2},
	{"IRAN", 4}, {"IRAQ", 4}, {"JAPAN", 2}, {"JORDAN", 4}, {"KENYA", 0},
	{"MOROCCO", 0}, {"MOZAMBIQUE", 0}, {"PERU", 1}, {"CHINA", 2}, {"ROMANIA", 3},
	{"SAUDI ARABIA", 4}, {"VIETNAM", 2}, {"RUSSIA", 3}, {"UNITED KINGDOM", 3}, {"UNITED STATES", 1},
}

var (
	scaleSegments     = []string{"AUTOMOBILE", "BUILDING", "FURNITURE", "HOUSEHOLD", "MACHINERY"}
	scalePriorities   = []string{"1-URGENT", "2-HIGH", "3-MEDIUM", "4-NOT SPECIFIED", "5-LOW"}
	scaleOrderStatus  = []string{"O", "F", "P"}
	scaleShipModes    = []string{"AIR", "AIR REG", "RAIL", "SHIP", "TRUCK", "MAIL", "FOB"}
	scaleShipInstruct = []string{"DELIVER IN PERSON", "COLLECT COD", "NONE", "TAKE BACK RETURN"}
	scaleReturnFlags  = []string{"R", "A", "N"}
	scaleLineStatus   = []string{"O", "F"}
	scaleContainerS1  = []string{"SM", "LG", "MED", "JUMBO", "WRAP"}
	scaleContainerS2  = []string{"CASE", "BOX", "BAG", "JAR", "PKG", "PACK", "CAN", "DRUM"}
	scaleTypeS1       = []string{"STANDARD", "SMALL", "MEDIUM", "LARGE", "ECONOMY", "PROMO"}
	scaleTypeS2       = []string{"ANODIZED", "BURNISHED", "PLATED", "POLISHED", "BRUSHED"}
	scaleTypeS3       = []string{"TIN", "NICKEL", "BRASS", "STEEL", "COPPER"}
	scaleColors       = []string{"green", "forest", "spring", "summer", "red", "blue", "navy",
		"sky", "white", "black", "almond", "antique", "aquamarine", "azure", "beige"}
)

// ---------------------------------------------------------------------------
// Loader
// ---------------------------------------------------------------------------

type scaleLoader struct {
	db   *sql.DB
	conn *sql.Conn
	rng  *rand.Rand
	t    *testing.T

	suppliers int
	customers int
	parts     int
	orders    int
}

func (l *scaleLoader) loadAll(ctx context.Context) error {
	// All load statements (BEGIN / multi-row INSERT / COMMIT) run on a
	// single dedicated connection. We drive transactions with plain
	// BEGIN/COMMIT statements rather than db.BeginTx: goopg reports the
	// post-BEGIN ReadyForQuery status as 'I', which trips lib/pq's
	// BeginTx status assertion ("unexpected transaction status idle").
	// This mirrors the hammerdb_load loader's approach.
	conn, err := l.db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	l.conn = conn

	steps := []struct {
		name string
		fn   func(context.Context) error
	}{
		{"region", l.loadRegion},
		{"nation", l.loadNation},
		{"supplier", l.loadSupplier},
		{"customer", l.loadCustomer},
		{"part", l.loadPart},
		{"partsupp", l.loadPartsupp},
		{"orders", l.loadOrders},
		{"lineitem", l.loadLineitem},
	}
	for _, s := range steps {
		start := time.Now()
		if err := s.fn(ctx); err != nil {
			return fmt.Errorf("%s: %w", s.name, err)
		}
		l.t.Logf("  loaded %-9s in %s", s.name, time.Since(start).Round(time.Second))
	}
	return nil
}

func (l *scaleLoader) loadRegion(ctx context.Context) error {
	w, err := newBatchWriter(ctx, l.conn, "region", "r_regionkey, r_name, r_comment")
	if err != nil {
		return err
	}
	for i := 0; i < len(scaleRegions); i++ {
		i := i
		if err := w.add(func(b *strings.Builder) {
			fmt.Fprintf(b, "(%d, %s, %s)", i, scaleLit(scaleRegions[i]), scaleLit("region comment"))
		}); err != nil {
			return err
		}
	}
	return w.close()
}

func (l *scaleLoader) loadNation(ctx context.Context) error {
	w, err := newBatchWriter(ctx, l.conn, "nation", "n_nationkey, n_name, n_regionkey, n_comment")
	if err != nil {
		return err
	}
	for i := 0; i < len(scaleNations); i++ {
		i := i
		if err := w.add(func(b *strings.Builder) {
			fmt.Fprintf(b, "(%d, %s, %d, %s)", i, scaleLit(scaleNations[i].name),
				scaleNations[i].regionkey, scaleLit("nation comment"))
		}); err != nil {
			return err
		}
	}
	return w.close()
}

func (l *scaleLoader) loadSupplier(ctx context.Context) error {
	w, err := newBatchWriter(ctx, l.conn, "supplier",
		"s_suppkey, s_nationkey, s_comment, s_name, s_address, s_phone, s_acctbal")
	if err != nil {
		return err
	}
	for i := 0; i < l.suppliers; i++ {
		sk := i + 1
		nk := l.rng.Intn(len(scaleNations))
		comment := "reliable supplier"
		// ~5% carry the "Customer ... Complaints" marker Q16 filters on.
		if l.rng.Intn(20) == 0 {
			comment = "Customer Complaints filed against this supplier"
		}
		phone := scalePhone(l.rng, nk)
		bal := scaleMoney(l.rng, -99999, 999999)
		if err := w.add(func(b *strings.Builder) {
			fmt.Fprintf(b, "(%d, %d, %s, %s, %s, %s, %s)",
				sk, nk, scaleLit(comment), scaleLit(fmt.Sprintf("Supplier#%09d", sk)),
				scaleLit("address"), scaleLit(phone), bal)
		}); err != nil {
			return err
		}
	}
	return w.close()
}

func (l *scaleLoader) loadCustomer(ctx context.Context) error {
	w, err := newBatchWriter(ctx, l.conn, "customer",
		"c_custkey, c_mktsegment, c_nationkey, c_name, c_address, c_phone, c_acctbal, c_comment")
	if err != nil {
		return err
	}
	for i := 0; i < l.customers; i++ {
		ck := i + 1
		seg := scaleSegments[l.rng.Intn(len(scaleSegments))]
		nk := l.rng.Intn(len(scaleNations))
		phone := scalePhone(l.rng, nk)
		bal := scaleMoney(l.rng, -99999, 999999)
		if err := w.add(func(b *strings.Builder) {
			fmt.Fprintf(b, "(%d, %s, %d, %s, %s, %s, %s, %s)",
				ck, scaleLit(seg), nk, scaleLit(fmt.Sprintf("Customer#%09d", ck)),
				scaleLit("address"), scaleLit(phone), bal, scaleLit("customer comment"))
		}); err != nil {
			return err
		}
	}
	return w.close()
}

func (l *scaleLoader) loadPart(ctx context.Context) error {
	w, err := newBatchWriter(ctx, l.conn, "part",
		"p_partkey, p_type, p_size, p_brand, p_name, p_container, p_mfgr, p_retailprice, p_comment")
	if err != nil {
		return err
	}
	for i := 0; i < l.parts; i++ {
		pk := i + 1
		typ := scaleTypeS1[l.rng.Intn(len(scaleTypeS1))] + " " +
			scaleTypeS2[l.rng.Intn(len(scaleTypeS2))] + " " +
			scaleTypeS3[l.rng.Intn(len(scaleTypeS3))]
		size := 1 + l.rng.Intn(50)
		m := 1 + l.rng.Intn(5)
		n := 1 + l.rng.Intn(5)
		brand := fmt.Sprintf("Brand#%d%d", m, n)
		name := scaleColors[l.rng.Intn(len(scaleColors))] + " " +
			scaleColors[l.rng.Intn(len(scaleColors))]
		container := scaleContainerS1[l.rng.Intn(len(scaleContainerS1))] + " " +
			scaleContainerS2[l.rng.Intn(len(scaleContainerS2))]
		mfgr := fmt.Sprintf("Manufacturer#%d", m)
		price := scaleMoney(l.rng, 90000, 200000)
		if err := w.add(func(b *strings.Builder) {
			fmt.Fprintf(b, "(%d, %s, %d, %s, %s, %s, %s, %s, %s)",
				pk, scaleLit(typ), size, scaleLit(brand), scaleLit(name),
				scaleLit(container), scaleLit(mfgr), price, scaleLit("part comment"))
		}); err != nil {
			return err
		}
	}
	return w.close()
}

func (l *scaleLoader) loadPartsupp(ctx context.Context) error {
	w, err := newBatchWriter(ctx, l.conn, "partsupp",
		"ps_partkey, ps_suppkey, ps_supplycost, ps_availqty, ps_comment")
	if err != nil {
		return err
	}
	total := l.parts * 4
	for i := 0; i < total; i++ {
		partkey := i/4 + 1
		j := i % 4
		sk := scaleSuppForPart(partkey, j, l.suppliers)
		cost := scaleMoney(l.rng, 100, 100000)
		avail := 1 + l.rng.Intn(9999)
		if err := w.add(func(b *strings.Builder) {
			fmt.Fprintf(b, "(%d, %d, %s, %d, %s)", partkey, sk, cost, avail, scaleLit("ps comment"))
		}); err != nil {
			return err
		}
	}
	return w.close()
}

func (l *scaleLoader) loadOrders(ctx context.Context) error {
	w, err := newBatchWriter(ctx, l.conn, "orders",
		"o_orderdate, o_orderkey, o_custkey, o_orderpriority, o_shippriority, o_clerk, o_orderstatus, o_totalprice, o_comment")
	if err != nil {
		return err
	}
	for i := 0; i < l.orders; i++ {
		ok := i + 1
		od := scaleDate(l.rng)
		ck := 1 + l.rng.Intn(l.customers)
		prio := scalePriorities[l.rng.Intn(len(scalePriorities))]
		clerk := fmt.Sprintf("Clerk#%09d", 1+l.rng.Intn(1000))
		status := scaleOrderStatus[l.rng.Intn(len(scaleOrderStatus))]
		price := scaleMoney(l.rng, 10000, 50000000)
		comment := "order comment"
		// ~10% carry the "special requests" phrase Q13 excludes via NOT LIKE.
		if l.rng.Intn(10) == 0 {
			comment = "handle special requests carefully"
		}
		if err := w.add(func(b *strings.Builder) {
			fmt.Fprintf(b, "(%s, %d, %d, %s, 0, %s, %s, %s, %s)",
				scaleTS(od), ok, ck, scaleLit(prio), scaleLit(clerk),
				scaleLit(status), price, scaleLit(comment))
		}); err != nil {
			return err
		}
	}
	return w.close()
}

func (l *scaleLoader) loadLineitem(ctx context.Context) error {
	w, err := newBatchWriter(ctx, l.conn, "lineitem",
		"l_shipdate, l_orderkey, l_discount, l_extendedprice, l_suppkey, l_quantity, l_returnflag, "+
			"l_partkey, l_linestatus, l_tax, l_commitdate, l_receiptdate, l_shipmode, l_linenumber, "+
			"l_shipinstruct, l_comment")
	if err != nil {
		return err
	}
	for ok := 1; ok <= l.orders; ok++ {
		od := scaleDate(l.rng)
		nlines := 1 + l.rng.Intn(7)
		for ln := 1; ln <= nlines; ln++ {
			partkey := 1 + l.rng.Intn(l.parts)
			suppkey := scaleSuppForPart(partkey, l.rng.Intn(4), l.suppliers)
			qty := 1 + l.rng.Intn(50)
			disc := scaleFraction(l.rng, 10) // 0.00..0.10
			tax := scaleFraction(l.rng, 8)   // 0.00..0.08
			price := scaleMoney(l.rng, 10000, 10000000)
			ship := od.Add(time.Duration(1+l.rng.Intn(121)) * 24 * time.Hour)
			commit := od.Add(time.Duration(30+l.rng.Intn(61)) * 24 * time.Hour)
			receipt := ship.Add(time.Duration(1+l.rng.Intn(30)) * 24 * time.Hour)
			rflag := scaleReturnFlags[l.rng.Intn(len(scaleReturnFlags))]
			lstatus := scaleLineStatus[l.rng.Intn(len(scaleLineStatus))]
			mode := scaleShipModes[l.rng.Intn(len(scaleShipModes))]
			instr := scaleShipInstruct[l.rng.Intn(len(scaleShipInstruct))]
			ln := ln
			if err := w.add(func(b *strings.Builder) {
				fmt.Fprintf(b, "(%s, %d, %s, %s, %d, %d, %s, %d, %s, %s, %s, %s, %s, %d, %s, %s)",
					scaleTS(ship), ok, disc, price, suppkey, qty, scaleLit(rflag),
					partkey, scaleLit(lstatus), tax, scaleTS(commit), scaleTS(receipt),
					scaleLit(mode), ln, scaleLit(instr), scaleLit("line comment"))
			}); err != nil {
				return err
			}
		}
	}
	return w.close()
}

// scaleSuppForPart returns the j-th (0..3) supplier key for a part,
// deterministically. partsupp is generated with the same function, so
// every lineitem (l_partkey, l_suppkey) pair it produces is guaranteed
// to exist in partsupp — which lets the partsupp-joining queries
// (Q9 / Q2 / Q11 / Q16 / Q20) connect real rows.
func scaleSuppForPart(partkey, j, suppliers int) int {
	return ((partkey - 1 + j*(suppliers/4+1)) % suppliers) + 1
}

// ---------------------------------------------------------------------------
// Batched multi-row INSERT writer
// ---------------------------------------------------------------------------

// batchWriter accumulates rows into `INSERT INTO t (...) VALUES (...),(...)`
// statements (batchRows per statement) and commits every rowsPerTx rows,
// matching the cadence HammerDB's loader uses. This keeps the load fast
// without holding one giant transaction open over ~10 M rows.
//
// Transactions are driven with plain BEGIN/COMMIT statements on a single
// dedicated *sql.Conn rather than db.BeginTx — see scaleLoader.loadAll
// for why (lib/pq's BeginTx status assertion is incompatible with how
// goopg reports the post-BEGIN ReadyForQuery status).
type batchWriter struct {
	ctx    context.Context
	conn   *sql.Conn
	prefix string

	b        strings.Builder
	inBatch  int
	rowsInTx int

	batchRows int
	rowsPerTx int
}

func newBatchWriter(ctx context.Context, conn *sql.Conn, table, cols string) (*batchWriter, error) {
	if _, err := conn.ExecContext(ctx, "BEGIN"); err != nil {
		return nil, err
	}
	return &batchWriter{
		ctx:       ctx,
		conn:      conn,
		prefix:    "INSERT INTO " + table + " (" + cols + ") VALUES ",
		batchRows: 400,
		rowsPerTx: 40000,
	}, nil
}

func (w *batchWriter) add(gen func(b *strings.Builder)) error {
	if w.inBatch == 0 {
		w.b.WriteString(w.prefix)
	} else {
		w.b.WriteByte(',')
	}
	gen(&w.b)
	w.inBatch++
	w.rowsInTx++

	if w.inBatch >= w.batchRows {
		if err := w.flush(); err != nil {
			return err
		}
	}
	if w.rowsInTx >= w.rowsPerTx {
		if err := w.flush(); err != nil {
			return err
		}
		if _, err := w.conn.ExecContext(w.ctx, "COMMIT"); err != nil {
			return err
		}
		if _, err := w.conn.ExecContext(w.ctx, "BEGIN"); err != nil {
			return err
		}
		w.rowsInTx = 0
	}
	return nil
}

func (w *batchWriter) flush() error {
	if w.inBatch == 0 {
		return nil
	}
	if _, err := w.conn.ExecContext(w.ctx, w.b.String()); err != nil {
		_, _ = w.conn.ExecContext(w.ctx, "ROLLBACK")
		return err
	}
	w.b.Reset()
	w.inBatch = 0
	return nil
}

func (w *batchWriter) close() error {
	if err := w.flush(); err != nil {
		return err
	}
	_, err := w.conn.ExecContext(w.ctx, "COMMIT")
	return err
}

// ---------------------------------------------------------------------------
// SQL-literal value helpers
// ---------------------------------------------------------------------------

func scaleLit(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

func scaleTS(t time.Time) string {
	return "timestamp '" + t.Format("2006-01-02 15:04:05") + "'"
}

// scaleMoney returns a NUMERIC text literal for a random amount in the
// [minCents, maxCents] cent range (e.g. "1234.56" or "-12.30").
func scaleMoney(rng *rand.Rand, minCents, maxCents int) string {
	cents := minCents
	if maxCents > minCents {
		cents += rng.Intn(maxCents - minCents + 1)
	}
	neg := ""
	if cents < 0 {
		neg = "-"
		cents = -cents
	}
	return fmt.Sprintf("%s%d.%02d", neg, cents/100, cents%100)
}

// scaleFraction returns "0.NN" with NN in [0, maxHundredths].
func scaleFraction(rng *rand.Rand, maxHundredths int) string {
	return fmt.Sprintf("0.%02d", rng.Intn(maxHundredths+1))
}

// scalePhone formats a TPC-H phone whose first two digits encode the
// nation (country code = nationkey + 10), so substr(c_phone,1,2)
// yields '10'..'34' — the shape Q22's cntrycode filter relies on.
func scalePhone(rng *rand.Rand, nationkey int) string {
	cc := nationkey + 10
	return fmt.Sprintf("%02d-%03d-%03d-%04d", cc, 100+rng.Intn(900), 100+rng.Intn(900), 1000+rng.Intn(9000))
}

// scaleDate returns a random day in TPC-H's 1992-01-01..1998-08-02 window.
func scaleDate(rng *rand.Rand) time.Time {
	start := time.Date(1992, 1, 1, 0, 0, 0, 0, time.UTC)
	return start.Add(time.Duration(rng.Intn(2403)) * 24 * time.Hour)
}
