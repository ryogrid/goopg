package misc

import (
	"strings"
	"testing"
	"time"
)

func TestTimezoneNames(t *testing.T) {
	rows := TimezoneNames(time.Date(2026, 1, 15, 0, 0, 0, 0, time.UTC))
	if len(rows) == 0 {
		t.Skip("no tz database")
	}
	byName := map[string]TimezoneNameRow{}
	for _, r := range rows {
		if strings.HasPrefix(r.Name, "right/") || r.Name == "posixrules" {
			t.Errorf("%s must not be listed", r.Name)
		}
		byName[r.Name] = r
	}
	if r, ok := byName["Asia/Tokyo"]; !ok || r.Abbrev != "JST" || r.Offset != 9*3600 || r.IsDST {
		t.Errorf("Asia/Tokyo = %+v, %v", r, ok)
	}
	offsets := map[int]bool{}
	for _, r := range rows {
		offsets[r.Offset] = true
	}
	if len(offsets) < 24 {
		t.Errorf("%d distinct offsets", len(offsets))
	}
}
