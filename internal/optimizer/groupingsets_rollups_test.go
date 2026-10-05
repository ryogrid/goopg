package optimizer

import (
	"reflect"
	"testing"
)

// TestExtractGroupingRollups pins preprocess_grouping_sets' rollups
// (M0146-0020b) on the shapes PG 18.3 prints.
func TestExtractGroupingRollups(t *testing.T) {
	cases := []struct {
		name      string
		sets      [][]int
		sortOrder []int
		want      []GroupingRollup
	}{
		{
			// CUBE(ten, two, v): PG prints `Group Key: ten, two, v / ten,
			// two / ten / ()`, then `Sort Key: two, v` and `Sort Key: v,
			// ten` with their sets.
			name: "cube of three",
			sets: [][]int{{0, 1, 2}, {0, 1}, {0, 2}, {0}, {1, 2}, {1}, {2}, {}},
			want: []GroupingRollup{
				{Order: []int{0, 1, 2}, Sets: []int{0, 1, 3, 7}},
				{Order: []int{1, 2}, Sets: []int{4, 5}},
				{Order: []int{2, 0}, Sets: []int{2, 6}},
			},
		},
		{
			name: "disjoint sets",
			sets: [][]int{{0}, {1}},
			want: []GroupingRollup{
				{Order: []int{0}, Sets: []int{0}},
				{Order: []int{1}, Sets: []int{1}},
			},
		},
		{
			// A single rollup follows ORDER BY: ROLLUP over (a, b) with
			// `ORDER BY b, a` groups on b, a.
			name:      "one rollup steered by ORDER BY",
			sets:      [][]int{{0, 1}, {}},
			sortOrder: []int{1, 0},
			want:      []GroupingRollup{{Order: []int{1, 0}, Sets: []int{0, 1}}},
		},
		{
			// ORDER BY is ignored once there are several rollups.
			name:      "several rollups ignore ORDER BY",
			sets:      [][]int{{0, 1}, {2}},
			sortOrder: []int{1, 0},
			want: []GroupingRollup{
				{Order: []int{2}, Sets: []int{1}},
				{Order: []int{0, 1}, Sets: []int{0}},
			},
		},
		{
			name: "duplicate and empty sets",
			sets: [][]int{{0}, {}, {0}, {}},
			want: []GroupingRollup{{Order: []int{0}, Sets: []int{2, 0, 3, 1}}},
		},
	}
	for _, c := range cases {
		got := ExtractGroupingRollups(c.sets, c.sortOrder)
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: got %+v, want %+v", c.name, got, c.want)
		}
	}
}
