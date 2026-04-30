package executor

import (
	"testing"
	"time"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/planner"
)

func TestUDFSimpleReturn(t *testing.T) {
	cat := catalog.NewInMemory()
	cat.Routines().Create(&catalog.Routine{
		Name:       "answer",
		ReturnType: catalog.Type{Name: "integer"},
		Language:   "plpgsql",
		Body:       "BEGIN RETURN 42; END",
	}, false)

	ctx := NewContext()
	ctx.Catalog = cat
	ctx.Now = time.Now()

	// SELECT answer()
	expr := &planner.FuncCall{Name: "answer"}
	res, err := evalExpr(expr, nil, ctx)
	if err != nil {
		t.Fatalf("evalExpr: %v", err)
	}
	if res.Kind != KindInt || res.Int != 42 {
		t.Errorf("res = %+v, want 42", res)
	}
}

func TestUDFWithArgsAndVars(t *testing.T) {
	cat := catalog.NewInMemory()
	cat.Routines().Create(&catalog.Routine{
		Name:       "add_one",
		ArgNames:   []string{"val"},
		ArgTypes:   []catalog.Type{{Name: "integer"}},
		ReturnType: catalog.Type{Name: "integer"},
		Language:   "plpgsql",
		Body: `DECLARE
			extra int := 1;
		BEGIN
			RETURN val + extra;
		END`,
	}, false)

	ctx := NewContext()
	ctx.Catalog = cat
	ctx.Now = time.Now()

	// SELECT add_one(10)
	expr := &planner.FuncCall{
		Name: "add_one",
		Args: []planner.Expr{&planner.IntegerConst{Value: 10}},
	}
	res, err := evalExpr(expr, nil, ctx)
	if err != nil {
		t.Fatalf("evalExpr: %v", err)
	}
	if res.Kind != KindInt || res.Int != 11 {
		t.Errorf("res = %+v, want 11", res)
	}
}

func TestUDFIfStmt(t *testing.T) {
	cat := catalog.NewInMemory()
	cat.Routines().Create(&catalog.Routine{
		Name:       "is_positive",
		ArgNames:   []string{"val"},
		ArgTypes:   []catalog.Type{{Name: "integer"}},
		ReturnType: catalog.Type{Name: "text"},
		Language:   "plpgsql",
		Body: `BEGIN
			IF val > 0 THEN
				RETURN 'yes';
			ELSIF val < 0 THEN
				RETURN 'no';
			ELSE
				RETURN 'zero';
			END IF;
		END`,
	}, false)

	ctx := NewContext()
	ctx.Catalog = cat
	ctx.Now = time.Now()

	cases := []struct {
		input int64
		want  string
	}{
		{10, "yes"},
		{-5, "no"},
		{0, "zero"},
	}

	for _, tc := range cases {
		expr := &planner.FuncCall{
			Name: "is_positive",
			Args: []planner.Expr{&planner.IntegerConst{Value: tc.input}},
		}
		res, err := evalExpr(expr, nil, ctx)
		if err != nil {
			t.Errorf("input %d: evalExpr: %v", tc.input, err)
			continue
		}
		if res.Kind != KindString || res.String != tc.want {
			t.Errorf("input %d: res = %v, want %q", tc.input, res.String, tc.want)
		}
	}
}

func TestUDFLoopExit(t *testing.T) {
	cat := catalog.NewInMemory()
	cat.Routines().Create(&catalog.Routine{
		Name:       "sum_to",
		ArgNames:   []string{"n"},
		ArgTypes:   []catalog.Type{{Name: "integer"}},
		ReturnType: catalog.Type{Name: "integer"},
		Language:   "plpgsql",
		Body: `DECLARE
			s int := 0;
			i int := 1;
		BEGIN
			LOOP
				IF i > n THEN
					EXIT;
				END IF;
				s := s + i;
				i := i + 1;
			END LOOP;
			RETURN s;
		END`,
	}, false)

	ctx := NewContext()
	ctx.Catalog = cat
	ctx.Now = time.Now()

	expr := &planner.FuncCall{
		Name: "sum_to",
		Args: []planner.Expr{&planner.IntegerConst{Value: 10}},
	}
	res, err := evalExpr(expr, nil, ctx)
	if err != nil {
		t.Fatalf("evalExpr: %v", err)
	}
	if res.Kind != KindInt || res.Int != 55 {
		t.Errorf("res = %+v, want 55", res)
	}
}

func TestUDFWhile(t *testing.T) {
	cat := catalog.NewInMemory()
	cat.Routines().Create(&catalog.Routine{
		Name:       "factorial",
		ArgNames:   []string{"n"},
		ArgTypes:   []catalog.Type{{Name: "integer"}},
		ReturnType: catalog.Type{Name: "integer"},
		Language:   "plpgsql",
		Body: `DECLARE
			res int := 1;
			i int := 1;
		BEGIN
			WHILE i <= n LOOP
				res := res * i;
				i := i + 1;
			END LOOP;
			RETURN res;
		END`,
	}, false)

	ctx := NewContext()
	ctx.Catalog = cat
	ctx.Now = time.Now()

	expr := &planner.FuncCall{
		Name: "factorial",
		Args: []planner.Expr{&planner.IntegerConst{Value: 5}},
	}
	res, err := evalExpr(expr, nil, ctx)
	if err != nil {
		t.Fatalf("evalExpr: %v", err)
	}
	if res.Kind != KindInt || res.Int != 120 {
		t.Errorf("res = %+v, want 120", res)
	}
}

func TestUDFContinue(t *testing.T) {
	cat := catalog.NewInMemory()
	cat.Routines().Create(&catalog.Routine{
		Name:       "sum_odd",
		ArgNames:   []string{"n"},
		ArgTypes:   []catalog.Type{{Name: "integer"}},
		ReturnType: catalog.Type{Name: "integer"},
		Language:   "plpgsql",
		Body: `DECLARE
			s int := 0;
			i int := 0;
		BEGIN
			LOOP
				i := i + 1;
				IF i > n THEN
					EXIT;
				END IF;
				IF i % 2 = 0 THEN
					CONTINUE;
				END IF;
				s := s + i;
			END LOOP;
			RETURN s;
		END`,
	}, false)

	ctx := NewContext()
	ctx.Catalog = cat
	ctx.Now = time.Now()

	expr := &planner.FuncCall{
		Name: "sum_odd",
		Args: []planner.Expr{&planner.IntegerConst{Value: 10}},
	}
	res, err := evalExpr(expr, nil, ctx)
	if err != nil {
		t.Fatalf("evalExpr: %v", err)
	}
	if res.Kind != KindInt || res.Int != 25 { // 1+3+5+7+9 = 25
		t.Errorf("res = %+v, want 25", res)
	}
}

func TestUDFFor(t *testing.T) {
	cat := catalog.NewInMemory()
	cat.Routines().Create(&catalog.Routine{
		Name:       "sum_for",
		ArgNames:   []string{"n"},
		ArgTypes:   []catalog.Type{{Name: "integer"}},
		ReturnType: catalog.Type{Name: "integer"},
		Language:   "plpgsql",
		Body: `DECLARE
			s int := 0;
		BEGIN
			FOR i IN 1..n LOOP
				s := s + i;
			END LOOP;
			RETURN s;
		END`,
	}, false)

	ctx := NewContext()
	ctx.Catalog = cat
	ctx.Now = time.Now()

	expr := &planner.FuncCall{
		Name: "sum_for",
		Args: []planner.Expr{&planner.IntegerConst{Value: 10}},
	}
	res, err := evalExpr(expr, nil, ctx)
	if err != nil {
		t.Fatalf("evalExpr: %v", err)
	}
	if res.Kind != KindInt || res.Int != 55 {
		t.Errorf("res = %+v, want 55", res)
	}
}

func TestUDFForReverse(t *testing.T) {
	cat := catalog.NewInMemory()
	cat.Routines().Create(&catalog.Routine{
		Name:       "countdown",
		ArgNames:   []string{"n"},
		ArgTypes:   []catalog.Type{{Name: "integer"}},
		ReturnType: catalog.Type{Name: "integer"},
		Language:   "plpgsql",
		Body: `DECLARE
			s int := 0;
		BEGIN
			FOR i IN REVERSE n..1 BY 2 LOOP
				s := s + i;
			END LOOP;
			RETURN s;
		END`,
	}, false)

	ctx := NewContext()
	ctx.Catalog = cat
	ctx.Now = time.Now()

	expr := &planner.FuncCall{
		Name: "countdown",
		Args: []planner.Expr{&planner.IntegerConst{Value: 10}},
	}
	res, err := evalExpr(expr, nil, ctx)
	if err != nil {
		t.Fatalf("evalExpr: %v", err)
	}
	// 10 + 8 + 6 + 4 + 2 = 30
	if res.Kind != KindInt || res.Int != 30 {
		t.Errorf("res = %+v, want 30", res)
	}
}
