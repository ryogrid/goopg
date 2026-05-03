package main

import (
	"context"
	"database/sql"
	"fmt"
	"math/rand"
	"strings"
	"time"
)

// order is the tuple shape goopg's tpch.tableDefs() declares for
// the orders relation, in the same column order. See
// internal/testutil/tpch/tpch.go::tableDefs().
type order struct {
	orderdate     string // YYYY-MM-DD HH:MM:SS
	orderkey      int
	custkey       int
	orderpriority string // char(15)
	shippriority  int
	clerk         string // char(15)
	orderstatus   string // char(1)
	totalprice    string // numeric — formatted text
	comment       string // varchar(79)
}

// lineitem is the matching shape for the lineitem relation.
type lineitem struct {
	shipdate      string
	orderkey      int
	discount      string // numeric
	extendedprice string
	suppkey       int
	quantity      int
	returnflag    string // char(1)
	partkey       int
	linestatus    string // char(1)
	tax           string
	commitdate    string
	receiptdate   string
	shipmode      string // char(10)
	linenumber    int
	shipinstruct  string // char(25)
	comment       string // varchar(44)
}

// genOrder builds one synthetic order. Values are random within
// type-stable ranges; we don't try to match dbgen's exact
// distributions because the test target is INSERT throughput, not
// query results.
func (l *loader) genOrder(okey int) order {
	priorities := []string{"1-URGENT", "2-HIGH", "3-MEDIUM", "4-NOT SPECIFIED", "5-LOW"}
	statuses := []string{"O", "F", "P"}
	d := time.Date(1992, 1, 1, 0, 0, 0, 0, time.UTC).
		Add(time.Duration(l.rng.Intn(2557)) * 24 * time.Hour) // ~7 years
	custkey := 1 + l.rng.Intn(customersPerSF*l.scale)
	if custkey > customersPerSF*l.scale {
		custkey = customersPerSF * l.scale
	}
	totalCents := 100_00 + l.rng.Intn(50_000_00)
	priceText := fmt.Sprintf("%d.%02d", totalCents/100, totalCents%100)
	return order{
		orderdate:     d.Format("2006-01-02 15:04:05"),
		orderkey:      okey,
		custkey:       custkey,
		orderpriority: priorities[l.rng.Intn(len(priorities))],
		shippriority:  0,
		clerk:         fmt.Sprintf("Clerk#%09d", 1+l.rng.Intn(1000)),
		orderstatus:   statuses[l.rng.Intn(len(statuses))],
		totalprice:    priceText,
		comment:       randText(l.rng, 5, 79),
	}
}

func (l *loader) genLineitem(okey, lnum int) lineitem {
	flags := []string{"R", "A", "N"}
	statuses := []string{"O", "F"}
	modes := []string{"AIR", "AIR REG", "RAIL", "SHIP", "TRUCK", "MAIL", "FOB"}
	instructs := []string{"DELIVER IN PERSON", "COLLECT COD", "NONE", "TAKE BACK RETURN"}
	ship := time.Date(1992, 1, 1, 0, 0, 0, 0, time.UTC).
		Add(time.Duration(l.rng.Intn(2557)) * 24 * time.Hour)
	commit := ship.Add(time.Duration(l.rng.Intn(120)) * 24 * time.Hour)
	receipt := ship.Add(time.Duration(7+l.rng.Intn(30)) * 24 * time.Hour)
	priceCents := 100_00 + l.rng.Intn(99_900_00)
	return lineitem{
		shipdate:      ship.Format("2006-01-02 15:04:05"),
		orderkey:      okey,
		discount:      twoDigitFraction(l.rng),
		extendedprice: fmt.Sprintf("%d.%02d", priceCents/100, priceCents%100),
		suppkey:       1 + l.rng.Intn(suppliersPerSF*l.scale),
		quantity:      1 + l.rng.Intn(50),
		returnflag:    flags[l.rng.Intn(len(flags))],
		partkey:       1 + l.rng.Intn(partsPerSF*l.scale),
		linestatus:    statuses[l.rng.Intn(len(statuses))],
		tax:           twoDigitFraction(l.rng),
		commitdate:    commit.Format("2006-01-02 15:04:05"),
		receiptdate:   receipt.Format("2006-01-02 15:04:05"),
		shipmode:      modes[l.rng.Intn(len(modes))],
		linenumber:    lnum,
		shipinstruct:  instructs[l.rng.Intn(len(instructs))],
		comment:       randText(l.rng, 5, 44),
	}
}

func twoDigitFraction(rng *rand.Rand) string {
	// 0.00..0.10 — same range dbgen uses for l_discount / l_tax.
	v := rng.Intn(11)
	return fmt.Sprintf("0.%02d", v)
}

const lowerLetters = "abcdefghijklmnopqrstuvwxyz"

func randText(rng *rand.Rand, minLen, maxLen int) string {
	if maxLen <= 0 {
		return ""
	}
	n := minLen + rng.Intn(max(1, maxLen-minLen))
	if n > maxLen {
		n = maxLen
	}
	var b strings.Builder
	for i := 0; i < n; i++ {
		if i > 0 && rng.Intn(6) == 0 {
			b.WriteByte(' ')
			continue
		}
		b.WriteByte(lowerLetters[rng.Intn(len(lowerLetters))])
	}
	return b.String()
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// createSchema mirrors HammerDB's CreateTables for the two large
// tables we exercise. DDL matches goopg's existing TPC-H test
// catalog (see internal/testutil/tpch/tpch.go::tableDefs()) so a
// HammerDB-compatible run lands in a parity-test-friendly shape.
func createSchema(ctx context.Context, db *sql.DB) error {
	stmts := []string{
		`DROP TABLE IF EXISTS lineitem`,
		`DROP TABLE IF EXISTS orders`,
		`CREATE TABLE orders (
			o_orderdate timestamp,
			o_orderkey numeric NOT NULL,
			o_custkey numeric NOT NULL,
			o_orderpriority char(15),
			o_shippriority numeric,
			o_clerk char(15),
			o_orderstatus char(1),
			o_totalprice numeric,
			o_comment varchar(79)
		)`,
		`CREATE TABLE lineitem (
			l_shipdate timestamp,
			l_orderkey numeric NOT NULL,
			l_discount numeric NOT NULL,
			l_extendedprice numeric NOT NULL,
			l_suppkey numeric NOT NULL,
			l_quantity numeric NOT NULL,
			l_returnflag char(1),
			l_partkey numeric NOT NULL,
			l_linestatus char(1),
			l_tax numeric NOT NULL,
			l_commitdate timestamp,
			l_receiptdate timestamp,
			l_shipmode char(10),
			l_linenumber numeric NOT NULL,
			l_shipinstruct char(25),
			l_comment varchar(44)
		)`,
	}
	for _, s := range stmts {
		if _, err := db.ExecContext(ctx, s); err != nil {
			return fmt.Errorf("DDL %q: %w", firstWords(s), err)
		}
	}
	return nil
}

func firstWords(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.Index(s, "\n"); i >= 0 {
		s = s[:i]
	}
	if len(s) > 60 {
		s = s[:60] + "…"
	}
	return s
}
