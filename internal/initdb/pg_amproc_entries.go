package initdb

// pgAmprocInitialEntries returns all 714 rows of pg_amproc (OID 2603) from
// PG18 pg_amproc.dat. OIDs are assigned by the caller (baseOID + index).
func pgAmprocInitialEntries() []pgAmprocEntry {
	const baseOID uint32 = 7100
	out := []pgAmprocEntry{
		// btree/array_ops (family=397)
		{Family: 397, LeftType: 2277, RightType: 2277, Num: 1, Proc: 382}, // anyarray x anyarray: btarraycmp

		// btree/bit_ops (family=423)
		{Family: 423, LeftType: 1560, RightType: 1560, Num: 1, Proc: 1596}, // bit x bit: bitcmp
		{Family: 423, LeftType: 1560, RightType: 1560, Num: 4, Proc: 5051}, // bit x bit: btequalimage

		// btree/bool_ops (family=424)
		{Family: 424, LeftType: 16, RightType: 16, Num: 1, Proc: 1693}, // bool x bool: btboolcmp
		{Family: 424, LeftType: 16, RightType: 16, Num: 6, Proc: 6408}, // bool x bool: btboolskipsupport
		{Family: 424, LeftType: 16, RightType: 16, Num: 4, Proc: 5051}, // bool x bool: btequalimage

		// btree/bpchar_ops (family=426)
		{Family: 426, LeftType: 1042, RightType: 1042, Num: 1, Proc: 1078}, // bpchar x bpchar: bpcharcmp
		{Family: 426, LeftType: 1042, RightType: 1042, Num: 2, Proc: 3328}, // bpchar x bpchar: bpchar_sortsupport
		{Family: 426, LeftType: 1042, RightType: 1042, Num: 4, Proc: 5050}, // bpchar x bpchar: btvarstrequalimage

		// btree/bytea_ops (family=428)
		{Family: 428, LeftType: 17, RightType: 17, Num: 1, Proc: 1954}, // bytea x bytea: byteacmp
		{Family: 428, LeftType: 17, RightType: 17, Num: 2, Proc: 3331}, // bytea x bytea: bytea_sortsupport
		{Family: 428, LeftType: 17, RightType: 17, Num: 4, Proc: 5051}, // bytea x bytea: btequalimage

		// btree/char_ops (family=429)
		{Family: 429, LeftType: 18, RightType: 18, Num: 1, Proc: 358}, // char x char: btcharcmp
		{Family: 429, LeftType: 18, RightType: 18, Num: 4, Proc: 5051}, // char x char: btequalimage
		{Family: 429, LeftType: 18, RightType: 18, Num: 6, Proc: 6406}, // char x char: btcharskipsupport

		// btree/datetime_ops (family=434)
		{Family: 434, LeftType: 1082, RightType: 1082, Num: 1, Proc: 1092}, // date x date: date_cmp
		{Family: 434, LeftType: 1082, RightType: 1082, Num: 2, Proc: 3136}, // date x date: date_sortsupport
		{Family: 434, LeftType: 1082, RightType: 1082, Num: 4, Proc: 5051}, // date x date: btequalimage
		{Family: 434, LeftType: 1082, RightType: 1082, Num: 6, Proc: 6407}, // date x date: date_skipsupport
		{Family: 434, LeftType: 1082, RightType: 1114, Num: 1, Proc: 2344}, // date x timestamp: date_cmp_timestamp
		{Family: 434, LeftType: 1082, RightType: 1184, Num: 1, Proc: 2357}, // date x timestamptz: date_cmp_timestamptz
		{Family: 434, LeftType: 1114, RightType: 1114, Num: 1, Proc: 2045}, // timestamp x timestamp: timestamp_cmp
		{Family: 434, LeftType: 1114, RightType: 1114, Num: 2, Proc: 3137}, // timestamp x timestamp: timestamp_sortsupport
		{Family: 434, LeftType: 1114, RightType: 1114, Num: 4, Proc: 5051}, // timestamp x timestamp: btequalimage
		{Family: 434, LeftType: 1114, RightType: 1114, Num: 6, Proc: 6409}, // timestamp x timestamp: timestamp_skipsupport
		{Family: 434, LeftType: 1114, RightType: 1082, Num: 1, Proc: 2370}, // timestamp x date: timestamp_cmp_date
		{Family: 434, LeftType: 1114, RightType: 1184, Num: 1, Proc: 2526}, // timestamp x timestamptz: timestamp_cmp_timestamptz
		{Family: 434, LeftType: 1184, RightType: 1184, Num: 1, Proc: 1314}, // timestamptz x timestamptz: timestamptz_cmp
		{Family: 434, LeftType: 1184, RightType: 1184, Num: 2, Proc: 3137}, // timestamptz x timestamptz: timestamp_sortsupport
		{Family: 434, LeftType: 1184, RightType: 1184, Num: 4, Proc: 5051}, // timestamptz x timestamptz: btequalimage
		{Family: 434, LeftType: 1184, RightType: 1184, Num: 6, Proc: 6409}, // timestamptz x timestamptz: timestamp_skipsupport
		{Family: 434, LeftType: 1184, RightType: 1082, Num: 1, Proc: 2383}, // timestamptz x date: timestamptz_cmp_date
		{Family: 434, LeftType: 1184, RightType: 1114, Num: 1, Proc: 2533}, // timestamptz x timestamp: timestamptz_cmp_timestamp
		{Family: 434, LeftType: 1082, RightType: 1186, Num: 3, Proc: 4133}, // date x interval: in_range(date,date,interval,bool,bool)
		{Family: 434, LeftType: 1114, RightType: 1186, Num: 3, Proc: 4134}, // timestamp x interval: in_range(timestamp,timestamp,interval,bool,bool)
		{Family: 434, LeftType: 1184, RightType: 1186, Num: 3, Proc: 4135}, // timestamptz x interval: in_range(timestamptz,timestamptz,interval,bool,bool)

		// btree/float_ops (family=1970)
		{Family: 1970, LeftType: 700, RightType: 700, Num: 1, Proc: 354}, // float4 x float4: btfloat4cmp
		{Family: 1970, LeftType: 700, RightType: 700, Num: 2, Proc: 3132}, // float4 x float4: btfloat4sortsupport
		{Family: 1970, LeftType: 700, RightType: 701, Num: 1, Proc: 2194}, // float4 x float8: btfloat48cmp
		{Family: 1970, LeftType: 701, RightType: 701, Num: 1, Proc: 355}, // float8 x float8: btfloat8cmp
		{Family: 1970, LeftType: 701, RightType: 701, Num: 2, Proc: 3133}, // float8 x float8: btfloat8sortsupport
		{Family: 1970, LeftType: 701, RightType: 700, Num: 1, Proc: 2195}, // float8 x float4: btfloat84cmp
		{Family: 1970, LeftType: 701, RightType: 701, Num: 3, Proc: 4139}, // float8 x float8: in_range(float8,float8,float8,bool,bool)
		{Family: 1970, LeftType: 700, RightType: 701, Num: 3, Proc: 4140}, // float4 x float8: in_range(float4,float4,float8,bool,bool)

		// btree/network_ops (family=1974)
		{Family: 1974, LeftType: 869, RightType: 869, Num: 1, Proc: 926}, // inet x inet: network_cmp
		{Family: 1974, LeftType: 869, RightType: 869, Num: 2, Proc: 5033}, // inet x inet: network_sortsupport
		{Family: 1974, LeftType: 869, RightType: 869, Num: 4, Proc: 5051}, // inet x inet: btequalimage

		// btree/integer_ops (family=1976)
		{Family: 1976, LeftType: 21, RightType: 21, Num: 1, Proc: 350}, // int2 x int2: btint2cmp
		{Family: 1976, LeftType: 21, RightType: 21, Num: 2, Proc: 3129}, // int2 x int2: btint2sortsupport
		{Family: 1976, LeftType: 21, RightType: 21, Num: 4, Proc: 5051}, // int2 x int2: btequalimage
		{Family: 1976, LeftType: 21, RightType: 21, Num: 6, Proc: 6402}, // int2 x int2: btint2skipsupport
		{Family: 1976, LeftType: 21, RightType: 23, Num: 1, Proc: 2190}, // int2 x int4: btint24cmp
		{Family: 1976, LeftType: 21, RightType: 20, Num: 1, Proc: 2192}, // int2 x int8: btint28cmp
		{Family: 1976, LeftType: 21, RightType: 20, Num: 3, Proc: 4130}, // int2 x int8: in_range(int2,int2,int8,bool,bool)
		{Family: 1976, LeftType: 21, RightType: 23, Num: 3, Proc: 4131}, // int2 x int4: in_range(int2,int2,int4,bool,bool)
		{Family: 1976, LeftType: 21, RightType: 21, Num: 3, Proc: 4132}, // int2 x int2: in_range(int2,int2,int2,bool,bool)
		{Family: 1976, LeftType: 23, RightType: 23, Num: 1, Proc: 351}, // int4 x int4: btint4cmp
		{Family: 1976, LeftType: 23, RightType: 23, Num: 2, Proc: 3130}, // int4 x int4: btint4sortsupport
		{Family: 1976, LeftType: 23, RightType: 23, Num: 4, Proc: 5051}, // int4 x int4: btequalimage
		{Family: 1976, LeftType: 23, RightType: 23, Num: 6, Proc: 6403}, // int4 x int4: btint4skipsupport
		{Family: 1976, LeftType: 23, RightType: 20, Num: 1, Proc: 2188}, // int4 x int8: btint48cmp
		{Family: 1976, LeftType: 23, RightType: 21, Num: 1, Proc: 2191}, // int4 x int2: btint42cmp
		{Family: 1976, LeftType: 23, RightType: 20, Num: 3, Proc: 4127}, // int4 x int8: in_range(int4,int4,int8,bool,bool)
		{Family: 1976, LeftType: 23, RightType: 23, Num: 3, Proc: 4128}, // int4 x int4: in_range(int4,int4,int4,bool,bool)
		{Family: 1976, LeftType: 23, RightType: 21, Num: 3, Proc: 4129}, // int4 x int2: in_range(int4,int4,int2,bool,bool)
		{Family: 1976, LeftType: 20, RightType: 20, Num: 1, Proc: 842}, // int8 x int8: btint8cmp
		{Family: 1976, LeftType: 20, RightType: 20, Num: 2, Proc: 3131}, // int8 x int8: btint8sortsupport
		{Family: 1976, LeftType: 20, RightType: 20, Num: 4, Proc: 5051}, // int8 x int8: btequalimage
		{Family: 1976, LeftType: 20, RightType: 20, Num: 6, Proc: 6404}, // int8 x int8: btint8skipsupport
		{Family: 1976, LeftType: 20, RightType: 23, Num: 1, Proc: 2189}, // int8 x int4: btint84cmp
		{Family: 1976, LeftType: 20, RightType: 21, Num: 1, Proc: 2193}, // int8 x int2: btint82cmp
		{Family: 1976, LeftType: 20, RightType: 20, Num: 3, Proc: 4126}, // int8 x int8: in_range(int8,int8,int8,bool,bool)

		// btree/interval_ops (family=1982)
		{Family: 1982, LeftType: 1186, RightType: 1186, Num: 1, Proc: 1315}, // interval x interval: interval_cmp
		{Family: 1982, LeftType: 1186, RightType: 1186, Num: 3, Proc: 4136}, // interval x interval: in_range(interval,interval,interval,bool,bool)

		// btree/macaddr_ops (family=1984)
		{Family: 1984, LeftType: 829, RightType: 829, Num: 1, Proc: 836}, // macaddr x macaddr: macaddr_cmp
		{Family: 1984, LeftType: 829, RightType: 829, Num: 2, Proc: 3359}, // macaddr x macaddr: macaddr_sortsupport
		{Family: 1984, LeftType: 829, RightType: 829, Num: 4, Proc: 5051}, // macaddr x macaddr: btequalimage

		// btree/numeric_ops (family=1988)
		{Family: 1988, LeftType: 1700, RightType: 1700, Num: 1, Proc: 1769}, // numeric x numeric: numeric_cmp
		{Family: 1988, LeftType: 1700, RightType: 1700, Num: 2, Proc: 3283}, // numeric x numeric: numeric_sortsupport
		{Family: 1988, LeftType: 1700, RightType: 1700, Num: 3, Proc: 4141}, // numeric x numeric: in_range(numeric,numeric,numeric,bool,bool)

		// btree/oid_ops (family=1989)
		{Family: 1989, LeftType: 26, RightType: 26, Num: 1, Proc: 356}, // oid x oid: btoidcmp
		{Family: 1989, LeftType: 26, RightType: 26, Num: 2, Proc: 3134}, // oid x oid: btoidsortsupport
		{Family: 1989, LeftType: 26, RightType: 26, Num: 4, Proc: 5051}, // oid x oid: btequalimage
		{Family: 1989, LeftType: 26, RightType: 26, Num: 6, Proc: 6405}, // oid x oid: btoidskipsupport

		// btree/oidvector_ops (family=1991)
		{Family: 1991, LeftType: 30, RightType: 30, Num: 1, Proc: 404}, // oidvector x oidvector: btoidvectorcmp
		{Family: 1991, LeftType: 30, RightType: 30, Num: 4, Proc: 5051}, // oidvector x oidvector: btequalimage

		// btree/text_ops (family=1994)
		{Family: 1994, LeftType: 25, RightType: 25, Num: 1, Proc: 360}, // text x text: bttextcmp
		{Family: 1994, LeftType: 25, RightType: 25, Num: 2, Proc: 3255}, // text x text: bttextsortsupport
		{Family: 1994, LeftType: 25, RightType: 25, Num: 4, Proc: 5050}, // text x text: btvarstrequalimage
		{Family: 1994, LeftType: 19, RightType: 19, Num: 1, Proc: 359}, // name x name: btnamecmp
		{Family: 1994, LeftType: 19, RightType: 19, Num: 2, Proc: 3135}, // name x name: btnamesortsupport
		{Family: 1994, LeftType: 19, RightType: 19, Num: 4, Proc: 5050}, // name x name: btvarstrequalimage
		{Family: 1994, LeftType: 19, RightType: 25, Num: 1, Proc: 246}, // name x text: btnametextcmp
		{Family: 1994, LeftType: 25, RightType: 19, Num: 1, Proc: 253}, // text x name: bttextnamecmp

		// btree/time_ops (family=1996)
		{Family: 1996, LeftType: 1083, RightType: 1083, Num: 1, Proc: 1107}, // time x time: time_cmp
		{Family: 1996, LeftType: 1083, RightType: 1083, Num: 4, Proc: 5051}, // time x time: btequalimage
		{Family: 1996, LeftType: 1083, RightType: 1186, Num: 3, Proc: 4137}, // time x interval: in_range(time,time,interval,bool,bool)

		// btree/timetz_ops (family=2000)
		{Family: 2000, LeftType: 1266, RightType: 1266, Num: 1, Proc: 1358}, // timetz x timetz: timetz_cmp
		{Family: 2000, LeftType: 1266, RightType: 1266, Num: 4, Proc: 5051}, // timetz x timetz: btequalimage
		{Family: 2000, LeftType: 1266, RightType: 1186, Num: 3, Proc: 4138}, // timetz x interval: in_range(timetz,timetz,interval,bool,bool)

		// btree/varbit_ops (family=2002)
		{Family: 2002, LeftType: 1562, RightType: 1562, Num: 1, Proc: 1672}, // varbit x varbit: varbitcmp
		{Family: 2002, LeftType: 1562, RightType: 1562, Num: 4, Proc: 5051}, // varbit x varbit: btequalimage

		// btree/text_pattern_ops (family=2095)
		{Family: 2095, LeftType: 25, RightType: 25, Num: 1, Proc: 2166}, // text x text: bttext_pattern_cmp
		{Family: 2095, LeftType: 25, RightType: 25, Num: 2, Proc: 3332}, // text x text: bttext_pattern_sortsupport
		{Family: 2095, LeftType: 25, RightType: 25, Num: 4, Proc: 5051}, // text x text: btequalimage

		// btree/bpchar_pattern_ops (family=2097)
		{Family: 2097, LeftType: 1042, RightType: 1042, Num: 1, Proc: 2180}, // bpchar x bpchar: btbpchar_pattern_cmp
		{Family: 2097, LeftType: 1042, RightType: 1042, Num: 2, Proc: 3333}, // bpchar x bpchar: btbpchar_pattern_sortsupport
		{Family: 2097, LeftType: 1042, RightType: 1042, Num: 4, Proc: 5051}, // bpchar x bpchar: btequalimage

		// btree/money_ops (family=2099)
		{Family: 2099, LeftType: 790, RightType: 790, Num: 1, Proc: 377}, // money x money: cash_cmp
		{Family: 2099, LeftType: 790, RightType: 790, Num: 4, Proc: 5051}, // money x money: btequalimage

		// btree/tid_ops (family=2789)
		{Family: 2789, LeftType: 27, RightType: 27, Num: 1, Proc: 2794}, // tid x tid: bttidcmp
		{Family: 2789, LeftType: 27, RightType: 27, Num: 4, Proc: 5051}, // tid x tid: btequalimage

		// btree/uuid_ops (family=2968)
		{Family: 2968, LeftType: 2950, RightType: 2950, Num: 1, Proc: 2960}, // uuid x uuid: uuid_cmp
		{Family: 2968, LeftType: 2950, RightType: 2950, Num: 2, Proc: 3300}, // uuid x uuid: uuid_sortsupport
		{Family: 2968, LeftType: 2950, RightType: 2950, Num: 4, Proc: 5051}, // uuid x uuid: btequalimage
		{Family: 2968, LeftType: 2950, RightType: 2950, Num: 6, Proc: 6410}, // uuid x uuid: uuid_skipsupport

		// btree/record_ops (family=2994)
		{Family: 2994, LeftType: 2249, RightType: 2249, Num: 1, Proc: 2987}, // record x record: btrecordcmp

		// btree/record_image_ops (family=3194)
		{Family: 3194, LeftType: 2249, RightType: 2249, Num: 1, Proc: 3187}, // record x record: btrecordimagecmp

		// btree/pg_lsn_ops (family=3253)
		{Family: 3253, LeftType: 3220, RightType: 3220, Num: 1, Proc: 3251}, // pg_lsn x pg_lsn: pg_lsn_cmp
		{Family: 3253, LeftType: 3220, RightType: 3220, Num: 4, Proc: 5051}, // pg_lsn x pg_lsn: btequalimage

		// btree/macaddr8_ops (family=3371)
		{Family: 3371, LeftType: 774, RightType: 774, Num: 1, Proc: 4119}, // macaddr8 x macaddr8: macaddr8_cmp
		{Family: 3371, LeftType: 774, RightType: 774, Num: 4, Proc: 5051}, // macaddr8 x macaddr8: btequalimage

		// btree/enum_ops (family=3522)
		{Family: 3522, LeftType: 3500, RightType: 3500, Num: 1, Proc: 3514}, // anyenum x anyenum: enum_cmp
		{Family: 3522, LeftType: 3500, RightType: 3500, Num: 4, Proc: 5051}, // anyenum x anyenum: btequalimage

		// btree/tsvector_ops (family=3626)
		{Family: 3626, LeftType: 3614, RightType: 3614, Num: 1, Proc: 3622}, // tsvector x tsvector: tsvector_cmp

		// btree/tsquery_ops (family=3683)
		{Family: 3683, LeftType: 3615, RightType: 3615, Num: 1, Proc: 3668}, // tsquery x tsquery: tsquery_cmp

		// btree/range_ops (family=3901)
		{Family: 3901, LeftType: 3831, RightType: 3831, Num: 1, Proc: 3870}, // anyrange x anyrange: range_cmp
		{Family: 3901, LeftType: 3831, RightType: 3831, Num: 2, Proc: 6391}, // anyrange x anyrange: range_sortsupport

		// btree/multirange_ops (family=4199)
		{Family: 4199, LeftType: 4537, RightType: 4537, Num: 1, Proc: 4273}, // anymultirange x anymultirange: multirange_cmp

		// btree/jsonb_ops (family=4033)
		{Family: 4033, LeftType: 3802, RightType: 3802, Num: 1, Proc: 4044}, // jsonb x jsonb: jsonb_cmp

		// btree/xid8_ops (family=5067)
		{Family: 5067, LeftType: 5069, RightType: 5069, Num: 1, Proc: 5096}, // xid8 x xid8: xid8cmp
		{Family: 5067, LeftType: 5069, RightType: 5069, Num: 4, Proc: 5051}, // xid8 x xid8: btequalimage

		// hash/bpchar_ops (family=427)
		{Family: 427, LeftType: 1042, RightType: 1042, Num: 1, Proc: 1080}, // bpchar x bpchar: hashbpchar
		{Family: 427, LeftType: 1042, RightType: 1042, Num: 2, Proc: 972}, // bpchar x bpchar: hashbpcharextended

		// hash/char_ops (family=431)
		{Family: 431, LeftType: 18, RightType: 18, Num: 1, Proc: 454}, // char x char: hashchar
		{Family: 431, LeftType: 18, RightType: 18, Num: 2, Proc: 446}, // char x char: hashcharextended

		// hash/date_ops (family=435)
		{Family: 435, LeftType: 1082, RightType: 1082, Num: 1, Proc: 6415}, // date x date: hashdate
		{Family: 435, LeftType: 1082, RightType: 1082, Num: 2, Proc: 6416}, // date x date: hashdateextended

		// hash/array_ops (family=627)
		{Family: 627, LeftType: 2277, RightType: 2277, Num: 1, Proc: 626}, // anyarray x anyarray: hash_array
		{Family: 627, LeftType: 2277, RightType: 2277, Num: 2, Proc: 782}, // anyarray x anyarray: hash_array_extended

		// hash/float_ops (family=1971)
		{Family: 1971, LeftType: 700, RightType: 700, Num: 1, Proc: 451}, // float4 x float4: hashfloat4
		{Family: 1971, LeftType: 700, RightType: 700, Num: 2, Proc: 443}, // float4 x float4: hashfloat4extended
		{Family: 1971, LeftType: 701, RightType: 701, Num: 1, Proc: 452}, // float8 x float8: hashfloat8
		{Family: 1971, LeftType: 701, RightType: 701, Num: 2, Proc: 444}, // float8 x float8: hashfloat8extended

		// hash/network_ops (family=1975)
		{Family: 1975, LeftType: 869, RightType: 869, Num: 1, Proc: 422}, // inet x inet: hashinet
		{Family: 1975, LeftType: 869, RightType: 869, Num: 2, Proc: 779}, // inet x inet: hashinetextended

		// hash/integer_ops (family=1977)
		{Family: 1977, LeftType: 21, RightType: 21, Num: 1, Proc: 449}, // int2 x int2: hashint2
		{Family: 1977, LeftType: 21, RightType: 21, Num: 2, Proc: 441}, // int2 x int2: hashint2extended
		{Family: 1977, LeftType: 23, RightType: 23, Num: 1, Proc: 450}, // int4 x int4: hashint4
		{Family: 1977, LeftType: 23, RightType: 23, Num: 2, Proc: 425}, // int4 x int4: hashint4extended
		{Family: 1977, LeftType: 20, RightType: 20, Num: 1, Proc: 949}, // int8 x int8: hashint8
		{Family: 1977, LeftType: 20, RightType: 20, Num: 2, Proc: 442}, // int8 x int8: hashint8extended

		// hash/interval_ops (family=1983)
		{Family: 1983, LeftType: 1186, RightType: 1186, Num: 1, Proc: 1697}, // interval x interval: interval_hash
		{Family: 1983, LeftType: 1186, RightType: 1186, Num: 2, Proc: 3418}, // interval x interval: interval_hash_extended

		// hash/macaddr_ops (family=1985)
		{Family: 1985, LeftType: 829, RightType: 829, Num: 1, Proc: 399}, // macaddr x macaddr: hashmacaddr
		{Family: 1985, LeftType: 829, RightType: 829, Num: 2, Proc: 778}, // macaddr x macaddr: hashmacaddrextended

		// hash/oid_ops (family=1990)
		{Family: 1990, LeftType: 26, RightType: 26, Num: 1, Proc: 453}, // oid x oid: hashoid
		{Family: 1990, LeftType: 26, RightType: 26, Num: 2, Proc: 445}, // oid x oid: hashoidextended

		// hash/oidvector_ops (family=1992)
		{Family: 1992, LeftType: 30, RightType: 30, Num: 1, Proc: 457}, // oidvector x oidvector: hashoidvector
		{Family: 1992, LeftType: 30, RightType: 30, Num: 2, Proc: 776}, // oidvector x oidvector: hashoidvectorextended

		// hash/text_ops (family=1995)
		{Family: 1995, LeftType: 25, RightType: 25, Num: 1, Proc: 400}, // text x text: hashtext
		{Family: 1995, LeftType: 25, RightType: 25, Num: 2, Proc: 448}, // text x text: hashtextextended
		{Family: 1995, LeftType: 19, RightType: 19, Num: 1, Proc: 455}, // name x name: hashname
		{Family: 1995, LeftType: 19, RightType: 19, Num: 2, Proc: 447}, // name x name: hashnameextended

		// hash/time_ops (family=1997)
		{Family: 1997, LeftType: 1083, RightType: 1083, Num: 1, Proc: 1688}, // time x time: time_hash
		{Family: 1997, LeftType: 1083, RightType: 1083, Num: 2, Proc: 3409}, // time x time: time_hash_extended

		// hash/numeric_ops (family=1998)
		{Family: 1998, LeftType: 1700, RightType: 1700, Num: 1, Proc: 432}, // numeric x numeric: hash_numeric
		{Family: 1998, LeftType: 1700, RightType: 1700, Num: 2, Proc: 780}, // numeric x numeric: hash_numeric_extended

		// hash/timestamptz_ops (family=1999)
		{Family: 1999, LeftType: 1184, RightType: 1184, Num: 1, Proc: 6425}, // timestamptz x timestamptz: timestamptz_hash
		{Family: 1999, LeftType: 1184, RightType: 1184, Num: 2, Proc: 6426}, // timestamptz x timestamptz: timestamptz_hash_extended

		// hash/timetz_ops (family=2001)
		{Family: 2001, LeftType: 1266, RightType: 1266, Num: 1, Proc: 1696}, // timetz x timetz: timetz_hash
		{Family: 2001, LeftType: 1266, RightType: 1266, Num: 2, Proc: 3410}, // timetz x timetz: timetz_hash_extended

		// hash/timestamp_ops (family=2040)
		{Family: 2040, LeftType: 1114, RightType: 1114, Num: 1, Proc: 2039}, // timestamp x timestamp: timestamp_hash
		{Family: 2040, LeftType: 1114, RightType: 1114, Num: 2, Proc: 3411}, // timestamp x timestamp: timestamp_hash_extended

		// hash/bool_ops (family=2222)
		{Family: 2222, LeftType: 16, RightType: 16, Num: 1, Proc: 6417}, // bool x bool: hashbool
		{Family: 2222, LeftType: 16, RightType: 16, Num: 2, Proc: 6418}, // bool x bool: hashboolextended

		// hash/bytea_ops (family=2223)
		{Family: 2223, LeftType: 17, RightType: 17, Num: 1, Proc: 6413}, // bytea x bytea: hashbytea
		{Family: 2223, LeftType: 17, RightType: 17, Num: 2, Proc: 6414}, // bytea x bytea: hashbyteaextended

		// hash/xid_ops (family=2225)
		{Family: 2225, LeftType: 28, RightType: 28, Num: 1, Proc: 6419}, // xid x xid: hashxid
		{Family: 2225, LeftType: 28, RightType: 28, Num: 2, Proc: 6420}, // xid x xid: hashxidextended

		// hash/xid8_ops (family=5032)
		{Family: 5032, LeftType: 5069, RightType: 5069, Num: 1, Proc: 6421}, // xid8 x xid8: hashxid8
		{Family: 5032, LeftType: 5069, RightType: 5069, Num: 2, Proc: 6422}, // xid8 x xid8: hashxid8extended

		// hash/cid_ops (family=2226)
		{Family: 2226, LeftType: 29, RightType: 29, Num: 1, Proc: 6423}, // cid x cid: hashcid
		{Family: 2226, LeftType: 29, RightType: 29, Num: 2, Proc: 6424}, // cid x cid: hashcidextended

		// hash/tid_ops (family=2227)
		{Family: 2227, LeftType: 27, RightType: 27, Num: 1, Proc: 2233}, // tid x tid: hashtid
		{Family: 2227, LeftType: 27, RightType: 27, Num: 2, Proc: 2234}, // tid x tid: hashtidextended

		// hash/text_pattern_ops (family=2229)
		{Family: 2229, LeftType: 25, RightType: 25, Num: 1, Proc: 400}, // text x text: hashtext
		{Family: 2229, LeftType: 25, RightType: 25, Num: 2, Proc: 448}, // text x text: hashtextextended

		// hash/bpchar_pattern_ops (family=2231)
		{Family: 2231, LeftType: 1042, RightType: 1042, Num: 1, Proc: 1080}, // bpchar x bpchar: hashbpchar
		{Family: 2231, LeftType: 1042, RightType: 1042, Num: 2, Proc: 972}, // bpchar x bpchar: hashbpcharextended

		// hash/aclitem_ops (family=2235)
		{Family: 2235, LeftType: 1033, RightType: 1033, Num: 1, Proc: 329}, // aclitem x aclitem: hash_aclitem
		{Family: 2235, LeftType: 1033, RightType: 1033, Num: 2, Proc: 777}, // aclitem x aclitem: hash_aclitem_extended

		// hash/uuid_ops (family=2969)
		{Family: 2969, LeftType: 2950, RightType: 2950, Num: 1, Proc: 2963}, // uuid x uuid: uuid_hash
		{Family: 2969, LeftType: 2950, RightType: 2950, Num: 2, Proc: 3412}, // uuid x uuid: uuid_hash_extended

		// hash/record_ops (family=6194)
		{Family: 6194, LeftType: 2249, RightType: 2249, Num: 1, Proc: 6192}, // record x record: hash_record
		{Family: 6194, LeftType: 2249, RightType: 2249, Num: 2, Proc: 6193}, // record x record: hash_record_extended

		// hash/pg_lsn_ops (family=3254)
		{Family: 3254, LeftType: 3220, RightType: 3220, Num: 1, Proc: 3252}, // pg_lsn x pg_lsn: pg_lsn_hash
		{Family: 3254, LeftType: 3220, RightType: 3220, Num: 2, Proc: 3413}, // pg_lsn x pg_lsn: pg_lsn_hash_extended

		// hash/macaddr8_ops (family=3372)
		{Family: 3372, LeftType: 774, RightType: 774, Num: 1, Proc: 328}, // macaddr8 x macaddr8: hashmacaddr8
		{Family: 3372, LeftType: 774, RightType: 774, Num: 2, Proc: 781}, // macaddr8 x macaddr8: hashmacaddr8extended

		// hash/enum_ops (family=3523)
		{Family: 3523, LeftType: 3500, RightType: 3500, Num: 1, Proc: 3515}, // anyenum x anyenum: hashenum
		{Family: 3523, LeftType: 3500, RightType: 3500, Num: 2, Proc: 3414}, // anyenum x anyenum: hashenumextended

		// hash/range_ops (family=3903)
		{Family: 3903, LeftType: 3831, RightType: 3831, Num: 1, Proc: 3902}, // anyrange x anyrange: hash_range
		{Family: 3903, LeftType: 3831, RightType: 3831, Num: 2, Proc: 3417}, // anyrange x anyrange: hash_range_extended

		// hash/multirange_ops (family=4225)
		{Family: 4225, LeftType: 4537, RightType: 4537, Num: 1, Proc: 4278}, // anymultirange x anymultirange: hash_multirange
		{Family: 4225, LeftType: 4537, RightType: 4537, Num: 2, Proc: 4279}, // anymultirange x anymultirange: hash_multirange_extended

		// hash/jsonb_ops (family=4034)
		{Family: 4034, LeftType: 3802, RightType: 3802, Num: 1, Proc: 4045}, // jsonb x jsonb: jsonb_hash
		{Family: 4034, LeftType: 3802, RightType: 3802, Num: 2, Proc: 3416}, // jsonb x jsonb: jsonb_hash_extended

		// gist/point_ops (family=1029)
		{Family: 1029, LeftType: 600, RightType: 600, Num: 1, Proc: 2179}, // point x point: gist_point_consistent
		{Family: 1029, LeftType: 600, RightType: 600, Num: 2, Proc: 2583}, // point x point: gist_box_union
		{Family: 1029, LeftType: 600, RightType: 600, Num: 3, Proc: 1030}, // point x point: gist_point_compress
		{Family: 1029, LeftType: 600, RightType: 600, Num: 5, Proc: 2581}, // point x point: gist_box_penalty
		{Family: 1029, LeftType: 600, RightType: 600, Num: 6, Proc: 2582}, // point x point: gist_box_picksplit
		{Family: 1029, LeftType: 600, RightType: 600, Num: 7, Proc: 2584}, // point x point: gist_box_same
		{Family: 1029, LeftType: 600, RightType: 600, Num: 8, Proc: 3064}, // point x point: gist_point_distance
		{Family: 1029, LeftType: 600, RightType: 600, Num: 9, Proc: 3282}, // point x point: gist_point_fetch
		{Family: 1029, LeftType: 600, RightType: 600, Num: 11, Proc: 3435}, // point x point: gist_point_sortsupport

		// gist/box_ops (family=2593)
		{Family: 2593, LeftType: 603, RightType: 603, Num: 1, Proc: 2578}, // box x box: gist_box_consistent
		{Family: 2593, LeftType: 603, RightType: 603, Num: 2, Proc: 2583}, // box x box: gist_box_union
		{Family: 2593, LeftType: 603, RightType: 603, Num: 5, Proc: 2581}, // box x box: gist_box_penalty
		{Family: 2593, LeftType: 603, RightType: 603, Num: 6, Proc: 2582}, // box x box: gist_box_picksplit
		{Family: 2593, LeftType: 603, RightType: 603, Num: 7, Proc: 2584}, // box x box: gist_box_same
		{Family: 2593, LeftType: 603, RightType: 603, Num: 8, Proc: 3998}, // box x box: gist_box_distance
		{Family: 2593, LeftType: 2276, RightType: 2276, Num: 12, Proc: 6347}, // any x any: gist_translate_cmptype_common

		// gist/poly_ops (family=2594)
		{Family: 2594, LeftType: 604, RightType: 604, Num: 1, Proc: 2585}, // polygon x polygon: gist_poly_consistent
		{Family: 2594, LeftType: 604, RightType: 604, Num: 2, Proc: 2583}, // polygon x polygon: gist_box_union
		{Family: 2594, LeftType: 604, RightType: 604, Num: 3, Proc: 2586}, // polygon x polygon: gist_poly_compress
		{Family: 2594, LeftType: 604, RightType: 604, Num: 5, Proc: 2581}, // polygon x polygon: gist_box_penalty
		{Family: 2594, LeftType: 604, RightType: 604, Num: 6, Proc: 2582}, // polygon x polygon: gist_box_picksplit
		{Family: 2594, LeftType: 604, RightType: 604, Num: 7, Proc: 2584}, // polygon x polygon: gist_box_same
		{Family: 2594, LeftType: 604, RightType: 604, Num: 8, Proc: 3288}, // polygon x polygon: gist_poly_distance
		{Family: 2594, LeftType: 2276, RightType: 2276, Num: 12, Proc: 6347}, // any x any: gist_translate_cmptype_common

		// gist/circle_ops (family=2595)
		{Family: 2595, LeftType: 718, RightType: 718, Num: 1, Proc: 2591}, // circle x circle: gist_circle_consistent
		{Family: 2595, LeftType: 718, RightType: 718, Num: 2, Proc: 2583}, // circle x circle: gist_box_union
		{Family: 2595, LeftType: 718, RightType: 718, Num: 3, Proc: 2592}, // circle x circle: gist_circle_compress
		{Family: 2595, LeftType: 718, RightType: 718, Num: 5, Proc: 2581}, // circle x circle: gist_box_penalty
		{Family: 2595, LeftType: 718, RightType: 718, Num: 6, Proc: 2582}, // circle x circle: gist_box_picksplit
		{Family: 2595, LeftType: 718, RightType: 718, Num: 7, Proc: 2584}, // circle x circle: gist_box_same
		{Family: 2595, LeftType: 718, RightType: 718, Num: 8, Proc: 3280}, // circle x circle: gist_circle_distance
		{Family: 2595, LeftType: 2276, RightType: 2276, Num: 12, Proc: 6347}, // any x any: gist_translate_cmptype_common

		// gist/tsvector_ops (family=3655)
		{Family: 3655, LeftType: 3614, RightType: 3614, Num: 1, Proc: 3654}, // tsvector x tsvector: gtsvector_consistent(internal,tsvector,int2,oid,internal)
		{Family: 3655, LeftType: 3614, RightType: 3614, Num: 2, Proc: 3651}, // tsvector x tsvector: gtsvector_union
		{Family: 3655, LeftType: 3614, RightType: 3614, Num: 3, Proc: 3648}, // tsvector x tsvector: gtsvector_compress
		{Family: 3655, LeftType: 3614, RightType: 3614, Num: 4, Proc: 3649}, // tsvector x tsvector: gtsvector_decompress
		{Family: 3655, LeftType: 3614, RightType: 3614, Num: 5, Proc: 3653}, // tsvector x tsvector: gtsvector_penalty
		{Family: 3655, LeftType: 3614, RightType: 3614, Num: 6, Proc: 3650}, // tsvector x tsvector: gtsvector_picksplit
		{Family: 3655, LeftType: 3614, RightType: 3614, Num: 7, Proc: 3652}, // tsvector x tsvector: gtsvector_same
		{Family: 3655, LeftType: 3614, RightType: 3614, Num: 10, Proc: 3434}, // tsvector x tsvector: gtsvector_options

		// gist/tsquery_ops (family=3702)
		{Family: 3702, LeftType: 3615, RightType: 3615, Num: 1, Proc: 3701}, // tsquery x tsquery: gtsquery_consistent(internal,tsquery,int2,oid,internal)
		{Family: 3702, LeftType: 3615, RightType: 3615, Num: 2, Proc: 3698}, // tsquery x tsquery: gtsquery_union
		{Family: 3702, LeftType: 3615, RightType: 3615, Num: 3, Proc: 3695}, // tsquery x tsquery: gtsquery_compress
		{Family: 3702, LeftType: 3615, RightType: 3615, Num: 5, Proc: 3700}, // tsquery x tsquery: gtsquery_penalty
		{Family: 3702, LeftType: 3615, RightType: 3615, Num: 6, Proc: 3697}, // tsquery x tsquery: gtsquery_picksplit
		{Family: 3702, LeftType: 3615, RightType: 3615, Num: 7, Proc: 3699}, // tsquery x tsquery: gtsquery_same

		// gist/range_ops (family=3919)
		{Family: 3919, LeftType: 3831, RightType: 3831, Num: 1, Proc: 3875}, // anyrange x anyrange: range_gist_consistent
		{Family: 3919, LeftType: 3831, RightType: 3831, Num: 2, Proc: 3876}, // anyrange x anyrange: range_gist_union
		{Family: 3919, LeftType: 3831, RightType: 3831, Num: 5, Proc: 3879}, // anyrange x anyrange: range_gist_penalty
		{Family: 3919, LeftType: 3831, RightType: 3831, Num: 6, Proc: 3880}, // anyrange x anyrange: range_gist_picksplit
		{Family: 3919, LeftType: 3831, RightType: 3831, Num: 7, Proc: 3881}, // anyrange x anyrange: range_gist_same
		{Family: 3919, LeftType: 3831, RightType: 3831, Num: 11, Proc: 6391}, // anyrange x anyrange: range_sortsupport
		{Family: 3919, LeftType: 2276, RightType: 2276, Num: 12, Proc: 6347}, // any x any: gist_translate_cmptype_common

		// gist/network_ops (family=3550)
		{Family: 3550, LeftType: 869, RightType: 869, Num: 1, Proc: 3553}, // inet x inet: inet_gist_consistent
		{Family: 3550, LeftType: 869, RightType: 869, Num: 2, Proc: 3554}, // inet x inet: inet_gist_union
		{Family: 3550, LeftType: 869, RightType: 869, Num: 3, Proc: 3555}, // inet x inet: inet_gist_compress
		{Family: 3550, LeftType: 869, RightType: 869, Num: 5, Proc: 3557}, // inet x inet: inet_gist_penalty
		{Family: 3550, LeftType: 869, RightType: 869, Num: 6, Proc: 3558}, // inet x inet: inet_gist_picksplit
		{Family: 3550, LeftType: 869, RightType: 869, Num: 7, Proc: 3559}, // inet x inet: inet_gist_same
		{Family: 3550, LeftType: 869, RightType: 869, Num: 9, Proc: 3573}, // inet x inet: inet_gist_fetch
		{Family: 3550, LeftType: 2276, RightType: 2276, Num: 12, Proc: 6347}, // any x any: gist_translate_cmptype_common

		// gist/multirange_ops (family=6158)
		{Family: 6158, LeftType: 4537, RightType: 4537, Num: 1, Proc: 6154}, // anymultirange x anymultirange: multirange_gist_consistent
		{Family: 6158, LeftType: 4537, RightType: 4537, Num: 2, Proc: 3876}, // anymultirange x anymultirange: range_gist_union
		{Family: 6158, LeftType: 4537, RightType: 4537, Num: 3, Proc: 6156}, // anymultirange x anymultirange: multirange_gist_compress
		{Family: 6158, LeftType: 4537, RightType: 4537, Num: 5, Proc: 3879}, // anymultirange x anymultirange: range_gist_penalty
		{Family: 6158, LeftType: 4537, RightType: 4537, Num: 6, Proc: 3880}, // anymultirange x anymultirange: range_gist_picksplit
		{Family: 6158, LeftType: 4537, RightType: 4537, Num: 7, Proc: 3881}, // anymultirange x anymultirange: range_gist_same
		{Family: 6158, LeftType: 2276, RightType: 2276, Num: 12, Proc: 6347}, // any x any: gist_translate_cmptype_common

		// gin/array_ops (family=2745)
		{Family: 2745, LeftType: 2277, RightType: 2277, Num: 2, Proc: 2743}, // anyarray x anyarray: ginarrayextract(anyarray,internal,internal)
		{Family: 2745, LeftType: 2277, RightType: 2277, Num: 3, Proc: 2774}, // anyarray x anyarray: ginqueryarrayextract
		{Family: 2745, LeftType: 2277, RightType: 2277, Num: 4, Proc: 2744}, // anyarray x anyarray: ginarrayconsistent
		{Family: 2745, LeftType: 2277, RightType: 2277, Num: 6, Proc: 3920}, // anyarray x anyarray: ginarraytriconsistent

		// gin/tsvector_ops (family=3659)
		{Family: 3659, LeftType: 3614, RightType: 3614, Num: 1, Proc: 3724}, // tsvector x tsvector: gin_cmp_tslexeme
		{Family: 3659, LeftType: 3614, RightType: 3614, Num: 2, Proc: 3656}, // tsvector x tsvector: gin_extract_tsvector(tsvector,internal,internal)
		{Family: 3659, LeftType: 3614, RightType: 3614, Num: 3, Proc: 3657}, // tsvector x tsvector: gin_extract_tsquery(tsvector,internal,int2,internal,internal,internal,internal)
		{Family: 3659, LeftType: 3614, RightType: 3614, Num: 4, Proc: 3658}, // tsvector x tsvector: gin_tsquery_consistent(internal,int2,tsvector,int4,internal,internal,internal,internal)
		{Family: 3659, LeftType: 3614, RightType: 3614, Num: 5, Proc: 2700}, // tsvector x tsvector: gin_cmp_prefix
		{Family: 3659, LeftType: 3614, RightType: 3614, Num: 6, Proc: 3921}, // tsvector x tsvector: gin_tsquery_triconsistent

		// gin/jsonb_ops (family=4036)
		{Family: 4036, LeftType: 3802, RightType: 3802, Num: 1, Proc: 3480}, // jsonb x jsonb: gin_compare_jsonb
		{Family: 4036, LeftType: 3802, RightType: 3802, Num: 2, Proc: 3482}, // jsonb x jsonb: gin_extract_jsonb
		{Family: 4036, LeftType: 3802, RightType: 3802, Num: 3, Proc: 3483}, // jsonb x jsonb: gin_extract_jsonb_query
		{Family: 4036, LeftType: 3802, RightType: 3802, Num: 4, Proc: 3484}, // jsonb x jsonb: gin_consistent_jsonb
		{Family: 4036, LeftType: 3802, RightType: 3802, Num: 6, Proc: 3488}, // jsonb x jsonb: gin_triconsistent_jsonb

		// gin/jsonb_path_ops (family=4037)
		{Family: 4037, LeftType: 3802, RightType: 3802, Num: 1, Proc: 351}, // jsonb x jsonb: btint4cmp
		{Family: 4037, LeftType: 3802, RightType: 3802, Num: 2, Proc: 3485}, // jsonb x jsonb: gin_extract_jsonb_path
		{Family: 4037, LeftType: 3802, RightType: 3802, Num: 3, Proc: 3486}, // jsonb x jsonb: gin_extract_jsonb_query_path
		{Family: 4037, LeftType: 3802, RightType: 3802, Num: 4, Proc: 3487}, // jsonb x jsonb: gin_consistent_jsonb_path
		{Family: 4037, LeftType: 3802, RightType: 3802, Num: 6, Proc: 3489}, // jsonb x jsonb: gin_triconsistent_jsonb_path

		// spgist/range_ops (family=3474)
		{Family: 3474, LeftType: 3831, RightType: 3831, Num: 1, Proc: 3469}, // anyrange x anyrange: spg_range_quad_config
		{Family: 3474, LeftType: 3831, RightType: 3831, Num: 2, Proc: 3470}, // anyrange x anyrange: spg_range_quad_choose
		{Family: 3474, LeftType: 3831, RightType: 3831, Num: 3, Proc: 3471}, // anyrange x anyrange: spg_range_quad_picksplit
		{Family: 3474, LeftType: 3831, RightType: 3831, Num: 4, Proc: 3472}, // anyrange x anyrange: spg_range_quad_inner_consistent
		{Family: 3474, LeftType: 3831, RightType: 3831, Num: 5, Proc: 3473}, // anyrange x anyrange: spg_range_quad_leaf_consistent

		// spgist/network_ops (family=3794)
		{Family: 3794, LeftType: 869, RightType: 869, Num: 1, Proc: 3795}, // inet x inet: inet_spg_config
		{Family: 3794, LeftType: 869, RightType: 869, Num: 2, Proc: 3796}, // inet x inet: inet_spg_choose
		{Family: 3794, LeftType: 869, RightType: 869, Num: 3, Proc: 3797}, // inet x inet: inet_spg_picksplit
		{Family: 3794, LeftType: 869, RightType: 869, Num: 4, Proc: 3798}, // inet x inet: inet_spg_inner_consistent
		{Family: 3794, LeftType: 869, RightType: 869, Num: 5, Proc: 3799}, // inet x inet: inet_spg_leaf_consistent

		// spgist/quad_point_ops (family=4015)
		{Family: 4015, LeftType: 600, RightType: 600, Num: 1, Proc: 4018}, // point x point: spg_quad_config
		{Family: 4015, LeftType: 600, RightType: 600, Num: 2, Proc: 4019}, // point x point: spg_quad_choose
		{Family: 4015, LeftType: 600, RightType: 600, Num: 3, Proc: 4020}, // point x point: spg_quad_picksplit
		{Family: 4015, LeftType: 600, RightType: 600, Num: 4, Proc: 4021}, // point x point: spg_quad_inner_consistent
		{Family: 4015, LeftType: 600, RightType: 600, Num: 5, Proc: 4022}, // point x point: spg_quad_leaf_consistent

		// spgist/kd_point_ops (family=4016)
		{Family: 4016, LeftType: 600, RightType: 600, Num: 1, Proc: 4023}, // point x point: spg_kd_config
		{Family: 4016, LeftType: 600, RightType: 600, Num: 2, Proc: 4024}, // point x point: spg_kd_choose
		{Family: 4016, LeftType: 600, RightType: 600, Num: 3, Proc: 4025}, // point x point: spg_kd_picksplit
		{Family: 4016, LeftType: 600, RightType: 600, Num: 4, Proc: 4026}, // point x point: spg_kd_inner_consistent
		{Family: 4016, LeftType: 600, RightType: 600, Num: 5, Proc: 4022}, // point x point: spg_quad_leaf_consistent

		// spgist/text_ops (family=4017)
		{Family: 4017, LeftType: 25, RightType: 25, Num: 1, Proc: 4027}, // text x text: spg_text_config
		{Family: 4017, LeftType: 25, RightType: 25, Num: 2, Proc: 4028}, // text x text: spg_text_choose
		{Family: 4017, LeftType: 25, RightType: 25, Num: 3, Proc: 4029}, // text x text: spg_text_picksplit
		{Family: 4017, LeftType: 25, RightType: 25, Num: 4, Proc: 4030}, // text x text: spg_text_inner_consistent
		{Family: 4017, LeftType: 25, RightType: 25, Num: 5, Proc: 4031}, // text x text: spg_text_leaf_consistent

		// spgist/box_ops (family=5000)
		{Family: 5000, LeftType: 603, RightType: 603, Num: 1, Proc: 5012}, // box x box: spg_box_quad_config
		{Family: 5000, LeftType: 603, RightType: 603, Num: 2, Proc: 5013}, // box x box: spg_box_quad_choose
		{Family: 5000, LeftType: 603, RightType: 603, Num: 3, Proc: 5014}, // box x box: spg_box_quad_picksplit
		{Family: 5000, LeftType: 603, RightType: 603, Num: 4, Proc: 5015}, // box x box: spg_box_quad_inner_consistent
		{Family: 5000, LeftType: 603, RightType: 603, Num: 5, Proc: 5016}, // box x box: spg_box_quad_leaf_consistent

		// spgist/poly_ops (family=5008)
		{Family: 5008, LeftType: 604, RightType: 604, Num: 1, Proc: 5010}, // polygon x polygon: spg_bbox_quad_config
		{Family: 5008, LeftType: 604, RightType: 604, Num: 2, Proc: 5013}, // polygon x polygon: spg_box_quad_choose
		{Family: 5008, LeftType: 604, RightType: 604, Num: 3, Proc: 5014}, // polygon x polygon: spg_box_quad_picksplit
		{Family: 5008, LeftType: 604, RightType: 604, Num: 4, Proc: 5015}, // polygon x polygon: spg_box_quad_inner_consistent
		{Family: 5008, LeftType: 604, RightType: 604, Num: 5, Proc: 5016}, // polygon x polygon: spg_box_quad_leaf_consistent
		{Family: 5008, LeftType: 604, RightType: 604, Num: 6, Proc: 5011}, // polygon x polygon: spg_poly_quad_compress

		// brin/bytea_minmax_ops (family=4064)
		{Family: 4064, LeftType: 17, RightType: 17, Num: 1, Proc: 3383}, // bytea x bytea: brin_minmax_opcinfo
		{Family: 4064, LeftType: 17, RightType: 17, Num: 2, Proc: 3384}, // bytea x bytea: brin_minmax_add_value
		{Family: 4064, LeftType: 17, RightType: 17, Num: 3, Proc: 3385}, // bytea x bytea: brin_minmax_consistent
		{Family: 4064, LeftType: 17, RightType: 17, Num: 4, Proc: 3386}, // bytea x bytea: brin_minmax_union

		// brin/bytea_bloom_ops (family=4578)
		{Family: 4578, LeftType: 17, RightType: 17, Num: 1, Proc: 4591}, // bytea x bytea: brin_bloom_opcinfo
		{Family: 4578, LeftType: 17, RightType: 17, Num: 2, Proc: 4592}, // bytea x bytea: brin_bloom_add_value
		{Family: 4578, LeftType: 17, RightType: 17, Num: 3, Proc: 4593}, // bytea x bytea: brin_bloom_consistent
		{Family: 4578, LeftType: 17, RightType: 17, Num: 4, Proc: 4594}, // bytea x bytea: brin_bloom_union
		{Family: 4578, LeftType: 17, RightType: 17, Num: 5, Proc: 4595}, // bytea x bytea: brin_bloom_options
		{Family: 4578, LeftType: 17, RightType: 17, Num: 11, Proc: 6413}, // bytea x bytea: hashbytea

		// brin/char_minmax_ops (family=4062)
		{Family: 4062, LeftType: 18, RightType: 18, Num: 1, Proc: 3383}, // char x char: brin_minmax_opcinfo
		{Family: 4062, LeftType: 18, RightType: 18, Num: 2, Proc: 3384}, // char x char: brin_minmax_add_value
		{Family: 4062, LeftType: 18, RightType: 18, Num: 3, Proc: 3385}, // char x char: brin_minmax_consistent
		{Family: 4062, LeftType: 18, RightType: 18, Num: 4, Proc: 3386}, // char x char: brin_minmax_union

		// brin/char_bloom_ops (family=4577)
		{Family: 4577, LeftType: 18, RightType: 18, Num: 1, Proc: 4591}, // char x char: brin_bloom_opcinfo
		{Family: 4577, LeftType: 18, RightType: 18, Num: 2, Proc: 4592}, // char x char: brin_bloom_add_value
		{Family: 4577, LeftType: 18, RightType: 18, Num: 3, Proc: 4593}, // char x char: brin_bloom_consistent
		{Family: 4577, LeftType: 18, RightType: 18, Num: 4, Proc: 4594}, // char x char: brin_bloom_union
		{Family: 4577, LeftType: 18, RightType: 18, Num: 5, Proc: 4595}, // char x char: brin_bloom_options
		{Family: 4577, LeftType: 18, RightType: 18, Num: 11, Proc: 454}, // char x char: hashchar

		// brin/name_minmax_ops (family=4065)
		{Family: 4065, LeftType: 19, RightType: 19, Num: 1, Proc: 3383}, // name x name: brin_minmax_opcinfo
		{Family: 4065, LeftType: 19, RightType: 19, Num: 2, Proc: 3384}, // name x name: brin_minmax_add_value
		{Family: 4065, LeftType: 19, RightType: 19, Num: 3, Proc: 3385}, // name x name: brin_minmax_consistent
		{Family: 4065, LeftType: 19, RightType: 19, Num: 4, Proc: 3386}, // name x name: brin_minmax_union

		// brin/name_bloom_ops (family=4579)
		{Family: 4579, LeftType: 19, RightType: 19, Num: 1, Proc: 4591}, // name x name: brin_bloom_opcinfo
		{Family: 4579, LeftType: 19, RightType: 19, Num: 2, Proc: 4592}, // name x name: brin_bloom_add_value
		{Family: 4579, LeftType: 19, RightType: 19, Num: 3, Proc: 4593}, // name x name: brin_bloom_consistent
		{Family: 4579, LeftType: 19, RightType: 19, Num: 4, Proc: 4594}, // name x name: brin_bloom_union
		{Family: 4579, LeftType: 19, RightType: 19, Num: 5, Proc: 4595}, // name x name: brin_bloom_options
		{Family: 4579, LeftType: 19, RightType: 19, Num: 11, Proc: 455}, // name x name: hashname

		// brin/integer_minmax_ops (family=4054)
		{Family: 4054, LeftType: 20, RightType: 20, Num: 1, Proc: 3383}, // int8 x int8: brin_minmax_opcinfo
		{Family: 4054, LeftType: 20, RightType: 20, Num: 2, Proc: 3384}, // int8 x int8: brin_minmax_add_value
		{Family: 4054, LeftType: 20, RightType: 20, Num: 3, Proc: 3385}, // int8 x int8: brin_minmax_consistent
		{Family: 4054, LeftType: 20, RightType: 20, Num: 4, Proc: 3386}, // int8 x int8: brin_minmax_union
		{Family: 4054, LeftType: 21, RightType: 21, Num: 1, Proc: 3383}, // int2 x int2: brin_minmax_opcinfo
		{Family: 4054, LeftType: 21, RightType: 21, Num: 2, Proc: 3384}, // int2 x int2: brin_minmax_add_value
		{Family: 4054, LeftType: 21, RightType: 21, Num: 3, Proc: 3385}, // int2 x int2: brin_minmax_consistent
		{Family: 4054, LeftType: 21, RightType: 21, Num: 4, Proc: 3386}, // int2 x int2: brin_minmax_union
		{Family: 4054, LeftType: 23, RightType: 23, Num: 1, Proc: 3383}, // int4 x int4: brin_minmax_opcinfo
		{Family: 4054, LeftType: 23, RightType: 23, Num: 2, Proc: 3384}, // int4 x int4: brin_minmax_add_value
		{Family: 4054, LeftType: 23, RightType: 23, Num: 3, Proc: 3385}, // int4 x int4: brin_minmax_consistent
		{Family: 4054, LeftType: 23, RightType: 23, Num: 4, Proc: 3386}, // int4 x int4: brin_minmax_union

		// brin/integer_minmax_multi_ops (family=4602)
		{Family: 4602, LeftType: 21, RightType: 21, Num: 1, Proc: 4616}, // int2 x int2: brin_minmax_multi_opcinfo
		{Family: 4602, LeftType: 21, RightType: 21, Num: 2, Proc: 4617}, // int2 x int2: brin_minmax_multi_add_value
		{Family: 4602, LeftType: 21, RightType: 21, Num: 3, Proc: 4618}, // int2 x int2: brin_minmax_multi_consistent
		{Family: 4602, LeftType: 21, RightType: 21, Num: 4, Proc: 4619}, // int2 x int2: brin_minmax_multi_union
		{Family: 4602, LeftType: 21, RightType: 21, Num: 5, Proc: 4620}, // int2 x int2: brin_minmax_multi_options
		{Family: 4602, LeftType: 21, RightType: 21, Num: 11, Proc: 4621}, // int2 x int2: brin_minmax_multi_distance_int2
		{Family: 4602, LeftType: 23, RightType: 23, Num: 1, Proc: 4616}, // int4 x int4: brin_minmax_multi_opcinfo
		{Family: 4602, LeftType: 23, RightType: 23, Num: 2, Proc: 4617}, // int4 x int4: brin_minmax_multi_add_value
		{Family: 4602, LeftType: 23, RightType: 23, Num: 3, Proc: 4618}, // int4 x int4: brin_minmax_multi_consistent
		{Family: 4602, LeftType: 23, RightType: 23, Num: 4, Proc: 4619}, // int4 x int4: brin_minmax_multi_union
		{Family: 4602, LeftType: 23, RightType: 23, Num: 5, Proc: 4620}, // int4 x int4: brin_minmax_multi_options
		{Family: 4602, LeftType: 23, RightType: 23, Num: 11, Proc: 4622}, // int4 x int4: brin_minmax_multi_distance_int4
		{Family: 4602, LeftType: 20, RightType: 20, Num: 1, Proc: 4616}, // int8 x int8: brin_minmax_multi_opcinfo
		{Family: 4602, LeftType: 20, RightType: 20, Num: 2, Proc: 4617}, // int8 x int8: brin_minmax_multi_add_value
		{Family: 4602, LeftType: 20, RightType: 20, Num: 3, Proc: 4618}, // int8 x int8: brin_minmax_multi_consistent
		{Family: 4602, LeftType: 20, RightType: 20, Num: 4, Proc: 4619}, // int8 x int8: brin_minmax_multi_union
		{Family: 4602, LeftType: 20, RightType: 20, Num: 5, Proc: 4620}, // int8 x int8: brin_minmax_multi_options
		{Family: 4602, LeftType: 20, RightType: 20, Num: 11, Proc: 4623}, // int8 x int8: brin_minmax_multi_distance_int8

		// brin/integer_bloom_ops (family=4572)
		{Family: 4572, LeftType: 20, RightType: 20, Num: 1, Proc: 4591}, // int8 x int8: brin_bloom_opcinfo
		{Family: 4572, LeftType: 20, RightType: 20, Num: 2, Proc: 4592}, // int8 x int8: brin_bloom_add_value
		{Family: 4572, LeftType: 20, RightType: 20, Num: 3, Proc: 4593}, // int8 x int8: brin_bloom_consistent
		{Family: 4572, LeftType: 20, RightType: 20, Num: 4, Proc: 4594}, // int8 x int8: brin_bloom_union
		{Family: 4572, LeftType: 20, RightType: 20, Num: 5, Proc: 4595}, // int8 x int8: brin_bloom_options
		{Family: 4572, LeftType: 20, RightType: 20, Num: 11, Proc: 949}, // int8 x int8: hashint8
		{Family: 4572, LeftType: 21, RightType: 21, Num: 1, Proc: 4591}, // int2 x int2: brin_bloom_opcinfo
		{Family: 4572, LeftType: 21, RightType: 21, Num: 2, Proc: 4592}, // int2 x int2: brin_bloom_add_value
		{Family: 4572, LeftType: 21, RightType: 21, Num: 3, Proc: 4593}, // int2 x int2: brin_bloom_consistent
		{Family: 4572, LeftType: 21, RightType: 21, Num: 4, Proc: 4594}, // int2 x int2: brin_bloom_union
		{Family: 4572, LeftType: 21, RightType: 21, Num: 5, Proc: 4595}, // int2 x int2: brin_bloom_options
		{Family: 4572, LeftType: 21, RightType: 21, Num: 11, Proc: 449}, // int2 x int2: hashint2
		{Family: 4572, LeftType: 23, RightType: 23, Num: 1, Proc: 4591}, // int4 x int4: brin_bloom_opcinfo
		{Family: 4572, LeftType: 23, RightType: 23, Num: 2, Proc: 4592}, // int4 x int4: brin_bloom_add_value
		{Family: 4572, LeftType: 23, RightType: 23, Num: 3, Proc: 4593}, // int4 x int4: brin_bloom_consistent
		{Family: 4572, LeftType: 23, RightType: 23, Num: 4, Proc: 4594}, // int4 x int4: brin_bloom_union
		{Family: 4572, LeftType: 23, RightType: 23, Num: 5, Proc: 4595}, // int4 x int4: brin_bloom_options
		{Family: 4572, LeftType: 23, RightType: 23, Num: 11, Proc: 450}, // int4 x int4: hashint4

		// brin/text_minmax_ops (family=4056)
		{Family: 4056, LeftType: 25, RightType: 25, Num: 1, Proc: 3383}, // text x text: brin_minmax_opcinfo
		{Family: 4056, LeftType: 25, RightType: 25, Num: 2, Proc: 3384}, // text x text: brin_minmax_add_value
		{Family: 4056, LeftType: 25, RightType: 25, Num: 3, Proc: 3385}, // text x text: brin_minmax_consistent
		{Family: 4056, LeftType: 25, RightType: 25, Num: 4, Proc: 3386}, // text x text: brin_minmax_union

		// brin/text_bloom_ops (family=4573)
		{Family: 4573, LeftType: 25, RightType: 25, Num: 1, Proc: 4591}, // text x text: brin_bloom_opcinfo
		{Family: 4573, LeftType: 25, RightType: 25, Num: 2, Proc: 4592}, // text x text: brin_bloom_add_value
		{Family: 4573, LeftType: 25, RightType: 25, Num: 3, Proc: 4593}, // text x text: brin_bloom_consistent
		{Family: 4573, LeftType: 25, RightType: 25, Num: 4, Proc: 4594}, // text x text: brin_bloom_union
		{Family: 4573, LeftType: 25, RightType: 25, Num: 5, Proc: 4595}, // text x text: brin_bloom_options
		{Family: 4573, LeftType: 25, RightType: 25, Num: 11, Proc: 400}, // text x text: hashtext

		// brin/oid_minmax_ops (family=4068)
		{Family: 4068, LeftType: 26, RightType: 26, Num: 1, Proc: 3383}, // oid x oid: brin_minmax_opcinfo
		{Family: 4068, LeftType: 26, RightType: 26, Num: 2, Proc: 3384}, // oid x oid: brin_minmax_add_value
		{Family: 4068, LeftType: 26, RightType: 26, Num: 3, Proc: 3385}, // oid x oid: brin_minmax_consistent
		{Family: 4068, LeftType: 26, RightType: 26, Num: 4, Proc: 3386}, // oid x oid: brin_minmax_union

		// brin/oid_minmax_multi_ops (family=4606)
		{Family: 4606, LeftType: 26, RightType: 26, Num: 1, Proc: 4616}, // oid x oid: brin_minmax_multi_opcinfo
		{Family: 4606, LeftType: 26, RightType: 26, Num: 2, Proc: 4617}, // oid x oid: brin_minmax_multi_add_value
		{Family: 4606, LeftType: 26, RightType: 26, Num: 3, Proc: 4618}, // oid x oid: brin_minmax_multi_consistent
		{Family: 4606, LeftType: 26, RightType: 26, Num: 4, Proc: 4619}, // oid x oid: brin_minmax_multi_union
		{Family: 4606, LeftType: 26, RightType: 26, Num: 5, Proc: 4620}, // oid x oid: brin_minmax_multi_options
		{Family: 4606, LeftType: 26, RightType: 26, Num: 11, Proc: 4622}, // oid x oid: brin_minmax_multi_distance_int4

		// brin/oid_bloom_ops (family=4580)
		{Family: 4580, LeftType: 26, RightType: 26, Num: 1, Proc: 4591}, // oid x oid: brin_bloom_opcinfo
		{Family: 4580, LeftType: 26, RightType: 26, Num: 2, Proc: 4592}, // oid x oid: brin_bloom_add_value
		{Family: 4580, LeftType: 26, RightType: 26, Num: 3, Proc: 4593}, // oid x oid: brin_bloom_consistent
		{Family: 4580, LeftType: 26, RightType: 26, Num: 4, Proc: 4594}, // oid x oid: brin_bloom_union
		{Family: 4580, LeftType: 26, RightType: 26, Num: 5, Proc: 4595}, // oid x oid: brin_bloom_options
		{Family: 4580, LeftType: 26, RightType: 26, Num: 11, Proc: 453}, // oid x oid: hashoid

		// brin/tid_minmax_ops (family=4069)
		{Family: 4069, LeftType: 27, RightType: 27, Num: 1, Proc: 3383}, // tid x tid: brin_minmax_opcinfo
		{Family: 4069, LeftType: 27, RightType: 27, Num: 2, Proc: 3384}, // tid x tid: brin_minmax_add_value
		{Family: 4069, LeftType: 27, RightType: 27, Num: 3, Proc: 3385}, // tid x tid: brin_minmax_consistent
		{Family: 4069, LeftType: 27, RightType: 27, Num: 4, Proc: 3386}, // tid x tid: brin_minmax_union

		// brin/tid_bloom_ops (family=4581)
		{Family: 4581, LeftType: 27, RightType: 27, Num: 1, Proc: 4591}, // tid x tid: brin_bloom_opcinfo
		{Family: 4581, LeftType: 27, RightType: 27, Num: 2, Proc: 4592}, // tid x tid: brin_bloom_add_value
		{Family: 4581, LeftType: 27, RightType: 27, Num: 3, Proc: 4593}, // tid x tid: brin_bloom_consistent
		{Family: 4581, LeftType: 27, RightType: 27, Num: 4, Proc: 4594}, // tid x tid: brin_bloom_union
		{Family: 4581, LeftType: 27, RightType: 27, Num: 5, Proc: 4595}, // tid x tid: brin_bloom_options
		{Family: 4581, LeftType: 27, RightType: 27, Num: 11, Proc: 2233}, // tid x tid: hashtid

		// brin/tid_minmax_multi_ops (family=4607)
		{Family: 4607, LeftType: 27, RightType: 27, Num: 1, Proc: 4616}, // tid x tid: brin_minmax_multi_opcinfo
		{Family: 4607, LeftType: 27, RightType: 27, Num: 2, Proc: 4617}, // tid x tid: brin_minmax_multi_add_value
		{Family: 4607, LeftType: 27, RightType: 27, Num: 3, Proc: 4618}, // tid x tid: brin_minmax_multi_consistent
		{Family: 4607, LeftType: 27, RightType: 27, Num: 4, Proc: 4619}, // tid x tid: brin_minmax_multi_union
		{Family: 4607, LeftType: 27, RightType: 27, Num: 5, Proc: 4620}, // tid x tid: brin_minmax_multi_options
		{Family: 4607, LeftType: 27, RightType: 27, Num: 11, Proc: 4627}, // tid x tid: brin_minmax_multi_distance_tid

		// brin/float_minmax_ops (family=4070)
		{Family: 4070, LeftType: 700, RightType: 700, Num: 1, Proc: 3383}, // float4 x float4: brin_minmax_opcinfo
		{Family: 4070, LeftType: 700, RightType: 700, Num: 2, Proc: 3384}, // float4 x float4: brin_minmax_add_value
		{Family: 4070, LeftType: 700, RightType: 700, Num: 3, Proc: 3385}, // float4 x float4: brin_minmax_consistent
		{Family: 4070, LeftType: 700, RightType: 700, Num: 4, Proc: 3386}, // float4 x float4: brin_minmax_union
		{Family: 4070, LeftType: 701, RightType: 701, Num: 1, Proc: 3383}, // float8 x float8: brin_minmax_opcinfo
		{Family: 4070, LeftType: 701, RightType: 701, Num: 2, Proc: 3384}, // float8 x float8: brin_minmax_add_value
		{Family: 4070, LeftType: 701, RightType: 701, Num: 3, Proc: 3385}, // float8 x float8: brin_minmax_consistent
		{Family: 4070, LeftType: 701, RightType: 701, Num: 4, Proc: 3386}, // float8 x float8: brin_minmax_union

		// brin/float_minmax_multi_ops (family=4608)
		{Family: 4608, LeftType: 700, RightType: 700, Num: 1, Proc: 4616}, // float4 x float4: brin_minmax_multi_opcinfo
		{Family: 4608, LeftType: 700, RightType: 700, Num: 2, Proc: 4617}, // float4 x float4: brin_minmax_multi_add_value
		{Family: 4608, LeftType: 700, RightType: 700, Num: 3, Proc: 4618}, // float4 x float4: brin_minmax_multi_consistent
		{Family: 4608, LeftType: 700, RightType: 700, Num: 4, Proc: 4619}, // float4 x float4: brin_minmax_multi_union
		{Family: 4608, LeftType: 700, RightType: 700, Num: 5, Proc: 4620}, // float4 x float4: brin_minmax_multi_options
		{Family: 4608, LeftType: 700, RightType: 700, Num: 11, Proc: 4624}, // float4 x float4: brin_minmax_multi_distance_float4
		{Family: 4608, LeftType: 701, RightType: 701, Num: 1, Proc: 4616}, // float8 x float8: brin_minmax_multi_opcinfo
		{Family: 4608, LeftType: 701, RightType: 701, Num: 2, Proc: 4617}, // float8 x float8: brin_minmax_multi_add_value
		{Family: 4608, LeftType: 701, RightType: 701, Num: 3, Proc: 4618}, // float8 x float8: brin_minmax_multi_consistent
		{Family: 4608, LeftType: 701, RightType: 701, Num: 4, Proc: 4619}, // float8 x float8: brin_minmax_multi_union
		{Family: 4608, LeftType: 701, RightType: 701, Num: 5, Proc: 4620}, // float8 x float8: brin_minmax_multi_options
		{Family: 4608, LeftType: 701, RightType: 701, Num: 11, Proc: 4625}, // float8 x float8: brin_minmax_multi_distance_float8

		// brin/float_bloom_ops (family=4582)
		{Family: 4582, LeftType: 700, RightType: 700, Num: 1, Proc: 4591}, // float4 x float4: brin_bloom_opcinfo
		{Family: 4582, LeftType: 700, RightType: 700, Num: 2, Proc: 4592}, // float4 x float4: brin_bloom_add_value
		{Family: 4582, LeftType: 700, RightType: 700, Num: 3, Proc: 4593}, // float4 x float4: brin_bloom_consistent
		{Family: 4582, LeftType: 700, RightType: 700, Num: 4, Proc: 4594}, // float4 x float4: brin_bloom_union
		{Family: 4582, LeftType: 700, RightType: 700, Num: 5, Proc: 4595}, // float4 x float4: brin_bloom_options
		{Family: 4582, LeftType: 700, RightType: 700, Num: 11, Proc: 451}, // float4 x float4: hashfloat4
		{Family: 4582, LeftType: 701, RightType: 701, Num: 1, Proc: 4591}, // float8 x float8: brin_bloom_opcinfo
		{Family: 4582, LeftType: 701, RightType: 701, Num: 2, Proc: 4592}, // float8 x float8: brin_bloom_add_value
		{Family: 4582, LeftType: 701, RightType: 701, Num: 3, Proc: 4593}, // float8 x float8: brin_bloom_consistent
		{Family: 4582, LeftType: 701, RightType: 701, Num: 4, Proc: 4594}, // float8 x float8: brin_bloom_union
		{Family: 4582, LeftType: 701, RightType: 701, Num: 5, Proc: 4595}, // float8 x float8: brin_bloom_options
		{Family: 4582, LeftType: 701, RightType: 701, Num: 11, Proc: 452}, // float8 x float8: hashfloat8

		// brin/macaddr_minmax_ops (family=4074)
		{Family: 4074, LeftType: 829, RightType: 829, Num: 1, Proc: 3383}, // macaddr x macaddr: brin_minmax_opcinfo
		{Family: 4074, LeftType: 829, RightType: 829, Num: 2, Proc: 3384}, // macaddr x macaddr: brin_minmax_add_value
		{Family: 4074, LeftType: 829, RightType: 829, Num: 3, Proc: 3385}, // macaddr x macaddr: brin_minmax_consistent
		{Family: 4074, LeftType: 829, RightType: 829, Num: 4, Proc: 3386}, // macaddr x macaddr: brin_minmax_union

		// brin/macaddr_minmax_multi_ops (family=4609)
		{Family: 4609, LeftType: 829, RightType: 829, Num: 1, Proc: 4616}, // macaddr x macaddr: brin_minmax_multi_opcinfo
		{Family: 4609, LeftType: 829, RightType: 829, Num: 2, Proc: 4617}, // macaddr x macaddr: brin_minmax_multi_add_value
		{Family: 4609, LeftType: 829, RightType: 829, Num: 3, Proc: 4618}, // macaddr x macaddr: brin_minmax_multi_consistent
		{Family: 4609, LeftType: 829, RightType: 829, Num: 4, Proc: 4619}, // macaddr x macaddr: brin_minmax_multi_union
		{Family: 4609, LeftType: 829, RightType: 829, Num: 5, Proc: 4620}, // macaddr x macaddr: brin_minmax_multi_options
		{Family: 4609, LeftType: 829, RightType: 829, Num: 11, Proc: 4634}, // macaddr x macaddr: brin_minmax_multi_distance_macaddr

		// brin/macaddr_bloom_ops (family=4583)
		{Family: 4583, LeftType: 829, RightType: 829, Num: 1, Proc: 4591}, // macaddr x macaddr: brin_bloom_opcinfo
		{Family: 4583, LeftType: 829, RightType: 829, Num: 2, Proc: 4592}, // macaddr x macaddr: brin_bloom_add_value
		{Family: 4583, LeftType: 829, RightType: 829, Num: 3, Proc: 4593}, // macaddr x macaddr: brin_bloom_consistent
		{Family: 4583, LeftType: 829, RightType: 829, Num: 4, Proc: 4594}, // macaddr x macaddr: brin_bloom_union
		{Family: 4583, LeftType: 829, RightType: 829, Num: 5, Proc: 4595}, // macaddr x macaddr: brin_bloom_options
		{Family: 4583, LeftType: 829, RightType: 829, Num: 11, Proc: 399}, // macaddr x macaddr: hashmacaddr

		// brin/macaddr8_minmax_ops (family=4109)
		{Family: 4109, LeftType: 774, RightType: 774, Num: 1, Proc: 3383}, // macaddr8 x macaddr8: brin_minmax_opcinfo
		{Family: 4109, LeftType: 774, RightType: 774, Num: 2, Proc: 3384}, // macaddr8 x macaddr8: brin_minmax_add_value
		{Family: 4109, LeftType: 774, RightType: 774, Num: 3, Proc: 3385}, // macaddr8 x macaddr8: brin_minmax_consistent
		{Family: 4109, LeftType: 774, RightType: 774, Num: 4, Proc: 3386}, // macaddr8 x macaddr8: brin_minmax_union

		// brin/macaddr8_minmax_multi_ops (family=4610)
		{Family: 4610, LeftType: 774, RightType: 774, Num: 1, Proc: 4616}, // macaddr8 x macaddr8: brin_minmax_multi_opcinfo
		{Family: 4610, LeftType: 774, RightType: 774, Num: 2, Proc: 4617}, // macaddr8 x macaddr8: brin_minmax_multi_add_value
		{Family: 4610, LeftType: 774, RightType: 774, Num: 3, Proc: 4618}, // macaddr8 x macaddr8: brin_minmax_multi_consistent
		{Family: 4610, LeftType: 774, RightType: 774, Num: 4, Proc: 4619}, // macaddr8 x macaddr8: brin_minmax_multi_union
		{Family: 4610, LeftType: 774, RightType: 774, Num: 5, Proc: 4620}, // macaddr8 x macaddr8: brin_minmax_multi_options
		{Family: 4610, LeftType: 774, RightType: 774, Num: 11, Proc: 4635}, // macaddr8 x macaddr8: brin_minmax_multi_distance_macaddr8

		// brin/macaddr8_bloom_ops (family=4584)
		{Family: 4584, LeftType: 774, RightType: 774, Num: 1, Proc: 4591}, // macaddr8 x macaddr8: brin_bloom_opcinfo
		{Family: 4584, LeftType: 774, RightType: 774, Num: 2, Proc: 4592}, // macaddr8 x macaddr8: brin_bloom_add_value
		{Family: 4584, LeftType: 774, RightType: 774, Num: 3, Proc: 4593}, // macaddr8 x macaddr8: brin_bloom_consistent
		{Family: 4584, LeftType: 774, RightType: 774, Num: 4, Proc: 4594}, // macaddr8 x macaddr8: brin_bloom_union
		{Family: 4584, LeftType: 774, RightType: 774, Num: 5, Proc: 4595}, // macaddr8 x macaddr8: brin_bloom_options
		{Family: 4584, LeftType: 774, RightType: 774, Num: 11, Proc: 328}, // macaddr8 x macaddr8: hashmacaddr8

		// brin/network_minmax_ops (family=4075)
		{Family: 4075, LeftType: 869, RightType: 869, Num: 1, Proc: 3383}, // inet x inet: brin_minmax_opcinfo
		{Family: 4075, LeftType: 869, RightType: 869, Num: 2, Proc: 3384}, // inet x inet: brin_minmax_add_value
		{Family: 4075, LeftType: 869, RightType: 869, Num: 3, Proc: 3385}, // inet x inet: brin_minmax_consistent
		{Family: 4075, LeftType: 869, RightType: 869, Num: 4, Proc: 3386}, // inet x inet: brin_minmax_union

		// brin/network_minmax_multi_ops (family=4611)
		{Family: 4611, LeftType: 869, RightType: 869, Num: 1, Proc: 4616}, // inet x inet: brin_minmax_multi_opcinfo
		{Family: 4611, LeftType: 869, RightType: 869, Num: 2, Proc: 4617}, // inet x inet: brin_minmax_multi_add_value
		{Family: 4611, LeftType: 869, RightType: 869, Num: 3, Proc: 4618}, // inet x inet: brin_minmax_multi_consistent
		{Family: 4611, LeftType: 869, RightType: 869, Num: 4, Proc: 4619}, // inet x inet: brin_minmax_multi_union
		{Family: 4611, LeftType: 869, RightType: 869, Num: 5, Proc: 4620}, // inet x inet: brin_minmax_multi_options
		{Family: 4611, LeftType: 869, RightType: 869, Num: 11, Proc: 4636}, // inet x inet: brin_minmax_multi_distance_inet

		// brin/network_bloom_ops (family=4585)
		{Family: 4585, LeftType: 869, RightType: 869, Num: 1, Proc: 4591}, // inet x inet: brin_bloom_opcinfo
		{Family: 4585, LeftType: 869, RightType: 869, Num: 2, Proc: 4592}, // inet x inet: brin_bloom_add_value
		{Family: 4585, LeftType: 869, RightType: 869, Num: 3, Proc: 4593}, // inet x inet: brin_bloom_consistent
		{Family: 4585, LeftType: 869, RightType: 869, Num: 4, Proc: 4594}, // inet x inet: brin_bloom_union
		{Family: 4585, LeftType: 869, RightType: 869, Num: 5, Proc: 4595}, // inet x inet: brin_bloom_options
		{Family: 4585, LeftType: 869, RightType: 869, Num: 11, Proc: 422}, // inet x inet: hashinet

		// brin/network_inclusion_ops (family=4102)
		{Family: 4102, LeftType: 869, RightType: 869, Num: 1, Proc: 4105}, // inet x inet: brin_inclusion_opcinfo
		{Family: 4102, LeftType: 869, RightType: 869, Num: 2, Proc: 4106}, // inet x inet: brin_inclusion_add_value
		{Family: 4102, LeftType: 869, RightType: 869, Num: 3, Proc: 4107}, // inet x inet: brin_inclusion_consistent
		{Family: 4102, LeftType: 869, RightType: 869, Num: 4, Proc: 4108}, // inet x inet: brin_inclusion_union
		{Family: 4102, LeftType: 869, RightType: 869, Num: 11, Proc: 4063}, // inet x inet: inet_merge
		{Family: 4102, LeftType: 869, RightType: 869, Num: 12, Proc: 4071}, // inet x inet: inet_same_family
		{Family: 4102, LeftType: 869, RightType: 869, Num: 13, Proc: 930}, // inet x inet: network_supeq

		// brin/bpchar_minmax_ops (family=4076)
		{Family: 4076, LeftType: 1042, RightType: 1042, Num: 1, Proc: 3383}, // bpchar x bpchar: brin_minmax_opcinfo
		{Family: 4076, LeftType: 1042, RightType: 1042, Num: 2, Proc: 3384}, // bpchar x bpchar: brin_minmax_add_value
		{Family: 4076, LeftType: 1042, RightType: 1042, Num: 3, Proc: 3385}, // bpchar x bpchar: brin_minmax_consistent
		{Family: 4076, LeftType: 1042, RightType: 1042, Num: 4, Proc: 3386}, // bpchar x bpchar: brin_minmax_union

		// brin/bpchar_bloom_ops (family=4586)
		{Family: 4586, LeftType: 1042, RightType: 1042, Num: 1, Proc: 4591}, // bpchar x bpchar: brin_bloom_opcinfo
		{Family: 4586, LeftType: 1042, RightType: 1042, Num: 2, Proc: 4592}, // bpchar x bpchar: brin_bloom_add_value
		{Family: 4586, LeftType: 1042, RightType: 1042, Num: 3, Proc: 4593}, // bpchar x bpchar: brin_bloom_consistent
		{Family: 4586, LeftType: 1042, RightType: 1042, Num: 4, Proc: 4594}, // bpchar x bpchar: brin_bloom_union
		{Family: 4586, LeftType: 1042, RightType: 1042, Num: 5, Proc: 4595}, // bpchar x bpchar: brin_bloom_options
		{Family: 4586, LeftType: 1042, RightType: 1042, Num: 11, Proc: 1080}, // bpchar x bpchar: hashbpchar

		// brin/time_minmax_ops (family=4077)
		{Family: 4077, LeftType: 1083, RightType: 1083, Num: 1, Proc: 3383}, // time x time: brin_minmax_opcinfo
		{Family: 4077, LeftType: 1083, RightType: 1083, Num: 2, Proc: 3384}, // time x time: brin_minmax_add_value
		{Family: 4077, LeftType: 1083, RightType: 1083, Num: 3, Proc: 3385}, // time x time: brin_minmax_consistent
		{Family: 4077, LeftType: 1083, RightType: 1083, Num: 4, Proc: 3386}, // time x time: brin_minmax_union

		// brin/time_minmax_multi_ops (family=4612)
		{Family: 4612, LeftType: 1083, RightType: 1083, Num: 1, Proc: 4616}, // time x time: brin_minmax_multi_opcinfo
		{Family: 4612, LeftType: 1083, RightType: 1083, Num: 2, Proc: 4617}, // time x time: brin_minmax_multi_add_value
		{Family: 4612, LeftType: 1083, RightType: 1083, Num: 3, Proc: 4618}, // time x time: brin_minmax_multi_consistent
		{Family: 4612, LeftType: 1083, RightType: 1083, Num: 4, Proc: 4619}, // time x time: brin_minmax_multi_union
		{Family: 4612, LeftType: 1083, RightType: 1083, Num: 5, Proc: 4620}, // time x time: brin_minmax_multi_options
		{Family: 4612, LeftType: 1083, RightType: 1083, Num: 11, Proc: 4630}, // time x time: brin_minmax_multi_distance_time

		// brin/time_bloom_ops (family=4587)
		{Family: 4587, LeftType: 1083, RightType: 1083, Num: 1, Proc: 4591}, // time x time: brin_bloom_opcinfo
		{Family: 4587, LeftType: 1083, RightType: 1083, Num: 2, Proc: 4592}, // time x time: brin_bloom_add_value
		{Family: 4587, LeftType: 1083, RightType: 1083, Num: 3, Proc: 4593}, // time x time: brin_bloom_consistent
		{Family: 4587, LeftType: 1083, RightType: 1083, Num: 4, Proc: 4594}, // time x time: brin_bloom_union
		{Family: 4587, LeftType: 1083, RightType: 1083, Num: 5, Proc: 4595}, // time x time: brin_bloom_options
		{Family: 4587, LeftType: 1083, RightType: 1083, Num: 11, Proc: 1688}, // time x time: time_hash

		// brin/datetime_minmax_ops (family=4059)
		{Family: 4059, LeftType: 1114, RightType: 1114, Num: 1, Proc: 3383}, // timestamp x timestamp: brin_minmax_opcinfo
		{Family: 4059, LeftType: 1114, RightType: 1114, Num: 2, Proc: 3384}, // timestamp x timestamp: brin_minmax_add_value
		{Family: 4059, LeftType: 1114, RightType: 1114, Num: 3, Proc: 3385}, // timestamp x timestamp: brin_minmax_consistent
		{Family: 4059, LeftType: 1114, RightType: 1114, Num: 4, Proc: 3386}, // timestamp x timestamp: brin_minmax_union
		{Family: 4059, LeftType: 1184, RightType: 1184, Num: 1, Proc: 3383}, // timestamptz x timestamptz: brin_minmax_opcinfo
		{Family: 4059, LeftType: 1184, RightType: 1184, Num: 2, Proc: 3384}, // timestamptz x timestamptz: brin_minmax_add_value
		{Family: 4059, LeftType: 1184, RightType: 1184, Num: 3, Proc: 3385}, // timestamptz x timestamptz: brin_minmax_consistent
		{Family: 4059, LeftType: 1184, RightType: 1184, Num: 4, Proc: 3386}, // timestamptz x timestamptz: brin_minmax_union
		{Family: 4059, LeftType: 1082, RightType: 1082, Num: 1, Proc: 3383}, // date x date: brin_minmax_opcinfo
		{Family: 4059, LeftType: 1082, RightType: 1082, Num: 2, Proc: 3384}, // date x date: brin_minmax_add_value
		{Family: 4059, LeftType: 1082, RightType: 1082, Num: 3, Proc: 3385}, // date x date: brin_minmax_consistent
		{Family: 4059, LeftType: 1082, RightType: 1082, Num: 4, Proc: 3386}, // date x date: brin_minmax_union

		// brin/datetime_minmax_multi_ops (family=4605)
		{Family: 4605, LeftType: 1114, RightType: 1114, Num: 1, Proc: 4616}, // timestamp x timestamp: brin_minmax_multi_opcinfo
		{Family: 4605, LeftType: 1114, RightType: 1114, Num: 2, Proc: 4617}, // timestamp x timestamp: brin_minmax_multi_add_value
		{Family: 4605, LeftType: 1114, RightType: 1114, Num: 3, Proc: 4618}, // timestamp x timestamp: brin_minmax_multi_consistent
		{Family: 4605, LeftType: 1114, RightType: 1114, Num: 4, Proc: 4619}, // timestamp x timestamp: brin_minmax_multi_union
		{Family: 4605, LeftType: 1114, RightType: 1114, Num: 5, Proc: 4620}, // timestamp x timestamp: brin_minmax_multi_options
		{Family: 4605, LeftType: 1114, RightType: 1114, Num: 11, Proc: 4637}, // timestamp x timestamp: brin_minmax_multi_distance_timestamp
		{Family: 4605, LeftType: 1184, RightType: 1184, Num: 1, Proc: 4616}, // timestamptz x timestamptz: brin_minmax_multi_opcinfo
		{Family: 4605, LeftType: 1184, RightType: 1184, Num: 2, Proc: 4617}, // timestamptz x timestamptz: brin_minmax_multi_add_value
		{Family: 4605, LeftType: 1184, RightType: 1184, Num: 3, Proc: 4618}, // timestamptz x timestamptz: brin_minmax_multi_consistent
		{Family: 4605, LeftType: 1184, RightType: 1184, Num: 4, Proc: 4619}, // timestamptz x timestamptz: brin_minmax_multi_union
		{Family: 4605, LeftType: 1184, RightType: 1184, Num: 5, Proc: 4620}, // timestamptz x timestamptz: brin_minmax_multi_options
		{Family: 4605, LeftType: 1184, RightType: 1184, Num: 11, Proc: 4637}, // timestamptz x timestamptz: brin_minmax_multi_distance_timestamp
		{Family: 4605, LeftType: 1082, RightType: 1082, Num: 1, Proc: 4616}, // date x date: brin_minmax_multi_opcinfo
		{Family: 4605, LeftType: 1082, RightType: 1082, Num: 2, Proc: 4617}, // date x date: brin_minmax_multi_add_value
		{Family: 4605, LeftType: 1082, RightType: 1082, Num: 3, Proc: 4618}, // date x date: brin_minmax_multi_consistent
		{Family: 4605, LeftType: 1082, RightType: 1082, Num: 4, Proc: 4619}, // date x date: brin_minmax_multi_union
		{Family: 4605, LeftType: 1082, RightType: 1082, Num: 5, Proc: 4620}, // date x date: brin_minmax_multi_options
		{Family: 4605, LeftType: 1082, RightType: 1082, Num: 11, Proc: 4629}, // date x date: brin_minmax_multi_distance_date

		// brin/datetime_bloom_ops (family=4576)
		{Family: 4576, LeftType: 1114, RightType: 1114, Num: 1, Proc: 4591}, // timestamp x timestamp: brin_bloom_opcinfo
		{Family: 4576, LeftType: 1114, RightType: 1114, Num: 2, Proc: 4592}, // timestamp x timestamp: brin_bloom_add_value
		{Family: 4576, LeftType: 1114, RightType: 1114, Num: 3, Proc: 4593}, // timestamp x timestamp: brin_bloom_consistent
		{Family: 4576, LeftType: 1114, RightType: 1114, Num: 4, Proc: 4594}, // timestamp x timestamp: brin_bloom_union
		{Family: 4576, LeftType: 1114, RightType: 1114, Num: 5, Proc: 4595}, // timestamp x timestamp: brin_bloom_options
		{Family: 4576, LeftType: 1114, RightType: 1114, Num: 11, Proc: 2039}, // timestamp x timestamp: timestamp_hash
		{Family: 4576, LeftType: 1184, RightType: 1184, Num: 1, Proc: 4591}, // timestamptz x timestamptz: brin_bloom_opcinfo
		{Family: 4576, LeftType: 1184, RightType: 1184, Num: 2, Proc: 4592}, // timestamptz x timestamptz: brin_bloom_add_value
		{Family: 4576, LeftType: 1184, RightType: 1184, Num: 3, Proc: 4593}, // timestamptz x timestamptz: brin_bloom_consistent
		{Family: 4576, LeftType: 1184, RightType: 1184, Num: 4, Proc: 4594}, // timestamptz x timestamptz: brin_bloom_union
		{Family: 4576, LeftType: 1184, RightType: 1184, Num: 5, Proc: 4595}, // timestamptz x timestamptz: brin_bloom_options
		{Family: 4576, LeftType: 1184, RightType: 1184, Num: 11, Proc: 2039}, // timestamptz x timestamptz: timestamp_hash
		{Family: 4576, LeftType: 1082, RightType: 1082, Num: 1, Proc: 4591}, // date x date: brin_bloom_opcinfo
		{Family: 4576, LeftType: 1082, RightType: 1082, Num: 2, Proc: 4592}, // date x date: brin_bloom_add_value
		{Family: 4576, LeftType: 1082, RightType: 1082, Num: 3, Proc: 4593}, // date x date: brin_bloom_consistent
		{Family: 4576, LeftType: 1082, RightType: 1082, Num: 4, Proc: 4594}, // date x date: brin_bloom_union
		{Family: 4576, LeftType: 1082, RightType: 1082, Num: 5, Proc: 4595}, // date x date: brin_bloom_options
		{Family: 4576, LeftType: 1082, RightType: 1082, Num: 11, Proc: 450}, // date x date: hashint4

		// brin/interval_minmax_ops (family=4078)
		{Family: 4078, LeftType: 1186, RightType: 1186, Num: 1, Proc: 3383}, // interval x interval: brin_minmax_opcinfo
		{Family: 4078, LeftType: 1186, RightType: 1186, Num: 2, Proc: 3384}, // interval x interval: brin_minmax_add_value
		{Family: 4078, LeftType: 1186, RightType: 1186, Num: 3, Proc: 3385}, // interval x interval: brin_minmax_consistent
		{Family: 4078, LeftType: 1186, RightType: 1186, Num: 4, Proc: 3386}, // interval x interval: brin_minmax_union

		// brin/interval_minmax_multi_ops (family=4613)
		{Family: 4613, LeftType: 1186, RightType: 1186, Num: 1, Proc: 4616}, // interval x interval: brin_minmax_multi_opcinfo
		{Family: 4613, LeftType: 1186, RightType: 1186, Num: 2, Proc: 4617}, // interval x interval: brin_minmax_multi_add_value
		{Family: 4613, LeftType: 1186, RightType: 1186, Num: 3, Proc: 4618}, // interval x interval: brin_minmax_multi_consistent
		{Family: 4613, LeftType: 1186, RightType: 1186, Num: 4, Proc: 4619}, // interval x interval: brin_minmax_multi_union
		{Family: 4613, LeftType: 1186, RightType: 1186, Num: 5, Proc: 4620}, // interval x interval: brin_minmax_multi_options
		{Family: 4613, LeftType: 1186, RightType: 1186, Num: 11, Proc: 4631}, // interval x interval: brin_minmax_multi_distance_interval

		// brin/interval_bloom_ops (family=4588)
		{Family: 4588, LeftType: 1186, RightType: 1186, Num: 1, Proc: 4591}, // interval x interval: brin_bloom_opcinfo
		{Family: 4588, LeftType: 1186, RightType: 1186, Num: 2, Proc: 4592}, // interval x interval: brin_bloom_add_value
		{Family: 4588, LeftType: 1186, RightType: 1186, Num: 3, Proc: 4593}, // interval x interval: brin_bloom_consistent
		{Family: 4588, LeftType: 1186, RightType: 1186, Num: 4, Proc: 4594}, // interval x interval: brin_bloom_union
		{Family: 4588, LeftType: 1186, RightType: 1186, Num: 5, Proc: 4595}, // interval x interval: brin_bloom_options
		{Family: 4588, LeftType: 1186, RightType: 1186, Num: 11, Proc: 1697}, // interval x interval: interval_hash

		// brin/timetz_minmax_ops (family=4058)
		{Family: 4058, LeftType: 1266, RightType: 1266, Num: 1, Proc: 3383}, // timetz x timetz: brin_minmax_opcinfo
		{Family: 4058, LeftType: 1266, RightType: 1266, Num: 2, Proc: 3384}, // timetz x timetz: brin_minmax_add_value
		{Family: 4058, LeftType: 1266, RightType: 1266, Num: 3, Proc: 3385}, // timetz x timetz: brin_minmax_consistent
		{Family: 4058, LeftType: 1266, RightType: 1266, Num: 4, Proc: 3386}, // timetz x timetz: brin_minmax_union

		// brin/timetz_minmax_multi_ops (family=4604)
		{Family: 4604, LeftType: 1266, RightType: 1266, Num: 1, Proc: 4616}, // timetz x timetz: brin_minmax_multi_opcinfo
		{Family: 4604, LeftType: 1266, RightType: 1266, Num: 2, Proc: 4617}, // timetz x timetz: brin_minmax_multi_add_value
		{Family: 4604, LeftType: 1266, RightType: 1266, Num: 3, Proc: 4618}, // timetz x timetz: brin_minmax_multi_consistent
		{Family: 4604, LeftType: 1266, RightType: 1266, Num: 4, Proc: 4619}, // timetz x timetz: brin_minmax_multi_union
		{Family: 4604, LeftType: 1266, RightType: 1266, Num: 5, Proc: 4620}, // timetz x timetz: brin_minmax_multi_options
		{Family: 4604, LeftType: 1266, RightType: 1266, Num: 11, Proc: 4632}, // timetz x timetz: brin_minmax_multi_distance_timetz

		// brin/timetz_bloom_ops (family=4575)
		{Family: 4575, LeftType: 1266, RightType: 1266, Num: 1, Proc: 4591}, // timetz x timetz: brin_bloom_opcinfo
		{Family: 4575, LeftType: 1266, RightType: 1266, Num: 2, Proc: 4592}, // timetz x timetz: brin_bloom_add_value
		{Family: 4575, LeftType: 1266, RightType: 1266, Num: 3, Proc: 4593}, // timetz x timetz: brin_bloom_consistent
		{Family: 4575, LeftType: 1266, RightType: 1266, Num: 4, Proc: 4594}, // timetz x timetz: brin_bloom_union
		{Family: 4575, LeftType: 1266, RightType: 1266, Num: 5, Proc: 4595}, // timetz x timetz: brin_bloom_options
		{Family: 4575, LeftType: 1266, RightType: 1266, Num: 11, Proc: 1696}, // timetz x timetz: timetz_hash

		// brin/bit_minmax_ops (family=4079)
		{Family: 4079, LeftType: 1560, RightType: 1560, Num: 1, Proc: 3383}, // bit x bit: brin_minmax_opcinfo
		{Family: 4079, LeftType: 1560, RightType: 1560, Num: 2, Proc: 3384}, // bit x bit: brin_minmax_add_value
		{Family: 4079, LeftType: 1560, RightType: 1560, Num: 3, Proc: 3385}, // bit x bit: brin_minmax_consistent
		{Family: 4079, LeftType: 1560, RightType: 1560, Num: 4, Proc: 3386}, // bit x bit: brin_minmax_union

		// brin/varbit_minmax_ops (family=4080)
		{Family: 4080, LeftType: 1562, RightType: 1562, Num: 1, Proc: 3383}, // varbit x varbit: brin_minmax_opcinfo
		{Family: 4080, LeftType: 1562, RightType: 1562, Num: 2, Proc: 3384}, // varbit x varbit: brin_minmax_add_value
		{Family: 4080, LeftType: 1562, RightType: 1562, Num: 3, Proc: 3385}, // varbit x varbit: brin_minmax_consistent
		{Family: 4080, LeftType: 1562, RightType: 1562, Num: 4, Proc: 3386}, // varbit x varbit: brin_minmax_union

		// brin/numeric_minmax_ops (family=4055)
		{Family: 4055, LeftType: 1700, RightType: 1700, Num: 1, Proc: 3383}, // numeric x numeric: brin_minmax_opcinfo
		{Family: 4055, LeftType: 1700, RightType: 1700, Num: 2, Proc: 3384}, // numeric x numeric: brin_minmax_add_value
		{Family: 4055, LeftType: 1700, RightType: 1700, Num: 3, Proc: 3385}, // numeric x numeric: brin_minmax_consistent
		{Family: 4055, LeftType: 1700, RightType: 1700, Num: 4, Proc: 3386}, // numeric x numeric: brin_minmax_union

		// brin/numeric_minmax_multi_ops (family=4603)
		{Family: 4603, LeftType: 1700, RightType: 1700, Num: 1, Proc: 4616}, // numeric x numeric: brin_minmax_multi_opcinfo
		{Family: 4603, LeftType: 1700, RightType: 1700, Num: 2, Proc: 4617}, // numeric x numeric: brin_minmax_multi_add_value
		{Family: 4603, LeftType: 1700, RightType: 1700, Num: 3, Proc: 4618}, // numeric x numeric: brin_minmax_multi_consistent
		{Family: 4603, LeftType: 1700, RightType: 1700, Num: 4, Proc: 4619}, // numeric x numeric: brin_minmax_multi_union
		{Family: 4603, LeftType: 1700, RightType: 1700, Num: 5, Proc: 4620}, // numeric x numeric: brin_minmax_multi_options
		{Family: 4603, LeftType: 1700, RightType: 1700, Num: 11, Proc: 4626}, // numeric x numeric: brin_minmax_multi_distance_numeric

		// brin/numeric_bloom_ops (family=4574)
		{Family: 4574, LeftType: 1700, RightType: 1700, Num: 1, Proc: 4591}, // numeric x numeric: brin_bloom_opcinfo
		{Family: 4574, LeftType: 1700, RightType: 1700, Num: 2, Proc: 4592}, // numeric x numeric: brin_bloom_add_value
		{Family: 4574, LeftType: 1700, RightType: 1700, Num: 3, Proc: 4593}, // numeric x numeric: brin_bloom_consistent
		{Family: 4574, LeftType: 1700, RightType: 1700, Num: 4, Proc: 4594}, // numeric x numeric: brin_bloom_union
		{Family: 4574, LeftType: 1700, RightType: 1700, Num: 5, Proc: 4595}, // numeric x numeric: brin_bloom_options
		{Family: 4574, LeftType: 1700, RightType: 1700, Num: 11, Proc: 432}, // numeric x numeric: hash_numeric

		// brin/uuid_minmax_ops (family=4081)
		{Family: 4081, LeftType: 2950, RightType: 2950, Num: 1, Proc: 3383}, // uuid x uuid: brin_minmax_opcinfo
		{Family: 4081, LeftType: 2950, RightType: 2950, Num: 2, Proc: 3384}, // uuid x uuid: brin_minmax_add_value
		{Family: 4081, LeftType: 2950, RightType: 2950, Num: 3, Proc: 3385}, // uuid x uuid: brin_minmax_consistent
		{Family: 4081, LeftType: 2950, RightType: 2950, Num: 4, Proc: 3386}, // uuid x uuid: brin_minmax_union

		// brin/uuid_minmax_multi_ops (family=4614)
		{Family: 4614, LeftType: 2950, RightType: 2950, Num: 1, Proc: 4616}, // uuid x uuid: brin_minmax_multi_opcinfo
		{Family: 4614, LeftType: 2950, RightType: 2950, Num: 2, Proc: 4617}, // uuid x uuid: brin_minmax_multi_add_value
		{Family: 4614, LeftType: 2950, RightType: 2950, Num: 3, Proc: 4618}, // uuid x uuid: brin_minmax_multi_consistent
		{Family: 4614, LeftType: 2950, RightType: 2950, Num: 4, Proc: 4619}, // uuid x uuid: brin_minmax_multi_union
		{Family: 4614, LeftType: 2950, RightType: 2950, Num: 5, Proc: 4620}, // uuid x uuid: brin_minmax_multi_options
		{Family: 4614, LeftType: 2950, RightType: 2950, Num: 11, Proc: 4628}, // uuid x uuid: brin_minmax_multi_distance_uuid

		// brin/uuid_bloom_ops (family=4589)
		{Family: 4589, LeftType: 2950, RightType: 2950, Num: 1, Proc: 4591}, // uuid x uuid: brin_bloom_opcinfo
		{Family: 4589, LeftType: 2950, RightType: 2950, Num: 2, Proc: 4592}, // uuid x uuid: brin_bloom_add_value
		{Family: 4589, LeftType: 2950, RightType: 2950, Num: 3, Proc: 4593}, // uuid x uuid: brin_bloom_consistent
		{Family: 4589, LeftType: 2950, RightType: 2950, Num: 4, Proc: 4594}, // uuid x uuid: brin_bloom_union
		{Family: 4589, LeftType: 2950, RightType: 2950, Num: 5, Proc: 4595}, // uuid x uuid: brin_bloom_options
		{Family: 4589, LeftType: 2950, RightType: 2950, Num: 11, Proc: 2963}, // uuid x uuid: uuid_hash

		// brin/range_inclusion_ops (family=4103)
		{Family: 4103, LeftType: 3831, RightType: 3831, Num: 1, Proc: 4105}, // anyrange x anyrange: brin_inclusion_opcinfo
		{Family: 4103, LeftType: 3831, RightType: 3831, Num: 2, Proc: 4106}, // anyrange x anyrange: brin_inclusion_add_value
		{Family: 4103, LeftType: 3831, RightType: 3831, Num: 3, Proc: 4107}, // anyrange x anyrange: brin_inclusion_consistent
		{Family: 4103, LeftType: 3831, RightType: 3831, Num: 4, Proc: 4108}, // anyrange x anyrange: brin_inclusion_union
		{Family: 4103, LeftType: 3831, RightType: 3831, Num: 11, Proc: 4057}, // anyrange x anyrange: range_merge(anyrange,anyrange)
		{Family: 4103, LeftType: 3831, RightType: 3831, Num: 13, Proc: 3859}, // anyrange x anyrange: range_contains
		{Family: 4103, LeftType: 3831, RightType: 3831, Num: 14, Proc: 3850}, // anyrange x anyrange: isempty(anyrange)

		// brin/pg_lsn_minmax_ops (family=4082)
		{Family: 4082, LeftType: 3220, RightType: 3220, Num: 1, Proc: 3383}, // pg_lsn x pg_lsn: brin_minmax_opcinfo
		{Family: 4082, LeftType: 3220, RightType: 3220, Num: 2, Proc: 3384}, // pg_lsn x pg_lsn: brin_minmax_add_value
		{Family: 4082, LeftType: 3220, RightType: 3220, Num: 3, Proc: 3385}, // pg_lsn x pg_lsn: brin_minmax_consistent
		{Family: 4082, LeftType: 3220, RightType: 3220, Num: 4, Proc: 3386}, // pg_lsn x pg_lsn: brin_minmax_union

		// brin/pg_lsn_minmax_multi_ops (family=4615)
		{Family: 4615, LeftType: 3220, RightType: 3220, Num: 1, Proc: 4616}, // pg_lsn x pg_lsn: brin_minmax_multi_opcinfo
		{Family: 4615, LeftType: 3220, RightType: 3220, Num: 2, Proc: 4617}, // pg_lsn x pg_lsn: brin_minmax_multi_add_value
		{Family: 4615, LeftType: 3220, RightType: 3220, Num: 3, Proc: 4618}, // pg_lsn x pg_lsn: brin_minmax_multi_consistent
		{Family: 4615, LeftType: 3220, RightType: 3220, Num: 4, Proc: 4619}, // pg_lsn x pg_lsn: brin_minmax_multi_union
		{Family: 4615, LeftType: 3220, RightType: 3220, Num: 5, Proc: 4620}, // pg_lsn x pg_lsn: brin_minmax_multi_options
		{Family: 4615, LeftType: 3220, RightType: 3220, Num: 11, Proc: 4633}, // pg_lsn x pg_lsn: brin_minmax_multi_distance_pg_lsn

		// brin/pg_lsn_bloom_ops (family=4590)
		{Family: 4590, LeftType: 3220, RightType: 3220, Num: 1, Proc: 4591}, // pg_lsn x pg_lsn: brin_bloom_opcinfo
		{Family: 4590, LeftType: 3220, RightType: 3220, Num: 2, Proc: 4592}, // pg_lsn x pg_lsn: brin_bloom_add_value
		{Family: 4590, LeftType: 3220, RightType: 3220, Num: 3, Proc: 4593}, // pg_lsn x pg_lsn: brin_bloom_consistent
		{Family: 4590, LeftType: 3220, RightType: 3220, Num: 4, Proc: 4594}, // pg_lsn x pg_lsn: brin_bloom_union
		{Family: 4590, LeftType: 3220, RightType: 3220, Num: 5, Proc: 4595}, // pg_lsn x pg_lsn: brin_bloom_options
		{Family: 4590, LeftType: 3220, RightType: 3220, Num: 11, Proc: 3252}, // pg_lsn x pg_lsn: pg_lsn_hash

		// brin/box_inclusion_ops (family=4104)
		{Family: 4104, LeftType: 603, RightType: 603, Num: 1, Proc: 4105}, // box x box: brin_inclusion_opcinfo
		{Family: 4104, LeftType: 603, RightType: 603, Num: 2, Proc: 4106}, // box x box: brin_inclusion_add_value
		{Family: 4104, LeftType: 603, RightType: 603, Num: 3, Proc: 4107}, // box x box: brin_inclusion_consistent
		{Family: 4104, LeftType: 603, RightType: 603, Num: 4, Proc: 4108}, // box x box: brin_inclusion_union
		{Family: 4104, LeftType: 603, RightType: 603, Num: 11, Proc: 4067}, // box x box: bound_box
		{Family: 4104, LeftType: 603, RightType: 603, Num: 13, Proc: 187}, // box x box: box_contain
	}
	for i := range out {
		out[i].OID = baseOID + uint32(i)
	}
	return out
}
