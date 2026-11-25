package executor

import (
	"testing"
	"time"

	"github.com/goopg/goopg/internal/optimizer"
)

// TestPOSIXZoneAbbreviationInputTerminates: reading a zone abbreviation under
// a session TimeZone given as a POSIX string (horology.sql's
// 'CST7CDT,M4.1.0,M10.5.0') used to loop in zonePeriods without end.
func TestPOSIXZoneAbbreviationInputTerminates(t *testing.T) {
	ctx := &Context{GetSetting: func(n string) (string, bool) {
		switch n {
		case "timezone":
			return "CST7CDT,M4.1.0,M10.5.0", true
		case "datestyle":
			return "Postgres, MDY", true
		}
		return "", false
	}}
	done := make(chan Datum, 1)
	go func() {
		d, err := evalTypedStringLit(&optimizer.TypedStringLit{Type: "timestamptz", Value: "Sat Apr 02 12:00:00 2005 CST"}, ctx)
		if err != nil {
			t.Error(err)
		}
		done <- d
	}()
	select {
	case d := <-done:
		// CST7CDT names CST seven hours west, so 12:00 CST is 19:00 UTC (PG 18.3).
		if got := d.TimeValue().UTC().Format("2006-01-02 15:04"); got != "2005-04-02 19:00" {
			t.Errorf("read %s", got)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("reading the literal did not finish")
	}
}
