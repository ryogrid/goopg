package executor

import (
	"encoding/hex"
	"testing"
)

// The tuples below are pg_class_relname_nsp_index leaf tuples of a PG 18.3
// initdb (base/5/2663): name keys stored as cstrings. M0149-0016e.
const (
	pgRelnameNspTupServers = "00000c00390020405f70675f666f726569676e5f7365727665727300d9330000"
	pgRelnameNspTupFDW     = "00000c00340028405f70675f666f726569676e5f646174615f7772617070657273000000d9330000"
)

func TestNameDataTupleToCstringMatchesPG(t *testing.T) {
	lay, ok := sysBtreeAttrLayouts(pgClassRelnameNspIndexOID)
	if !ok {
		t.Fatal("no layout for pg_class_relname_nsp_index")
	}
	for _, tc := range []struct {
		name   string
		off    uint16
		pgHex  string
		blkNum uint32
	}{
		{"_pg_foreign_servers", 0x39, pgRelnameNspTupServers, 12},
		{"_pg_foreign_data_wrappers", 0x34, pgRelnameNspTupFDW, 12},
	} {
		goopg := buildIndexTupleNameOidKey(tc.blkNum, tc.off, tc.name, 13273)
		got, ok := nameDataTupleToCstring(lay, goopg)
		if !ok {
			t.Fatalf("%s: conversion failed", tc.name)
		}
		if h := hex.EncodeToString(got); h != tc.pgHex {
			t.Errorf("%s:\n got %s\nwant %s", tc.name, h, tc.pgHex)
		}
		// And back: the NameData payload compares equal to goopg's own.
		pg, _ := hex.DecodeString(tc.pgHex)
		back := cstringKeyConv(lay)(pg[sysIndexTupleHoff:], len(lay))
		if cmpKeyNameOid(back, goopg[sysIndexTupleHoff:]) != 0 {
			t.Errorf("%s: cstring payload does not convert back to goopg's key", tc.name)
		}
	}
}

func TestWrapCstringCmpOrdersPGKeys(t *testing.T) {
	lay, _ := sysBtreeAttrLayouts(pgClassRelnameNspIndexOID)
	a, _ := hex.DecodeString(pgRelnameNspTupFDW)
	b, _ := hex.DecodeString(pgRelnameNspTupServers)
	cmp := wrapCstringCmp(cmpKeyNameOid, cstringKeyConv(lay), len(lay))
	if c := cmp(a[sysIndexTupleHoff:], b[sysIndexTupleHoff:]); c >= 0 {
		t.Errorf("_pg_foreign_data_wrappers vs _pg_foreign_servers = %d, want < 0", c)
	}
	// A truncated pivot holding only the name sorts before an equal full key.
	pc := pivotAwareCmp(pgClassRelnameNspIndexOID, cmpKeyNameOid, cstringKeyConv(lay))
	piv := append([]byte(nil), b[:sysIndexTupleHoff+20]...) // name + NUL only
	piv[4], piv[5] = 1, 0                                   // natts = 1
	piv[7] |= 0x20                                          // INDEX_ALT_TID_MASK
	if c := pc(b[sysIndexTupleHoff:], piv); c <= 0 {
		t.Errorf("full key vs its truncated pivot = %d, want > 0", c)
	}
}

func TestCstringLayoutOfVarlenaKey(t *testing.T) {
	lay, _ := sysBtreeAttrLayouts(pgProcPronameArgsNspIndexOID)
	// NameData layout: "abs" NUL-padded to 64, then an empty oidvector header.
	nd := make([]byte, 64+24+4)
	copy(nd, "abs")
	nd[64] = byte(24 << 2)
	if cs, decided := cstringLayoutOfVarlenaKey(lay, nd); !decided || cs {
		t.Errorf("NameData key: cs=%v decided=%v, want false,true", cs, decided)
	}
	conv, ok := nameDataToCstringPayload(lay, nd)
	if !ok {
		t.Fatal("conversion failed")
	}
	if cs, decided := cstringLayoutOfVarlenaKey(lay, conv); !decided || !cs {
		t.Errorf("cstring key: cs=%v decided=%v, want true,true", cs, decided)
	}
	back, ok := cstringToNameDataPayload(lay, conv, len(lay))
	if !ok || string(back) != string(nd) {
		t.Errorf("round trip changed the key")
	}
}
