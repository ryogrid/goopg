package initdb

import (
	"fmt"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/executor"
)

// pgConversionEntry mirrors one row of PG18's pg_conversion (OID 2607).
// goopg stores the 8 fixed-size columns that PG reads during early
// backend startup via GETSTRUCT(Form_pg_conversion).
type pgConversionEntry struct {
	OID            uint32
	ConName        string // name (max 64 bytes)
	ConNamespace   uint32 // 11 = pg_catalog for all BKI rows
	ConOwner       uint32 // 10 = BOOTSTRAP_SUPERUSERID
	ConForEncoding int32  // source encoding ID (pg_enc enum value)
	ConToEncoding  int32  // target encoding ID (pg_enc enum value)
	ConProc        uint32 // OID of conversion proc in pg_proc
	ConDefault     bool   // always true for BKI rows (BKI_DEFAULT(t))
}

// pg_enc integer values from postgres/src/include/mb/pg_wchar.h.
// PG_SQL_ASCII=0 is omitted; conversions only reference the values below.
const (
	pgEncEUCJP        int32 = 1
	pgEncEUCCN        int32 = 2
	pgEncEUCKR        int32 = 3
	pgEncEUCTW        int32 = 4
	pgEncEUCJIS2004   int32 = 5
	pgEncUTF8         int32 = 6
	pgEncMULEINTERNAL int32 = 7
	pgEncLATIN1       int32 = 8
	pgEncLATIN2       int32 = 9
	pgEncLATIN3       int32 = 10
	pgEncLATIN4       int32 = 11
	pgEncLATIN5       int32 = 12
	pgEncLATIN6       int32 = 13
	pgEncLATIN7       int32 = 14
	pgEncLATIN8       int32 = 15
	pgEncLATIN9       int32 = 16
	pgEncLATIN10      int32 = 17
	pgEncWIN1256      int32 = 18
	pgEncWIN1258      int32 = 19
	pgEncWIN866       int32 = 20
	pgEncWIN874       int32 = 21
	pgEncKOI8R        int32 = 22
	pgEncWIN1251      int32 = 23
	pgEncWIN1252      int32 = 24
	pgEncISO88595     int32 = 25
	pgEncISO88596     int32 = 26
	pgEncISO88597     int32 = 27
	pgEncISO88598     int32 = 28
	pgEncWIN1250      int32 = 29
	pgEncWIN1253      int32 = 30
	pgEncWIN1254      int32 = 31
	pgEncWIN1255      int32 = 32
	pgEncWIN1257      int32 = 33
	pgEncKOI8U        int32 = 34
	pgEncSJIS         int32 = 35
	pgEncBIG5         int32 = 36
	pgEncGBK          int32 = 37
	pgEncUHC          int32 = 38
	pgEncGB18030      int32 = 39
	pgEncJOHAB        int32 = 40
	pgEncSHIFTJIS2004 int32 = 41
)

// pgConversionColDefs returns the 8-column fixed-size schema for pg_conversion
// (OID 2607) used by goopg's bootstrap writer.
func pgConversionColDefs() []catalog.Column {
	return []catalog.Column{
		{Name: "oid", Type: catalog.Type{Name: "oid"}},
		{Name: "conname", Type: catalog.Type{Name: "name"}},
		{Name: "connamespace", Type: catalog.Type{Name: "oid"}},
		{Name: "conowner", Type: catalog.Type{Name: "oid"}},
		{Name: "conforencoding", Type: catalog.Type{Name: "int4"}},
		{Name: "contoencoding", Type: catalog.Type{Name: "int4"}},
		{Name: "conproc", Type: catalog.Type{Name: "oid"}},
		{Name: "condefault", Type: catalog.Type{Name: "bool"}},
	}
}

// pgConversionRow encodes one pg_conversion row. Field order mirrors the
// 8 fixed-size columns of FormData_pg_conversion so PG's GETSTRUCT cast
// is byte-for-byte valid for these columns.
func pgConversionRow(e pgConversionEntry) executor.Row {
	return executor.Row{
		executor.NewIntDatum(int64(e.OID)),
		executor.NewStringDatum(e.ConName),
		executor.NewIntDatum(int64(e.ConNamespace)),
		executor.NewIntDatum(int64(e.ConOwner)),
		executor.NewIntDatum(int64(e.ConForEncoding)),
		executor.NewIntDatum(int64(e.ConToEncoding)),
		executor.NewIntDatum(int64(e.ConProc)),
		executor.NewBoolDatum(e.ConDefault),
	}
}

// pgConversionInitialEntries returns all 128 BKI seed rows for pg_conversion
// from PG18's pg_conversion.dat. All rows use connamespace=11 (pg_catalog)
// and conowner=10 (BOOTSTRAP_SUPERUSERID) and condefault=true.
//
// Encoding integer IDs are from the pg_enc enum in pg_wchar.h.
// ConProc OIDs are resolved from pg_proc.dat.
func pgConversionInitialEntries() []pgConversionEntry {
	const nsp = uint32(11) // pg_catalog
	const own = uint32(10) // BOOTSTRAP_SUPERUSERID
	return []pgConversionEntry{
		// OIDs 4402-4449: Cyrillic and MULE_INTERNAL conversions
		{4402, "koi8_r_to_mic", nsp, own, pgEncKOI8R, pgEncMULEINTERNAL, 4302, true},
		{4403, "mic_to_koi8_r", nsp, own, pgEncMULEINTERNAL, pgEncKOI8R, 4303, true},
		{4404, "iso_8859_5_to_mic", nsp, own, pgEncISO88595, pgEncMULEINTERNAL, 4304, true},
		{4405, "mic_to_iso_8859_5", nsp, own, pgEncMULEINTERNAL, pgEncISO88595, 4305, true},
		{4406, "windows_1251_to_mic", nsp, own, pgEncWIN1251, pgEncMULEINTERNAL, 4306, true},
		{4407, "mic_to_windows_1251", nsp, own, pgEncMULEINTERNAL, pgEncWIN1251, 4307, true},
		{4408, "windows_866_to_mic", nsp, own, pgEncWIN866, pgEncMULEINTERNAL, 4308, true},
		{4409, "mic_to_windows_866", nsp, own, pgEncMULEINTERNAL, pgEncWIN866, 4309, true},
		{4410, "koi8_r_to_windows_1251", nsp, own, pgEncKOI8R, pgEncWIN1251, 4310, true},
		{4411, "windows_1251_to_koi8_r", nsp, own, pgEncWIN1251, pgEncKOI8R, 4311, true},
		{4412, "koi8_r_to_windows_866", nsp, own, pgEncKOI8R, pgEncWIN866, 4312, true},
		{4413, "windows_866_to_koi8_r", nsp, own, pgEncWIN866, pgEncKOI8R, 4313, true},
		{4414, "windows_866_to_windows_1251", nsp, own, pgEncWIN866, pgEncWIN1251, 4314, true},
		{4415, "windows_1251_to_windows_866", nsp, own, pgEncWIN1251, pgEncWIN866, 4315, true},
		{4416, "iso_8859_5_to_koi8_r", nsp, own, pgEncISO88595, pgEncKOI8R, 4316, true},
		{4417, "koi8_r_to_iso_8859_5", nsp, own, pgEncKOI8R, pgEncISO88595, 4317, true},
		{4418, "iso_8859_5_to_windows_1251", nsp, own, pgEncISO88595, pgEncWIN1251, 4318, true},
		{4419, "windows_1251_to_iso_8859_5", nsp, own, pgEncWIN1251, pgEncISO88595, 4319, true},
		{4420, "iso_8859_5_to_windows_866", nsp, own, pgEncISO88595, pgEncWIN866, 4320, true},
		{4421, "windows_866_to_iso_8859_5", nsp, own, pgEncWIN866, pgEncISO88595, 4321, true},
		// EUC-CN and MULE_INTERNAL
		{4422, "euc_cn_to_mic", nsp, own, pgEncEUCCN, pgEncMULEINTERNAL, 4322, true},
		{4423, "mic_to_euc_cn", nsp, own, pgEncMULEINTERNAL, pgEncEUCCN, 4323, true},
		// EUC-JP, SJIS and MULE_INTERNAL
		{4424, "euc_jp_to_sjis", nsp, own, pgEncEUCJP, pgEncSJIS, 4324, true},
		{4425, "sjis_to_euc_jp", nsp, own, pgEncSJIS, pgEncEUCJP, 4325, true},
		{4426, "euc_jp_to_mic", nsp, own, pgEncEUCJP, pgEncMULEINTERNAL, 4326, true},
		{4427, "sjis_to_mic", nsp, own, pgEncSJIS, pgEncMULEINTERNAL, 4327, true},
		{4428, "mic_to_euc_jp", nsp, own, pgEncMULEINTERNAL, pgEncEUCJP, 4328, true},
		{4429, "mic_to_sjis", nsp, own, pgEncMULEINTERNAL, pgEncSJIS, 4329, true},
		// EUC-KR and MULE_INTERNAL
		{4430, "euc_kr_to_mic", nsp, own, pgEncEUCKR, pgEncMULEINTERNAL, 4330, true},
		{4431, "mic_to_euc_kr", nsp, own, pgEncMULEINTERNAL, pgEncEUCKR, 4331, true},
		// EUC-TW, BIG5 and MULE_INTERNAL
		{4432, "euc_tw_to_big5", nsp, own, pgEncEUCTW, pgEncBIG5, 4332, true},
		{4433, "big5_to_euc_tw", nsp, own, pgEncBIG5, pgEncEUCTW, 4333, true},
		{4434, "euc_tw_to_mic", nsp, own, pgEncEUCTW, pgEncMULEINTERNAL, 4334, true},
		{4435, "big5_to_mic", nsp, own, pgEncBIG5, pgEncMULEINTERNAL, 4335, true},
		{4436, "mic_to_euc_tw", nsp, own, pgEncMULEINTERNAL, pgEncEUCTW, 4336, true},
		{4437, "mic_to_big5", nsp, own, pgEncMULEINTERNAL, pgEncBIG5, 4337, true},
		// Latin and WIN1250 encodings via MULE_INTERNAL
		{4438, "iso_8859_2_to_mic", nsp, own, pgEncLATIN2, pgEncMULEINTERNAL, 4338, true},
		{4439, "mic_to_iso_8859_2", nsp, own, pgEncMULEINTERNAL, pgEncLATIN2, 4339, true},
		{4440, "windows_1250_to_mic", nsp, own, pgEncWIN1250, pgEncMULEINTERNAL, 4340, true},
		{4441, "mic_to_windows_1250", nsp, own, pgEncMULEINTERNAL, pgEncWIN1250, 4341, true},
		{4442, "iso_8859_2_to_windows_1250", nsp, own, pgEncLATIN2, pgEncWIN1250, 4342, true},
		{4443, "windows_1250_to_iso_8859_2", nsp, own, pgEncWIN1250, pgEncLATIN2, 4343, true},
		{4444, "iso_8859_1_to_mic", nsp, own, pgEncLATIN1, pgEncMULEINTERNAL, 4344, true},
		{4445, "mic_to_iso_8859_1", nsp, own, pgEncMULEINTERNAL, pgEncLATIN1, 4345, true},
		{4446, "iso_8859_3_to_mic", nsp, own, pgEncLATIN3, pgEncMULEINTERNAL, 4346, true},
		{4447, "mic_to_iso_8859_3", nsp, own, pgEncMULEINTERNAL, pgEncLATIN3, 4347, true},
		{4448, "iso_8859_4_to_mic", nsp, own, pgEncLATIN4, pgEncMULEINTERNAL, 4348, true},
		{4449, "mic_to_iso_8859_4", nsp, own, pgEncMULEINTERNAL, pgEncLATIN4, 4349, true},
		// OIDs 4452-4531: UTF8 conversions (4450-4451 not present in pg_conversion.dat)
		{4452, "big5_to_utf8", nsp, own, pgEncBIG5, pgEncUTF8, 4352, true},
		{4453, "utf8_to_big5", nsp, own, pgEncUTF8, pgEncBIG5, 4353, true},
		{4454, "utf8_to_koi8_r", nsp, own, pgEncUTF8, pgEncKOI8R, 4354, true},
		{4455, "koi8_r_to_utf8", nsp, own, pgEncKOI8R, pgEncUTF8, 4355, true},
		{4456, "utf8_to_koi8_u", nsp, own, pgEncUTF8, pgEncKOI8U, 4356, true},
		{4457, "koi8_u_to_utf8", nsp, own, pgEncKOI8U, pgEncUTF8, 4357, true},
		{4458, "utf8_to_windows_866", nsp, own, pgEncUTF8, pgEncWIN866, 4358, true},
		{4459, "windows_866_to_utf8", nsp, own, pgEncWIN866, pgEncUTF8, 4359, true},
		{4460, "utf8_to_windows_874", nsp, own, pgEncUTF8, pgEncWIN874, 4358, true},
		{4461, "windows_874_to_utf8", nsp, own, pgEncWIN874, pgEncUTF8, 4359, true},
		{4462, "utf8_to_windows_1250", nsp, own, pgEncUTF8, pgEncWIN1250, 4358, true},
		{4463, "windows_1250_to_utf8", nsp, own, pgEncWIN1250, pgEncUTF8, 4359, true},
		{4464, "utf8_to_windows_1251", nsp, own, pgEncUTF8, pgEncWIN1251, 4358, true},
		{4465, "windows_1251_to_utf8", nsp, own, pgEncWIN1251, pgEncUTF8, 4359, true},
		{4466, "utf8_to_windows_1252", nsp, own, pgEncUTF8, pgEncWIN1252, 4358, true},
		{4467, "windows_1252_to_utf8", nsp, own, pgEncWIN1252, pgEncUTF8, 4359, true},
		{4468, "utf8_to_windows_1253", nsp, own, pgEncUTF8, pgEncWIN1253, 4358, true},
		{4469, "windows_1253_to_utf8", nsp, own, pgEncWIN1253, pgEncUTF8, 4359, true},
		{4470, "utf8_to_windows_1254", nsp, own, pgEncUTF8, pgEncWIN1254, 4358, true},
		{4471, "windows_1254_to_utf8", nsp, own, pgEncWIN1254, pgEncUTF8, 4359, true},
		{4472, "utf8_to_windows_1255", nsp, own, pgEncUTF8, pgEncWIN1255, 4358, true},
		{4473, "windows_1255_to_utf8", nsp, own, pgEncWIN1255, pgEncUTF8, 4359, true},
		{4474, "utf8_to_windows_1256", nsp, own, pgEncUTF8, pgEncWIN1256, 4358, true},
		{4475, "windows_1256_to_utf8", nsp, own, pgEncWIN1256, pgEncUTF8, 4359, true},
		{4476, "utf8_to_windows_1257", nsp, own, pgEncUTF8, pgEncWIN1257, 4358, true},
		{4477, "windows_1257_to_utf8", nsp, own, pgEncWIN1257, pgEncUTF8, 4359, true},
		{4478, "utf8_to_windows_1258", nsp, own, pgEncUTF8, pgEncWIN1258, 4358, true},
		{4479, "windows_1258_to_utf8", nsp, own, pgEncWIN1258, pgEncUTF8, 4359, true},
		{4480, "euc_cn_to_utf8", nsp, own, pgEncEUCCN, pgEncUTF8, 4360, true},
		{4481, "utf8_to_euc_cn", nsp, own, pgEncUTF8, pgEncEUCCN, 4361, true},
		{4482, "euc_jp_to_utf8", nsp, own, pgEncEUCJP, pgEncUTF8, 4362, true},
		{4483, "utf8_to_euc_jp", nsp, own, pgEncUTF8, pgEncEUCJP, 4363, true},
		{4484, "euc_kr_to_utf8", nsp, own, pgEncEUCKR, pgEncUTF8, 4364, true},
		{4485, "utf8_to_euc_kr", nsp, own, pgEncUTF8, pgEncEUCKR, 4365, true},
		{4486, "euc_tw_to_utf8", nsp, own, pgEncEUCTW, pgEncUTF8, 4366, true},
		{4487, "utf8_to_euc_tw", nsp, own, pgEncUTF8, pgEncEUCTW, 4367, true},
		{4488, "gb18030_to_utf8", nsp, own, pgEncGB18030, pgEncUTF8, 4368, true},
		{4489, "utf8_to_gb18030", nsp, own, pgEncUTF8, pgEncGB18030, 4369, true},
		{4490, "gbk_to_utf8", nsp, own, pgEncGBK, pgEncUTF8, 4370, true},
		{4491, "utf8_to_gbk", nsp, own, pgEncUTF8, pgEncGBK, 4371, true},
		{4492, "utf8_to_iso_8859_2", nsp, own, pgEncUTF8, pgEncLATIN2, 4372, true},
		{4493, "iso_8859_2_to_utf8", nsp, own, pgEncLATIN2, pgEncUTF8, 4373, true},
		{4494, "utf8_to_iso_8859_3", nsp, own, pgEncUTF8, pgEncLATIN3, 4372, true},
		{4495, "iso_8859_3_to_utf8", nsp, own, pgEncLATIN3, pgEncUTF8, 4373, true},
		{4496, "utf8_to_iso_8859_4", nsp, own, pgEncUTF8, pgEncLATIN4, 4372, true},
		{4497, "iso_8859_4_to_utf8", nsp, own, pgEncLATIN4, pgEncUTF8, 4373, true},
		{4498, "utf8_to_iso_8859_9", nsp, own, pgEncUTF8, pgEncLATIN5, 4372, true},
		{4499, "iso_8859_9_to_utf8", nsp, own, pgEncLATIN5, pgEncUTF8, 4373, true},
		{4500, "utf8_to_iso_8859_10", nsp, own, pgEncUTF8, pgEncLATIN6, 4372, true},
		{4501, "iso_8859_10_to_utf8", nsp, own, pgEncLATIN6, pgEncUTF8, 4373, true},
		{4502, "utf8_to_iso_8859_13", nsp, own, pgEncUTF8, pgEncLATIN7, 4372, true},
		{4503, "iso_8859_13_to_utf8", nsp, own, pgEncLATIN7, pgEncUTF8, 4373, true},
		{4504, "utf8_to_iso_8859_14", nsp, own, pgEncUTF8, pgEncLATIN8, 4372, true},
		{4505, "iso_8859_14_to_utf8", nsp, own, pgEncLATIN8, pgEncUTF8, 4373, true},
		{4506, "utf8_to_iso_8859_15", nsp, own, pgEncUTF8, pgEncLATIN9, 4372, true},
		{4507, "iso_8859_15_to_utf8", nsp, own, pgEncLATIN9, pgEncUTF8, 4373, true},
		{4508, "utf8_to_iso_8859_16", nsp, own, pgEncUTF8, pgEncLATIN10, 4372, true},
		{4509, "iso_8859_16_to_utf8", nsp, own, pgEncLATIN10, pgEncUTF8, 4373, true},
		{4510, "utf8_to_iso_8859_5", nsp, own, pgEncUTF8, pgEncISO88595, 4372, true},
		{4511, "iso_8859_5_to_utf8", nsp, own, pgEncISO88595, pgEncUTF8, 4373, true},
		{4512, "utf8_to_iso_8859_6", nsp, own, pgEncUTF8, pgEncISO88596, 4372, true},
		{4513, "iso_8859_6_to_utf8", nsp, own, pgEncISO88596, pgEncUTF8, 4373, true},
		{4514, "utf8_to_iso_8859_7", nsp, own, pgEncUTF8, pgEncISO88597, 4372, true},
		{4515, "iso_8859_7_to_utf8", nsp, own, pgEncISO88597, pgEncUTF8, 4373, true},
		{4516, "utf8_to_iso_8859_8", nsp, own, pgEncUTF8, pgEncISO88598, 4372, true},
		{4517, "iso_8859_8_to_utf8", nsp, own, pgEncISO88598, pgEncUTF8, 4373, true},
		{4518, "iso_8859_1_to_utf8", nsp, own, pgEncLATIN1, pgEncUTF8, 4374, true},
		{4519, "utf8_to_iso_8859_1", nsp, own, pgEncUTF8, pgEncLATIN1, 4375, true},
		{4520, "johab_to_utf8", nsp, own, pgEncJOHAB, pgEncUTF8, 4376, true},
		{4521, "utf8_to_johab", nsp, own, pgEncUTF8, pgEncJOHAB, 4377, true},
		{4522, "sjis_to_utf8", nsp, own, pgEncSJIS, pgEncUTF8, 4378, true},
		{4523, "utf8_to_sjis", nsp, own, pgEncUTF8, pgEncSJIS, 4379, true},
		{4524, "uhc_to_utf8", nsp, own, pgEncUHC, pgEncUTF8, 4380, true},
		{4525, "utf8_to_uhc", nsp, own, pgEncUTF8, pgEncUHC, 4381, true},
		{4526, "euc_jis_2004_to_utf8", nsp, own, pgEncEUCJIS2004, pgEncUTF8, 4382, true},
		{4527, "utf8_to_euc_jis_2004", nsp, own, pgEncUTF8, pgEncEUCJIS2004, 4383, true},
		{4528, "shift_jis_2004_to_utf8", nsp, own, pgEncSHIFTJIS2004, pgEncUTF8, 4384, true},
		{4529, "utf8_to_shift_jis_2004", nsp, own, pgEncUTF8, pgEncSHIFTJIS2004, 4385, true},
		{4530, "euc_jis_2004_to_shift_jis_2004", nsp, own, pgEncEUCJIS2004, pgEncSHIFTJIS2004, 4386, true},
		{4531, "shift_jis_2004_to_euc_jis_2004", nsp, own, pgEncSHIFTJIS2004, pgEncEUCJIS2004, 4387, true},
	}
}

// bootstrapPgConversionTuples writes all 128 pg_conversion rows to
// base/{1,5}/2607 in the 8-column fixed-size layout.
// Returns TIDs keyed by conversion OID for index seeding.
func bootstrapPgConversionTuples(dataDir string) (map[uint32]heapTID, error) {
	cols := pgConversionColDefs()
	entries := pgConversionInitialEntries()
	rows := make([]executor.Row, len(entries))
	for i, e := range entries {
		rows[i] = pgConversionRow(e)
	}
	rawTIDs, err := writeMultiPageHeapRows(dataDir, "2607", cols, rows)
	if err != nil {
		return nil, fmt.Errorf("bootstrapPgConversionTuples: %w", err)
	}
	tidMap := make(map[uint32]heapTID, len(entries))
	for i, e := range entries {
		tidMap[e.OID] = rawTIDs[i]
	}
	return tidMap, nil
}
