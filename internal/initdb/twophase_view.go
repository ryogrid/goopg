package initdb

import (
	"fmt"
	"time"

	"github.com/goopg/goopg/internal/access/transam"
	"github.com/goopg/goopg/internal/access/transam/xlog"
	"github.com/goopg/goopg/internal/catalog"
)

// registerPreparedXactsView feeds pg_catalog.pg_prepared_xacts from the
// transaction manager's prepared-transaction registry: pg_prepared_xact()
// joined to pg_authid and pg_database, as system_views.sql defines it. The
// registry holds every prepared transaction the server knows — prepared here,
// restored at startup, or (on a standby) replayed from a PREPARE record and
// not yet finished — which is what GetPreparedTransactionList walks.
// M0149-0016.
func registerPreparedXactsView(cat *catalog.InMemory, txnMgr *transam.Manager) error {
	return cat.SetVirtualRows("pg_catalog", "pg_prepared_xacts", func() [][]string {
		if txnMgr == nil {
			return nil
		}
		list := txnMgr.PreparedXacts()
		out := make([][]string, 0, len(list))
		for _, p := range list {
			st, err := xlog.ParseTwoPhaseState(p.State)
			if err != nil {
				continue
			}
			owner := cat.RoleNameForOID(st.Owner)
			db, _ := cat.DatabaseNameByDisplayOID(st.Database)
			out = append(out, []string{
				fmt.Sprintf("%d", p.XID),
				p.GID,
				pgTimestampTzText(st.PreparedAt),
				owner,
				db,
			})
		}
		return out
	})
}

// pgTimestampTzText renders a TimestampTz (µs since 2000-01-01 UTC) as
// timestamptz input text.
func pgTimestampTzText(us int64) string {
	const pgEpochUnix = 946684800
	t := time.Unix(pgEpochUnix, 0).Add(time.Duration(us) * time.Microsecond).UTC()
	return t.Format("2006-01-02 15:04:05.999999") + "+00"
}
