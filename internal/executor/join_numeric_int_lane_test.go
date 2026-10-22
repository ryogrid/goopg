package executor

import (
	"math/big"
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/optimizer"
	"github.com/goopg/goopg/internal/parser"
)

// TestBigNumericInt64KeyIsExact pins datumToInt64Key's big lane. A numeric
// join key now starts in the int64 lane (HashKeysTakeInt64Lane), whose probe
// treats "not int64-representable" as "matches nothing", so every integral
// in-range value must map, whatever its scale.
func TestBigNumericInt64KeyIsExact(t *testing.T) {
	ten := func(e int) *big.Int { return new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(e)), nil) }
	for _, c := range []struct {
		name  string
		m     *big.Int
		scale int
		want  int64
		ok    bool
	}{
		{"1 at scale 22", ten(22), 22, 1, true},
		{"-7 at scale 20", new(big.Int).Mul(big.NewInt(-7), ten(20)), 20, -7, true},
		{"0 at scale 30", big.NewInt(0), 30, 0, true},
		{"1.5 at scale 21", new(big.Int).Mul(big.NewInt(15), ten(20)), 21, 0, false},
		{"past int64", new(big.Int).Mul(big.NewInt(12345), ten(20)), 0, 0, false},
		{"past int64 after stripping", new(big.Int).Mul(big.NewInt(12345), ten(30)), 5, 0, false},
	} {
		got, ok := bigNumericInt64Key(new(big.Int).Set(c.m), c.scale)
		if ok != c.ok || (ok && got != c.want) {
			t.Errorf("%s: got %d,%v want %d,%v", c.name, got, ok, c.want, c.ok)
		}
	}
}

// TestNumericHashJoinIntLane runs numeric-keyed hash joins, which now start in
// the int64 lane, against the same queries with hash joins disabled. nk holds
// integral keys spelled at several scales (one past int64's mantissa), a
// fraction that demotes the build, negatives, zero and NULL; ik is int4. Every
// result must equal the non-hash plan's.
func TestNumericHashJoinIntLane(t *testing.T) {
	ctx := deformW8Fixture(t)
	runSQL(t, ctx, "CREATE TABLE nk (k numeric, tag text)")
	runSQL(t, ctx, "INSERT INTO nk SELECT x % 40 - 5, 'n' || x FROM generate_series(1, 400) x")
	runSQL(t, ctx, "INSERT INTO nk VALUES (1.0000000000000000000000, 'big-one'), (2.000, 'two-scaled'), (NULL, 'null'), (0.0, 'zero-scaled'), (-3.00, 'neg-scaled')")
	runSQL(t, ctx, "CREATE TABLE nf (k numeric, tag text)")
	runSQL(t, ctx, "INSERT INTO nf SELECT x, 'f' || x FROM generate_series(-5, 50) x")
	runSQL(t, ctx, "INSERT INTO nf VALUES (2.5, 'frac'), (1.00000000000000000000000000, 'f-big-one'), (NULL, 'f-null')")
	runSQL(t, ctx, "CREATE TABLE ik (k int4, tag text)")
	runSQL(t, ctx, "INSERT INTO ik SELECT x, 'i' || x FROM generate_series(-10, 30) x")
	for _, tb := range []string{"nk", "nf", "ik"} {
		runSQL(t, ctx, "ANALYZE "+tb)
	}
	noHash := optimizer.DefaultPlannerSettings()
	noHash.EnableHashJoin = false
	for _, q := range []string{
		"SELECT nk.tag, nf.tag FROM nk JOIN nf ON nk.k = nf.k",
		"SELECT nk.tag, nf.tag FROM nf JOIN nk ON nf.k = nk.k",
		"SELECT nk.tag, nf.tag FROM nk LEFT JOIN nf ON nk.k = nf.k",
		"SELECT nk.tag, nf.tag FROM nf LEFT JOIN nk ON nk.k = nf.k",
		"SELECT nk.tag, ik.tag FROM nk JOIN ik ON nk.k = ik.k",
		"SELECT nk.tag, ik.tag FROM ik LEFT JOIN nk ON nk.k = ik.k",
		"SELECT nk.tag FROM nk WHERE EXISTS (SELECT 1 FROM nf WHERE nf.k = nk.k)",
		"SELECT nk.tag FROM nk WHERE NOT EXISTS (SELECT 1 FROM nf WHERE nf.k = nk.k)",
	} {
		stmts, err := parser.Parse(q)
		if err != nil {
			t.Fatal(err)
		}
		plan, err := optimizer.PlanWithSettings(stmts[0], ctx.Catalog, optimizer.DefaultPlannerSettings())
		if err != nil {
			t.Fatal(err)
		}
		joins := hashJoins(mustBuildDeform(t, plan))
		if len(joins) != 1 || !joins[0].plan.HashKeysTakeInt64Lane() {
			t.Fatalf("%s: want one hash join starting in the int64 lane, got %d", q, len(joins))
		}
		got := sortedCells(runSQLWith(t, ctx, q, optimizer.DefaultPlannerSettings()))
		want := sortedCells(runSQLWith(t, ctx, q, noHash))
		if strings.Join(got, "|") != strings.Join(want, "|") {
			t.Fatalf("%s: hash join %d rows, non-hash plan %d rows\nhash  %v\nother %v", q, len(got), len(want), got, want)
		}
		if len(want) == 0 {
			t.Fatalf("%s: no rows", q)
		}
	}
}
