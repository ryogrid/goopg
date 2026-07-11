package initdb

import (
	"github.com/goopg/goopg/internal/activity"
	"github.com/goopg/goopg/internal/catalog"
)

// registerPgStatSslView registers the pg_catalog.pg_stat_ssl virtual view
// (M0122-0003). Upstream (system_views.sql) projects a subset of
// pg_stat_get_activity(NULL) columns for every backend whose client_port is
// non-NULL (i.e. real client backends, not background workers):
//
//	SELECT pid, ssl, sslversion AS version, sslcipher AS cipher,
//	       sslbits AS bits, ssl_client_dn AS client_dn,
//	       ssl_client_serial AS client_serial, ssl_issuer_dn AS issuer_dn
//	FROM pg_stat_get_activity(NULL) AS S
//	WHERE S.client_port IS NOT NULL;
//
// goopg does not implement TLS, so every connection is unencrypted: ssl is a
// faithful false and all TLS-detail columns are NULL — byte-identical to a real
// PG 18.3 cluster with `ssl = off`. The view is backed by the SAME
// activity.Registry that powers pg_stat_activity, so it lists exactly the live
// client backends (one row each, matching pg_stat_activity minus the
// background-worker rows the client_port filter drops).
func registerPgStatSslView(cat *catalog.InMemory, reg *activity.Registry) error {
	tbl := &catalog.Table{
		Schema:  "pg_catalog",
		Name:    "pg_stat_ssl",
		Virtual: true,
		Columns: []catalog.Column{
			{Name: "pid", Type: catalog.Type{Name: "int4"}},
			{Name: "ssl", Type: catalog.Type{Name: "bool"}},
			{Name: "version", Type: catalog.Type{Name: "text"}},
			{Name: "cipher", Type: catalog.Type{Name: "text"}},
			{Name: "bits", Type: catalog.Type{Name: "int4"}},
			{Name: "client_dn", Type: catalog.Type{Name: "text"}},
			{Name: "client_serial", Type: catalog.Type{Name: "numeric"}},
			{Name: "issuer_dn", Type: catalog.Type{Name: "text"}},
		},
	}
	tbl.VirtualRows = func() [][]string {
		if reg == nil {
			return nil
		}
		snap := reg.Snapshot()
		rows := make([][]string, 0, len(snap))
		for _, b := range snap {
			if b.ClientPort == "" { // client_port IS NOT NULL filter
				continue
			}
			rows = append(rows, []string{
				numericPIDOrNull(b.PID),
				boolText(false), // ssl: goopg has no TLS
				"",              // version: NULL
				"",              // cipher: NULL
				"",              // bits: NULL
				"",              // client_dn: NULL
				"",              // client_serial: NULL
				"",              // issuer_dn: NULL
			})
		}
		return rows
	}
	return cat.RegisterVirtualTable(tbl)
}

// registerPgStatGssapiView registers the pg_catalog.pg_stat_gssapi virtual view
// (M0122-0003). Upstream (system_views.sql):
//
//	SELECT pid, gss_auth AS gss_authenticated, gss_princ AS principal,
//	       gss_enc AS encrypted, gss_delegation AS credentials_delegated
//	FROM pg_stat_get_activity(NULL) AS S
//	WHERE S.client_port IS NOT NULL;
//
// goopg does not implement GSSAPI, so gss_authenticated / encrypted /
// credentials_delegated are all a faithful false and principal is NULL —
// byte-identical to a real PG 18.3 cluster built without GSSAPI. Backed by the
// same activity.Registry as pg_stat_ssl / pg_stat_activity.
func registerPgStatGssapiView(cat *catalog.InMemory, reg *activity.Registry) error {
	tbl := &catalog.Table{
		Schema:  "pg_catalog",
		Name:    "pg_stat_gssapi",
		Virtual: true,
		Columns: []catalog.Column{
			{Name: "pid", Type: catalog.Type{Name: "int4"}},
			{Name: "gss_authenticated", Type: catalog.Type{Name: "bool"}},
			{Name: "principal", Type: catalog.Type{Name: "text"}},
			{Name: "encrypted", Type: catalog.Type{Name: "bool"}},
			{Name: "credentials_delegated", Type: catalog.Type{Name: "bool"}},
		},
	}
	tbl.VirtualRows = func() [][]string {
		if reg == nil {
			return nil
		}
		snap := reg.Snapshot()
		rows := make([][]string, 0, len(snap))
		for _, b := range snap {
			if b.ClientPort == "" { // client_port IS NOT NULL filter
				continue
			}
			rows = append(rows, []string{
				numericPIDOrNull(b.PID),
				boolText(false), // gss_authenticated
				"",              // principal: NULL
				boolText(false), // encrypted
				boolText(false), // credentials_delegated
			})
		}
		return rows
	}
	return cat.RegisterVirtualTable(tbl)
}
