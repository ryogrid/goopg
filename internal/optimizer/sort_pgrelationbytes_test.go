package optimizer

import (
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/executor/hashsize"
)

func TestPGRelationByteSizeAlignmentAndInvalidWidth(t *testing.T) {
	for _, tc := range []struct {
		width int
		want  float64
	}{
		{1, 32}, {8, 32}, {9, 40}, {16, 40}, {17, 48},
	} {
		got, ok := pgRelationByteSize(3, tc.width)
		if !ok || got != 3*tc.want {
			t.Fatalf("width %d: bytes = %v, %v; want %v, true", tc.width, got, ok, 3*tc.want)
		}
	}
	for _, tc := range []struct {
		rows  float64
		width int
	}{{-1, 8}, {1, 0}, {1, -1}, {1, math.MaxInt}} {
		if _, ok := pgRelationByteSize(tc.rows, tc.width); ok {
			t.Fatalf("pgRelationByteSize(%v, %d) unexpectedly valid", tc.rows, tc.width)
		}
	}
}

func TestCostSortRunPGRelationBytesSwitchOffIsLegacy(t *testing.T) {
	restore := setPGSortRelationBytesCostForTest(false)
	defer restore()
	cp := defaultCostParams()
	for _, tc := range []struct {
		rows, avg, limit float64
		ncols, width      int
	}{
		{0, 0, -1, 4, 8}, {1, 4, -1, 4, 9}, {1000, 32, -1, 8, 64},
		{1e7, 500, 100, 24, 700},
	} {
		legacy := costSortRun(cp, tc.rows, tc.ncols, tc.avg, tc.limit)
		got := costSortRunWithWidth(cp, tc.rows, tc.ncols, tc.avg, tc.limit, tc.width, "test")
		if got != legacy {
			t.Fatalf("switch-off %+v: got %+v, want legacy %+v", tc, got, legacy)
		}
	}
}

func TestCostSortRunPGRelationBytesUsesPGFloorOrdering(t *testing.T) {
	restore := setPGSortRelationBytesCostForTest(true)
	defer restore()
	cp := defaultCostParams()
	cp.workMem = 31 // one PG width-1 row is 32 bytes

	// PG computes zero input bytes before its two-row CPU floor. If input bytes
	// were computed after the floor, this would take the disk arm at 31 bytes.
	got := costSortRunWithWidth(cp, 0, 1, 0, -1, 1, "test")
	want := 2 * cp.cpuOperatorCost * 2 * math.Log2(2)
	want += cp.cpuOperatorCost * 2
	if math.Abs(got.Startup-(want-cp.cpuOperatorCost*2)) > 1e-12 || math.Abs(got.Total-want) > 1e-12 {
		t.Fatalf("zero-row PG sort = %+v, want pure floored CPU total %v", got, want)
	}

	// A useful bound computes output bytes after the floor. With a 0.5-row
	// bound, output fits while the original one-row input does not; the bounded
	// branch has N log2(2K) = 0 comparisons, not a disk price.
	bounded := costSortRunWithWidth(cp, 1, 1, 0, 0.5, 1, "test")
	wantBounded := cp.cpuOperatorCost * 2
	if math.Abs(bounded.Startup) > 1e-12 || math.Abs(bounded.Total-wantBounded) > 1e-12 {
		t.Fatalf("bounded PG floor ordering = %+v, want startup 0 total %v", bounded, wantBounded)
	}

	// Width, not a Goopg Datum-column count, authorizes the PG planner model.
	// A future path with no NCols metadata must not silently suppress it.
	withWidth := costSortRunWithWidth(cp, 1, 0, 0, -1, 1, "test")
	withoutWidth := costSortRunWithWidth(cp, 1, 0, 0, -1, 0, "test")
	if !(withWidth.Startup > withoutWidth.Startup) {
		t.Fatalf("positive PG width with NCols=0 did not price bytes: with %+v without %+v", withWidth, withoutWidth)
	}
}

func TestSortPathUsesNarrowEmittedWidthOnlyForOptInPrice(t *testing.T) {
	restore := setPGSortRelationBytesCostForTest(true)
	defer restore()
	cp := defaultCostParams()
	cp.workMem = 64
	rel := &RelOptInfo{Width: 700, NCols: 4, AvgVarBytes: 32}
	sub := &Path{Rel: rel, Rows: 10, Cost: Cost{Total: 7}, NCols: 2, AvgVarBytes: 0, OutputWidth: 8}
	got := sortPathFor(sub, nil, cp)
	want := costSortRunWithWidth(cp, 10, 2, 0, -1, 8, "test")
	if got.Cost != (Cost{Startup: 7 + want.Startup, Total: 7 + want.Total}) {
		t.Fatalf("narrow Sort cost = %+v, want width-8 cost %+v", got.Cost, want)
	}
	wide := costSortRunWithWidth(cp, 10, 2, 0, -1, 700, "test")
	if want == wide {
		t.Fatalf("fixture did not distinguish narrow output from relation width: %+v", want)
	}

	before := hashsize.Choose(sub.Rows, pathNCols(sub), pathAvgVarBytes(sub), cp.workMem)
	after := hashsize.Choose(sub.Rows, pathNCols(sub), pathAvgVarBytes(sub), cp.workMem)
	if before != after {
		t.Fatalf("Sort planner width changed executor hash geometry: before %+v after %+v", before, after)
	}
}

func TestOrderedSortUsesItsActualInputWidth(t *testing.T) {
	restore := setPGSortRelationBytesCostForTest(true)
	defer restore()
	cp := defaultCostParams()
	in := upperOrderedInput(10)
	sort := createOrderedPaths(newUpperRels(), in, upperOrderedKeys(), 0, cp, 0, -1).(*Sort)
	pc, ok := sort.PlanCostInfo()
	if !ok {
		t.Fatal("ordered Sort has no cost")
	}
	child := legacyDisplayCostOf(in)
	want := costSortRunWithWidth(cp, child.PlanRows, len(in.Output()), nodeAvgVarBytes(in.Output()), -1, nodeTupleWidth(in), "test")
	if pc.StartupCost != child.TotalCost+want.Startup || pc.TotalCost != child.TotalCost+want.Total {
		t.Fatalf("ordered Sort cost %+v, want actual input width %d cost %+v", pc, nodeTupleWidth(in), want)
	}
}

func TestPGSortRelationBytesTraceNamesBothCurrencies(t *testing.T) {
	restore := setPGSortRelationBytesCostForTest(true)
	defer restore()
	cp := defaultCostParams()
	lines := captureTrace(t, func() {
		costSortRunWithWidth(cp, 10, 2, 0, -1, 9, "test.sort")
	})
	if len(lines) != 1 || !strings.Contains(lines[0], "DPPGSORT caller=test.sort") ||
		!strings.Contains(lines[0], "width=9") || !strings.Contains(lines[0], "currency=pg") ||
		!strings.Contains(lines[0], "goopginput=") || !strings.Contains(lines[0], "pginput=") ||
		!strings.Contains(lines[0], "goopgbranch=") || !strings.Contains(lines[0], "pgbranch=") {
		t.Fatalf("trace = %q, want one PG Sort currency record", lines)
	}
	restore()
	restore = setPGSortRelationBytesCostForTest(false)
	lines = captureTrace(t, func() {
		costSortRunWithWidth(cp, 10, 2, 0, -1, 9, "test.sort")
	})
	if len(lines) != 1 || !strings.Contains(lines[0], "currency=goopg") ||
		!strings.Contains(lines[0], "pginput=") || !strings.Contains(lines[0], "pgbranch=") {
		t.Fatalf("switch-off trace = %q, want both currencies and Goopg election", lines)
	}
}

func TestCostSortRunWithWidthProductionCallersAreComplete(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	dir := filepath.Dir(thisFile)
	wantWidthCalls := map[string]int{
		"cost_funcs.go":          2, // definition plus legacy compatibility wrapper
		"joinpathsmerge.go":      1,
		"partialsortpaths.go":    2,
		"windowsetoppaths.go":    1,
	}
	seenWidthCalls := make(map[string]int)
	files, err := filepath.Glob(filepath.Join(dir, "*.go"))
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		body, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		name := filepath.Base(file)
		if got := strings.Count(string(body), "costSortRunWithWidth("); got != 0 {
			seenWidthCalls[name] = got
		}
		if got := strings.Count(string(body), "costSortRun("); got != 0 && (name != "cost_funcs.go" || got != 1) {
			t.Fatalf("%s has %d direct legacy costSortRun calls; every production caller needs R113 width provenance", name, got)
		}
	}
	if len(seenWidthCalls) != len(wantWidthCalls) {
		t.Fatalf("width-call files = %v, want exactly %v", seenWidthCalls, wantWidthCalls)
	}
	for file, want := range wantWidthCalls {
		if got := seenWidthCalls[file]; got != want {
			t.Fatalf("%s has %d costSortRunWithWidth occurrences, want %d; update R113 width provenance", file, got, want)
		}
	}
}
