package config

import "strconv"

// MajorVersion is the PostgreSQL major version goopg targets ("18"). It is the
// value written to a data directory's PG_VERSION file and the major component
// of the server_version ParameterStatus.
const MajorVersion = "18"

// CatalogVersionNo is the pg_control CATALOG_VERSION_NO (PG18). It is the
// single source of truth shared by the control-file writer (internal/initdb)
// and any subsystem that must construct the tablespace version directory
// (CREATE TABLESPACE, BASE_BACKUP). It lives in the leaf config package so
// those higher-level packages can reference it without an import cycle.
const CatalogVersionNo = 202506291

// TablespaceVersionDirectory mirrors PG's TABLESPACE_VERSION_DIRECTORY macro
// ("PG_" PG_MAJORVERSION "_" CATALOG_VERSION_NO), e.g. "PG_18_202506291". It
// names the per-tablespace version subdirectory created under pg_tblspc/<oid>
// (see create_tablespace_directories in tablespace.c).
var TablespaceVersionDirectory = "PG_" + MajorVersion + "_" + strconv.Itoa(CatalogVersionNo)
