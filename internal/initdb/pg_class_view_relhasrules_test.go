package initdb

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// TestBootstrappedViewsCarryRelhasrules is M0131-S6 guard 2: the on-disk
// evidence for the relhasrules flip.
//
// A hosted PG only scans pg_rewrite for a relation whose pg_class row says
// relhasrules=true — RelationBuildDesc takes the else arm at
// postgres/src/backend/utils/cache/relcache.c:1249-1255 otherwise, and the six
// nailed replication views become unevaluable no matter how faithful their
// _RETURN rows are. Asserting pgClassRow() in isolation is not enough: the
// value that matters is the byte a hosted PG reads out of base/{1,5}/1259, at
// FormData_pg_class offset 124.
//
// Both directions are pinned. Every relkind='v' tuple must carry relhasrules
// true; every relkind='r' tuple must still carry false, because ordinary
// catalog heaps have no rewrite rules and a spurious true would send PG through
// RelationBuildRuleLock for a relation with no pg_rewrite row (a silent
// failure — relcache.c:4313-4318 retries once, then quietly clears its local
// copy of the flag).
func TestBootstrappedViewsCarryRelhasrules(t *testing.T) {
	dir := t.TempDir()
	if err := Init(Options{DataDir: dir, NoSync: true}); err != nil {
		t.Fatal(err)
	}

	// FormData_pg_class fixed-width offsets, per pgClassColDefs().
	const (
		offRelkind      = 119
		offRelhasrules  = 124
		offRelfilenode  = 88
		minPayloadBytes = offRelhasrules + 1
	)

	for _, db := range []string{"base/1", "base/5"} {
		data, err := os.ReadFile(filepath.Join(dir, db, "1259"))
		if err != nil {
			t.Fatalf("%s/1259: %v", db, err)
		}
		views, tables := 0, 0

		for pi := 0; pi < len(data)/storage.BlockSize; pi++ {
			page := storage.Page(data[pi*storage.BlockSize : (pi+1)*storage.BlockSize])
			count, err := storage.PageLinePointerCount(page)
			if err != nil {
				t.Fatalf("%s/1259 page %d: %v", db, pi, err)
			}
			for slot := uint16(1); slot <= uint16(count); slot++ {
				itemID, err := storage.PageGetItemID(page, slot)
				if err != nil {
					t.Fatalf("%s/1259 page %d slot %d: %v", db, pi, slot, err)
				}
				if itemID.Flags != storage.ItemIDNormal {
					continue
				}
				raw, err := storage.PageGetItemRaw(page, slot)
				if err != nil {
					t.Fatalf("%s/1259 page %d slot %d: %v", db, pi, slot, err)
				}
				payload := raw[int(raw[22]):]
				if len(payload) < minPayloadBytes {
					t.Fatalf("%s/1259 page %d slot %d: payload %d bytes, need >= %d",
						db, pi, slot, len(payload), minPayloadBytes)
				}
				relkind := payload[offRelkind]
				hasRules := payload[offRelhasrules] != 0

				switch relkind {
				case 'v':
					views++
					if !hasRules {
						t.Errorf("%s/1259 page %d slot %d: view has relhasrules=false, want true",
							db, pi, slot)
					}
					// Cheap cross-check that offset 119 really is relkind:
					// a view must also have relfilenode=0.
					if fn := payload[offRelfilenode : offRelfilenode+4]; fn[0]|fn[1]|fn[2]|fn[3] != 0 {
						t.Errorf("%s/1259 page %d slot %d: relkind='v' but relfilenode nonzero — offsets are wrong",
							db, pi, slot)
					}
				case 'r':
					tables++
					if hasRules {
						t.Errorf("%s/1259 page %d slot %d: table has relhasrules=true, want false",
							db, pi, slot)
					}
				}
			}
		}

		// Non-vacuity: the six nailed replication views must all be present,
		// and there must be real tables to have checked the false direction on.
		if views != len(nailedViewOIDs()) {
			t.Errorf("%s/1259: %d relkind='v' tuples, want %d", db, views, len(nailedViewOIDs()))
		}
		if tables == 0 {
			t.Errorf("%s/1259: no relkind='r' tuples — the false direction was never exercised", db)
		}
	}
}

// nailedViewOIDs lists the bootstrapped system views, in the pinned upstream
// OIDs M0131-S8a adopted. Kept as a function rather than derived from
// nailedLocalRels so the count is an independent statement of intent: a view
// silently dropped from (or added to) the nailed set fails the guard above.
func nailedViewOIDs() []uint32 {
	return []uint32{12231, 12240, 12244, 12248, 12261, 12266}
}
