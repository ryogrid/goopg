(idle — nothing in flight)

Note for next loop: this loop landed `ALTER DOMAIN ... SET NOT NULL`/`DROP
NOT NULL` (M0122-0005 bucket, loop #50) — parser (`internal/parser/ddl.go`),
`catalog.SetDomainNotNull`, `execAlterDomain`'s new cases, tests, design doc
follow-up section, deferral ledger row, fix_plan entry all landed and
committed. Only `SET SCHEMA` remains unparsed for `ALTER DOMAIN` (needs a new
`Domain.Schema` field decision — `Domain` has none today, unlike `Table`).
No `ALTER DOMAIN` sub-form WAL-logs yet (restart persistence gap, unchanged
from prior loops — only `CREATE`/`DROP DOMAIN` persist).

Next step: per the Current Priority banner, either (a) resume the M0110-0001
multi-database catalog/storage isolation survey (milestone-scale — see
deferral ledger row at "DU-002 round-trip probe added to
TestPort_PgDumpConnectionSetup" for the resume point: every `catalog.InMemory`
object map needs a DBOid/DBName key), (b) pick up `Domain.Schema`/`ALTER
DOMAIN SET SCHEMA` (small, mechanical, same shape as this loop), or (c)
survey `.ralph/deferral_ledger.md` for a fresh open (`status = -`) row not
already covered by an M0122-NNNN bucket item.

Gates run this loop: go build ./... clean; go test
./internal/catalog/... ./internal/executor/... ./internal/parser/... PASS;
scripts/tpch-spotcheck.sh PASS (Q12=2/Q13=33); make ralph-state-guard OK
(auto-repaired a stale progress.json left by the prior loop).

In-flight: none.
