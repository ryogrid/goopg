# Suggested Commands for goopg Development

## Build
```bash
go build ./...                        # full module
go build -o bin/goopg ./cmd/goopg    # explicit binary
```

## Test
```bash
go test ./...                                         # full unit suite (fast)
go test -count=1 ./...                               # no cache
go test -race ./...                                  # race detector
go test -run <Pattern> ./internal/<pkg>              # focused

# PostgreSQL oracle TAP tests (slow, needs client tools)
go test -v -run TestPort_ ./internal/testport/
go test -v -run TestPort_Psql001Basic ./internal/testport/

# Key pre-commit gates (required before commits touching executor/planner/wal)
go test -count=1 ./internal/mvcc/... ./internal/wal/...
go test -count=1 ./internal/planner/... ./internal/executor/... ./internal/server/... ./internal/initdb/...

# Ralph loop state consistency (run before final status block every loop)
make ralph-state-guard
```

## Lint/Format
```bash
gofmt -l .          # must produce empty output
go vet ./...
```

## PostgreSQL Reference Tools
```bash
# GNU GLOBAL (from inside ./postgres/)
global -x SymbolName         # locate definitions
global -rx SymbolName        # locate references
global -f path/to/file.c     # list symbols in file

# psql / pgbench compatibility testing
PATH=./postgres/local_install/bin:$PATH psql -p <port> -U postgres
```

## Server
```bash
./bin/goopg init -D /tmp/pgdata
./bin/goopg start -D /tmp/pgdata
```
