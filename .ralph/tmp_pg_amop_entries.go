// Auto-generated from postgres/src/include/catalog/pg_amop.dat
// 945 entries total: 285 btree (57 type-pairs x 5 strategies) + 660 non-btree
//
// AM OIDs: btree=403, hash=405, gist=783, gin=2742, spgist=4000, brin=3580
//
// addPair(opfamilyOID, leftTypeOID, rightTypeOID, amOID, [5]uint32{lt, le, eq, ge, gt})
// amOp(opfamilyOID, leftTypeOID, rightTypeOID, strategy, operatorOID, amOID)

// ================================================================
// BTREE entries: 57 type-pairs, 285 total operator rows
// Strategies: 1=lt, 2=le, 3=eq, 4=ge, 5=gt
// ================================================================

	// family=1976 (integer_ops) btree
	addPair(1976, 21, 21, amBtree, [5]uint32{95, 522, 94, 524, 520}) // int2 x int2
	addPair(1976, 21, 23, amBtree, [5]uint32{534, 540, 532, 542, 536}) // int2 x int4
	addPair(1976, 21, 20, amBtree, [5]uint32{1864, 1866, 1862, 1867, 1865}) // int2 x int8
	addPair(1976, 23, 23, amBtree, [5]uint32{97, 523, 96, 525, 521}) // int4 x int4
	addPair(1976, 23, 21, amBtree, [5]uint32{535, 541, 533, 543, 537}) // int4 x int2
	addPair(1976, 23, 20, amBtree, [5]uint32{37, 80, 15, 82, 76}) // int4 x int8
	addPair(1976, 20, 20, amBtree, [5]uint32{412, 414, 410, 415, 413}) // int8 x int8
	addPair(1976, 20, 21, amBtree, [5]uint32{1870, 1872, 1868, 1873, 1871}) // int8 x int2
	addPair(1976, 20, 23, amBtree, [5]uint32{418, 420, 416, 430, 419}) // int8 x int4

	// family=1989 (oid_ops) btree
	addPair(1989, 26, 26, amBtree, [5]uint32{609, 611, 607, 612, 610}) // oid x oid

	// family=5067 (xid8_ops) btree
	addPair(5067, 5069, 5069, amBtree, [5]uint32{5073, 5075, 5068, 5076, 5074}) // xid8 x xid8

	// family=2789 (tid_ops) btree
	addPair(2789, 27, 27, amBtree, [5]uint32{2799, 2801, 387, 2802, 2800}) // tid x tid

	// family=1991 (oidvector_ops) btree
	addPair(1991, 30, 30, amBtree, [5]uint32{645, 647, 649, 648, 646}) // oidvector x oidvector

	// family=1970 (float_ops) btree
	addPair(1970, 700, 700, amBtree, [5]uint32{622, 624, 620, 625, 623}) // float4 x float4
	addPair(1970, 700, 701, amBtree, [5]uint32{1122, 1124, 1120, 1125, 1123}) // float4 x float8
	addPair(1970, 701, 701, amBtree, [5]uint32{672, 673, 670, 675, 674}) // float8 x float8
	addPair(1970, 701, 700, amBtree, [5]uint32{1132, 1134, 1130, 1135, 1133}) // float8 x float4

	// family=429 (char_ops) btree
	addPair(429, 18, 18, amBtree, [5]uint32{631, 632, 92, 634, 633}) // char x char

	// family=1994 (text_ops) btree
	addPair(1994, 25, 25, amBtree, [5]uint32{664, 665, 98, 667, 666}) // text x text
	addPair(1994, 19, 19, amBtree, [5]uint32{660, 661, 93, 663, 662}) // name x name
	addPair(1994, 19, 25, amBtree, [5]uint32{255, 256, 254, 257, 258}) // name x text
	addPair(1994, 25, 19, amBtree, [5]uint32{261, 262, 260, 263, 264}) // text x name

	// family=426 (bpchar_ops) btree
	addPair(426, 1042, 1042, amBtree, [5]uint32{1058, 1059, 1054, 1061, 1060}) // bpchar x bpchar

	// family=428 (bytea_ops) btree
	addPair(428, 17, 17, amBtree, [5]uint32{1957, 1958, 1955, 1960, 1959}) // bytea x bytea

	// family=434 (datetime_ops) btree
	addPair(434, 1082, 1082, amBtree, [5]uint32{1095, 1096, 1093, 1098, 1097}) // date x date
	addPair(434, 1082, 1114, amBtree, [5]uint32{2345, 2346, 2347, 2348, 2349}) // date x timestamp
	addPair(434, 1082, 1184, amBtree, [5]uint32{2358, 2359, 2360, 2361, 2362}) // date x timestamptz
	addPair(434, 1114, 1114, amBtree, [5]uint32{2062, 2063, 2060, 2065, 2064}) // timestamp x timestamp
	addPair(434, 1114, 1082, amBtree, [5]uint32{2371, 2372, 2373, 2374, 2375}) // timestamp x date
	addPair(434, 1114, 1184, amBtree, [5]uint32{2534, 2535, 2536, 2537, 2538}) // timestamp x timestamptz
	addPair(434, 1184, 1184, amBtree, [5]uint32{1322, 1323, 1320, 1325, 1324}) // timestamptz x timestamptz
	addPair(434, 1184, 1082, amBtree, [5]uint32{2384, 2385, 2386, 2387, 2388}) // timestamptz x date
	addPair(434, 1184, 1114, amBtree, [5]uint32{2540, 2541, 2542, 2543, 2544}) // timestamptz x timestamp

	// family=1996 (time_ops) btree
	addPair(1996, 1083, 1083, amBtree, [5]uint32{1110, 1111, 1108, 1113, 1112}) // time x time

	// family=2000 (timetz_ops) btree
	addPair(2000, 1266, 1266, amBtree, [5]uint32{1552, 1553, 1550, 1555, 1554}) // timetz x timetz

	// family=1982 (interval_ops) btree
	addPair(1982, 1186, 1186, amBtree, [5]uint32{1332, 1333, 1330, 1335, 1334}) // interval x interval

	// family=1984 (macaddr_ops) btree
	addPair(1984, 829, 829, amBtree, [5]uint32{1222, 1223, 1220, 1225, 1224}) // macaddr x macaddr

	// family=3371 (macaddr8_ops) btree
	addPair(3371, 774, 774, amBtree, [5]uint32{3364, 3365, 3362, 3367, 3366}) // macaddr8 x macaddr8

	// family=1974 (network_ops) btree
	addPair(1974, 869, 869, amBtree, [5]uint32{1203, 1204, 1201, 1206, 1205}) // inet x inet

	// family=1988 (numeric_ops) btree
	addPair(1988, 1700, 1700, amBtree, [5]uint32{1754, 1755, 1752, 1757, 1756}) // numeric x numeric

	// family=424 (bool_ops) btree
	addPair(424, 16, 16, amBtree, [5]uint32{58, 1694, 91, 1695, 59}) // bool x bool

	// family=423 (bit_ops) btree
	addPair(423, 1560, 1560, amBtree, [5]uint32{1786, 1788, 1784, 1789, 1787}) // bit x bit

	// family=2002 (varbit_ops) btree
	addPair(2002, 1562, 1562, amBtree, [5]uint32{1806, 1808, 1804, 1809, 1807}) // varbit x varbit

	// family=2095 (text_pattern_ops) btree
	addPair(2095, 25, 25, amBtree, [5]uint32{2314, 2315, 98, 2317, 2318}) // text x text

	// family=2097 (bpchar_pattern_ops) btree
	addPair(2097, 1042, 1042, amBtree, [5]uint32{2326, 2327, 1054, 2329, 2330}) // bpchar x bpchar

	// family=2099 (money_ops) btree
	addPair(2099, 790, 790, amBtree, [5]uint32{902, 904, 900, 905, 903}) // money x money

	// family=397 (array_ops) btree
	addPair(397, 2277, 2277, amBtree, [5]uint32{1072, 1074, 1070, 1075, 1073}) // anyarray x anyarray

	// family=2994 (record_ops) btree
	addPair(2994, 2249, 2249, amBtree, [5]uint32{2990, 2992, 2988, 2993, 2991}) // record x record

	// family=3194 (record_image_ops) btree
	addPair(3194, 2249, 2249, amBtree, [5]uint32{3190, 3192, 3188, 3193, 3191}) // record x record

	// family=2968 (uuid_ops) btree
	addPair(2968, 2950, 2950, amBtree, [5]uint32{2974, 2976, 2972, 2977, 2975}) // uuid x uuid

	// family=3253 (pg_lsn_ops) btree
	addPair(3253, 3220, 3220, amBtree, [5]uint32{3224, 3226, 3222, 3227, 3225}) // pg_lsn x pg_lsn

	// family=3522 (enum_ops) btree
	addPair(3522, 3500, 3500, amBtree, [5]uint32{3518, 3520, 3516, 3521, 3519}) // anyenum x anyenum

	// family=3626 (tsvector_ops) btree
	addPair(3626, 3614, 3614, amBtree, [5]uint32{3627, 3628, 3629, 3631, 3632}) // tsvector x tsvector

	// family=3683 (tsquery_ops) btree
	addPair(3683, 3615, 3615, amBtree, [5]uint32{3674, 3675, 3676, 3678, 3679}) // tsquery x tsquery

	// family=3901 (range_ops) btree
	addPair(3901, 3831, 3831, amBtree, [5]uint32{3884, 3885, 3882, 3886, 3887}) // anyrange x anyrange

	// family=4199 (multirange_ops) btree
	addPair(4199, 4537, 4537, amBtree, [5]uint32{2862, 2863, 2860, 2864, 2865}) // anymultirange x anymultirange

	// family=4033 (jsonb_ops) btree
	addPair(4033, 3802, 3802, amBtree, [5]uint32{3242, 3244, 3240, 3245, 3243}) // jsonb x jsonb

	// Total btree addPair calls: 57 (285 rows)

	// ================================================================
	// NON-BTREE entries: hash, gist, gin, spgist, brin
	// amOp(opfamilyOID, leftTypeOID, rightTypeOID, strategy, operatorOID, amOID)
	// ================================================================

	// family=427 (bpchar_ops) hash
	amOp(427, 1042, 1042, 1, 1054, amHash) // strat=1 =(bpchar,bpchar) (bpchar x bpchar)

	// family=431 (char_ops) hash
	amOp(431, 18, 18, 1, 92, amHash) // strat=1 =(char,char) (char x char)

	// family=435 (date_ops) hash
	amOp(435, 1082, 1082, 1, 1093, amHash) // strat=1 =(date,date) (date x date)

	// family=1971 (float_ops) hash
	amOp(1971, 700, 700, 1, 620, amHash) // strat=1 =(float4,float4) (float4 x float4)
	amOp(1971, 701, 701, 1, 670, amHash) // strat=1 =(float8,float8) (float8 x float8)
	amOp(1971, 700, 701, 1, 1120, amHash) // strat=1 =(float4,float8) (float4 x float8)
	amOp(1971, 701, 700, 1, 1130, amHash) // strat=1 =(float8,float4) (float8 x float4)

	// family=1975 (network_ops) hash
	amOp(1975, 869, 869, 1, 1201, amHash) // strat=1 =(inet,inet) (inet x inet)

	// family=1977 (integer_ops) hash
	amOp(1977, 21, 21, 1, 94, amHash) // strat=1 =(int2,int2) (int2 x int2)
	amOp(1977, 23, 23, 1, 96, amHash) // strat=1 =(int4,int4) (int4 x int4)
	amOp(1977, 20, 20, 1, 410, amHash) // strat=1 =(int8,int8) (int8 x int8)
	amOp(1977, 21, 23, 1, 532, amHash) // strat=1 =(int2,int4) (int2 x int4)
	amOp(1977, 21, 20, 1, 1862, amHash) // strat=1 =(int2,int8) (int2 x int8)
	amOp(1977, 23, 21, 1, 533, amHash) // strat=1 =(int4,int2) (int4 x int2)
	amOp(1977, 23, 20, 1, 15, amHash) // strat=1 =(int4,int8) (int4 x int8)
	amOp(1977, 20, 21, 1, 1868, amHash) // strat=1 =(int8,int2) (int8 x int2)
	amOp(1977, 20, 23, 1, 416, amHash) // strat=1 =(int8,int4) (int8 x int4)

	// family=1983 (interval_ops) hash
	amOp(1983, 1186, 1186, 1, 1330, amHash) // strat=1 =(interval,interval) (interval x interval)

	// family=1985 (macaddr_ops) hash
	amOp(1985, 829, 829, 1, 1220, amHash) // strat=1 =(macaddr,macaddr) (macaddr x macaddr)

	// family=3372 (macaddr8_ops) hash
	amOp(3372, 774, 774, 1, 3362, amHash) // strat=1 =(macaddr8,macaddr8) (macaddr8 x macaddr8)

	// family=1990 (oid_ops) hash
	amOp(1990, 26, 26, 1, 607, amHash) // strat=1 =(oid,oid) (oid x oid)

	// family=1992 (oidvector_ops) hash
	amOp(1992, 30, 30, 1, 649, amHash) // strat=1 =(oidvector,oidvector) (oidvector x oidvector)

	// family=6194 (record_ops) hash
	amOp(6194, 2249, 2249, 1, 2988, amHash) // strat=1 =(record,record) (record x record)

	// family=1995 (text_ops) hash
	amOp(1995, 25, 25, 1, 98, amHash) // strat=1 =(text,text) (text x text)
	amOp(1995, 19, 19, 1, 93, amHash) // strat=1 =(name,name) (name x name)
	amOp(1995, 19, 25, 1, 254, amHash) // strat=1 =(name,text) (name x text)
	amOp(1995, 25, 19, 1, 260, amHash) // strat=1 =(text,name) (text x name)

	// family=1997 (time_ops) hash
	amOp(1997, 1083, 1083, 1, 1108, amHash) // strat=1 =(time,time) (time x time)

	// family=1999 (timestamptz_ops) hash
	amOp(1999, 1184, 1184, 1, 1320, amHash) // strat=1 =(timestamptz,timestamptz) (timestamptz x timestamptz)

	// family=2001 (timetz_ops) hash
	amOp(2001, 1266, 1266, 1, 1550, amHash) // strat=1 =(timetz,timetz) (timetz x timetz)

	// family=2040 (timestamp_ops) hash
	amOp(2040, 1114, 1114, 1, 2060, amHash) // strat=1 =(timestamp,timestamp) (timestamp x timestamp)

	// family=2222 (bool_ops) hash
	amOp(2222, 16, 16, 1, 91, amHash) // strat=1 =(bool,bool) (bool x bool)

	// family=2223 (bytea_ops) hash
	amOp(2223, 17, 17, 1, 1955, amHash) // strat=1 =(bytea,bytea) (bytea x bytea)

	// family=2225 (xid_ops) hash
	amOp(2225, 28, 28, 1, 352, amHash) // strat=1 =(xid,xid) (xid x xid)

	// family=5032 (xid8_ops) hash
	amOp(5032, 5069, 5069, 1, 5068, amHash) // strat=1 =(xid8,xid8) (xid8 x xid8)

	// family=2226 (cid_ops) hash
	amOp(2226, 29, 29, 1, 385, amHash) // strat=1 =(cid,cid) (cid x cid)

	// family=2227 (tid_ops) hash
	amOp(2227, 27, 27, 1, 387, amHash) // strat=1 =(tid,tid) (tid x tid)

	// family=2229 (text_pattern_ops) hash
	amOp(2229, 25, 25, 1, 98, amHash) // strat=1 =(text,text) (text x text)

	// family=2231 (bpchar_pattern_ops) hash
	amOp(2231, 1042, 1042, 1, 1054, amHash) // strat=1 =(bpchar,bpchar) (bpchar x bpchar)

	// family=2235 (aclitem_ops) hash
	amOp(2235, 1033, 1033, 1, 974, amHash) // strat=1 =(aclitem,aclitem) (aclitem x aclitem)

	// family=2969 (uuid_ops) hash
	amOp(2969, 2950, 2950, 1, 2972, amHash) // strat=1 =(uuid,uuid) (uuid x uuid)

	// family=3254 (pg_lsn_ops) hash
	amOp(3254, 3220, 3220, 1, 3222, amHash) // strat=1 =(pg_lsn,pg_lsn) (pg_lsn x pg_lsn)

	// family=1998 (numeric_ops) hash
	amOp(1998, 1700, 1700, 1, 1752, amHash) // strat=1 =(numeric,numeric) (numeric x numeric)

	// family=627 (array_ops) hash
	amOp(627, 2277, 2277, 1, 1070, amHash) // strat=1 =(anyarray,anyarray) (anyarray x anyarray)

	// family=2593 (box_ops) gist
	amOp(2593, 603, 603, 1, 493, amGist) // strat=1 <<(box,box) (box x box)
	amOp(2593, 603, 603, 2, 494, amGist) // strat=2 &<(box,box) (box x box)
	amOp(2593, 603, 603, 3, 500, amGist) // strat=3 &&(box,box) (box x box)
	amOp(2593, 603, 603, 4, 495, amGist) // strat=4 &>(box,box) (box x box)
	amOp(2593, 603, 603, 5, 496, amGist) // strat=5 >>(box,box) (box x box)
	amOp(2593, 603, 603, 6, 499, amGist) // strat=6 ~=(box,box) (box x box)
	amOp(2593, 603, 603, 7, 498, amGist) // strat=7 @>(box,box) (box x box)
	amOp(2593, 603, 603, 8, 497, amGist) // strat=8 <@(box,box) (box x box)
	amOp(2593, 603, 603, 9, 2571, amGist) // strat=9 &<|(box,box) (box x box)
	amOp(2593, 603, 603, 10, 2570, amGist) // strat=10 <<|(box,box) (box x box)
	amOp(2593, 603, 603, 11, 2573, amGist) // strat=11 |>>(box,box) (box x box)
	amOp(2593, 603, 603, 12, 2572, amGist) // strat=12 |&>(box,box) (box x box)
	amOp(2593, 603, 600, 15, 606, amGist) // strat=15 <->(box,point) (box x point)

	// family=1029 (point_ops) gist
	amOp(1029, 600, 600, 11, 4161, amGist) // strat=11 |>>(point,point) (point x point)
	amOp(1029, 600, 600, 30, 506, amGist) // strat=30 >^(point,point) (point x point)
	amOp(1029, 600, 600, 1, 507, amGist) // strat=1 <<(point,point) (point x point)
	amOp(1029, 600, 600, 5, 508, amGist) // strat=5 >>(point,point) (point x point)
	amOp(1029, 600, 600, 10, 4162, amGist) // strat=10 <<|(point,point) (point x point)
	amOp(1029, 600, 600, 29, 509, amGist) // strat=29 <^(point,point) (point x point)
	amOp(1029, 600, 600, 6, 510, amGist) // strat=6 ~=(point,point) (point x point)
	amOp(1029, 600, 600, 15, 517, amGist) // strat=15 <->(point,point) (point x point)
	amOp(1029, 600, 603, 28, 511, amGist) // strat=28 <@(point,box) (point x box)
	amOp(1029, 600, 604, 48, 756, amGist) // strat=48 <@(point,polygon) (point x polygon)
	amOp(1029, 600, 718, 68, 758, amGist) // strat=68 <@(point,circle) (point x circle)

	// family=2594 (poly_ops) gist
	amOp(2594, 604, 604, 1, 485, amGist) // strat=1 <<(polygon,polygon) (polygon x polygon)
	amOp(2594, 604, 604, 2, 486, amGist) // strat=2 &<(polygon,polygon) (polygon x polygon)
	amOp(2594, 604, 604, 3, 492, amGist) // strat=3 &&(polygon,polygon) (polygon x polygon)
	amOp(2594, 604, 604, 4, 487, amGist) // strat=4 &>(polygon,polygon) (polygon x polygon)
	amOp(2594, 604, 604, 5, 488, amGist) // strat=5 >>(polygon,polygon) (polygon x polygon)
	amOp(2594, 604, 604, 6, 491, amGist) // strat=6 ~=(polygon,polygon) (polygon x polygon)
	amOp(2594, 604, 604, 7, 490, amGist) // strat=7 @>(polygon,polygon) (polygon x polygon)
	amOp(2594, 604, 604, 8, 489, amGist) // strat=8 <@(polygon,polygon) (polygon x polygon)
	amOp(2594, 604, 604, 9, 2575, amGist) // strat=9 &<|(polygon,polygon) (polygon x polygon)
	amOp(2594, 604, 604, 10, 2574, amGist) // strat=10 <<|(polygon,polygon) (polygon x polygon)
	amOp(2594, 604, 604, 11, 2577, amGist) // strat=11 |>>(polygon,polygon) (polygon x polygon)
	amOp(2594, 604, 604, 12, 2576, amGist) // strat=12 |&>(polygon,polygon) (polygon x polygon)
	amOp(2594, 604, 600, 15, 3289, amGist) // strat=15 <->(polygon,point) (polygon x point)

	// family=2595 (circle_ops) gist
	amOp(2595, 718, 718, 1, 1506, amGist) // strat=1 <<(circle,circle) (circle x circle)
	amOp(2595, 718, 718, 2, 1507, amGist) // strat=2 &<(circle,circle) (circle x circle)
	amOp(2595, 718, 718, 3, 1513, amGist) // strat=3 &&(circle,circle) (circle x circle)
	amOp(2595, 718, 718, 4, 1508, amGist) // strat=4 &>(circle,circle) (circle x circle)
	amOp(2595, 718, 718, 5, 1509, amGist) // strat=5 >>(circle,circle) (circle x circle)
	amOp(2595, 718, 718, 6, 1512, amGist) // strat=6 ~=(circle,circle) (circle x circle)
	amOp(2595, 718, 718, 7, 1511, amGist) // strat=7 @>(circle,circle) (circle x circle)
	amOp(2595, 718, 718, 8, 1510, amGist) // strat=8 <@(circle,circle) (circle x circle)
	amOp(2595, 718, 718, 9, 2589, amGist) // strat=9 &<|(circle,circle) (circle x circle)
	amOp(2595, 718, 718, 10, 1515, amGist) // strat=10 <<|(circle,circle) (circle x circle)
	amOp(2595, 718, 718, 11, 1514, amGist) // strat=11 |>>(circle,circle) (circle x circle)
	amOp(2595, 718, 718, 12, 2590, amGist) // strat=12 |&>(circle,circle) (circle x circle)
	amOp(2595, 718, 600, 15, 3291, amGist) // strat=15 <->(circle,point) (circle x point)

	// family=2745 (array_ops) gin
	amOp(2745, 2277, 2277, 1, 2750, amGin) // strat=1 &&(anyarray,anyarray) (anyarray x anyarray)
	amOp(2745, 2277, 2277, 2, 2751, amGin) // strat=2 @>(anyarray,anyarray) (anyarray x anyarray)
	amOp(2745, 2277, 2277, 3, 2752, amGin) // strat=3 <@(anyarray,anyarray) (anyarray x anyarray)
	amOp(2745, 2277, 2277, 4, 1070, amGin) // strat=4 =(anyarray,anyarray) (anyarray x anyarray)

	// family=3523 (enum_ops) hash
	amOp(3523, 3500, 3500, 1, 3516, amHash) // strat=1 =(anyenum,anyenum) (anyenum x anyenum)

	// family=3655 (tsvector_ops) gist
	amOp(3655, 3614, 3615, 1, 3636, amGist) // strat=1 @@(tsvector,tsquery) (tsvector x tsquery)

	// family=3659 (tsvector_ops) gin
	amOp(3659, 3614, 3615, 1, 3636, amGin) // strat=1 @@(tsvector,tsquery) (tsvector x tsquery)
	amOp(3659, 3614, 3615, 2, 3660, amGin) // strat=2 @@@(tsvector,tsquery) (tsvector x tsquery)

	// family=3702 (tsquery_ops) gist
	amOp(3702, 3615, 3615, 7, 3693, amGist) // strat=7 @>(tsquery,tsquery) (tsquery x tsquery)
	amOp(3702, 3615, 3615, 8, 3694, amGist) // strat=8 <@(tsquery,tsquery) (tsquery x tsquery)

	// family=3903 (range_ops) hash
	amOp(3903, 3831, 3831, 1, 3882, amHash) // strat=1 =(anyrange,anyrange) (anyrange x anyrange)

	// family=3919 (range_ops) gist
	amOp(3919, 3831, 3831, 1, 3893, amGist) // strat=1 <<(anyrange,anyrange) (anyrange x anyrange)
	amOp(3919, 3831, 4537, 1, 4395, amGist) // strat=1 <<(anyrange,anymultirange) (anyrange x anymultirange)
	amOp(3919, 3831, 3831, 2, 3895, amGist) // strat=2 &<(anyrange,anyrange) (anyrange x anyrange)
	amOp(3919, 3831, 4537, 2, 2875, amGist) // strat=2 &<(anyrange,anymultirange) (anyrange x anymultirange)
	amOp(3919, 3831, 3831, 3, 3888, amGist) // strat=3 &&(anyrange,anyrange) (anyrange x anyrange)
	amOp(3919, 3831, 4537, 3, 2866, amGist) // strat=3 &&(anyrange,anymultirange) (anyrange x anymultirange)
	amOp(3919, 3831, 3831, 4, 3896, amGist) // strat=4 &>(anyrange,anyrange) (anyrange x anyrange)
	amOp(3919, 3831, 4537, 4, 3585, amGist) // strat=4 &>(anyrange,anymultirange) (anyrange x anymultirange)
	amOp(3919, 3831, 3831, 5, 3894, amGist) // strat=5 >>(anyrange,anyrange) (anyrange x anyrange)
	amOp(3919, 3831, 4537, 5, 4398, amGist) // strat=5 >>(anyrange,anymultirange) (anyrange x anymultirange)
	amOp(3919, 3831, 3831, 6, 3897, amGist) // strat=6 -|-(anyrange,anyrange) (anyrange x anyrange)
	amOp(3919, 3831, 4537, 6, 4179, amGist) // strat=6 -|-(anyrange,anymultirange) (anyrange x anymultirange)
	amOp(3919, 3831, 3831, 7, 3890, amGist) // strat=7 @>(anyrange,anyrange) (anyrange x anyrange)
	amOp(3919, 3831, 4537, 7, 4539, amGist) // strat=7 @>(anyrange,anymultirange) (anyrange x anymultirange)
	amOp(3919, 3831, 3831, 8, 3892, amGist) // strat=8 <@(anyrange,anyrange) (anyrange x anyrange)
	amOp(3919, 3831, 4537, 8, 2873, amGist) // strat=8 <@(anyrange,anymultirange) (anyrange x anymultirange)
	amOp(3919, 3831, 2283, 16, 3889, amGist) // strat=16 @>(anyrange,anyelement) (anyrange x anyelement)
	amOp(3919, 3831, 3831, 18, 3882, amGist) // strat=18 =(anyrange,anyrange) (anyrange x anyrange)

	// family=6158 (multirange_ops) gist
	amOp(6158, 4537, 4537, 1, 4397, amGist) // strat=1 <<(anymultirange,anymultirange) (anymultirange x anymultirange)
	amOp(6158, 4537, 3831, 1, 4396, amGist) // strat=1 <<(anymultirange,anyrange) (anymultirange x anyrange)
	amOp(6158, 4537, 4537, 2, 2877, amGist) // strat=2 &<(anymultirange,anymultirange) (anymultirange x anymultirange)
	amOp(6158, 4537, 3831, 2, 2876, amGist) // strat=2 &<(anymultirange,anyrange) (anymultirange x anyrange)
	amOp(6158, 4537, 4537, 3, 2868, amGist) // strat=3 &&(anymultirange,anymultirange) (anymultirange x anymultirange)
	amOp(6158, 4537, 3831, 3, 2867, amGist) // strat=3 &&(anymultirange,anyrange) (anymultirange x anyrange)
	amOp(6158, 4537, 4537, 4, 4142, amGist) // strat=4 &>(anymultirange,anymultirange) (anymultirange x anymultirange)
	amOp(6158, 4537, 3831, 4, 4035, amGist) // strat=4 &>(anymultirange,anyrange) (anymultirange x anyrange)
	amOp(6158, 4537, 4537, 5, 4400, amGist) // strat=5 >>(anymultirange,anymultirange) (anymultirange x anymultirange)
	amOp(6158, 4537, 3831, 5, 4399, amGist) // strat=5 >>(anymultirange,anyrange) (anymultirange x anyrange)
	amOp(6158, 4537, 4537, 6, 4198, amGist) // strat=6 -|-(anymultirange,anymultirange) (anymultirange x anymultirange)
	amOp(6158, 4537, 3831, 6, 4180, amGist) // strat=6 -|-(anymultirange,anyrange) (anymultirange x anyrange)
	amOp(6158, 4537, 4537, 7, 2871, amGist) // strat=7 @>(anymultirange,anymultirange) (anymultirange x anymultirange)
	amOp(6158, 4537, 3831, 7, 2870, amGist) // strat=7 @>(anymultirange,anyrange) (anymultirange x anyrange)
	amOp(6158, 4537, 4537, 8, 2874, amGist) // strat=8 <@(anymultirange,anymultirange) (anymultirange x anymultirange)
	amOp(6158, 4537, 3831, 8, 4540, amGist) // strat=8 <@(anymultirange,anyrange) (anymultirange x anyrange)
	amOp(6158, 4537, 2283, 16, 2869, amGist) // strat=16 @>(anymultirange,anyelement) (anymultirange x anyelement)
	amOp(6158, 4537, 4537, 18, 2860, amGist) // strat=18 =(anymultirange,anymultirange) (anymultirange x anymultirange)

	// family=4225 (multirange_ops) hash
	amOp(4225, 4537, 4537, 1, 2860, amHash) // strat=1 =(anymultirange,anymultirange) (anymultirange x anymultirange)

	// family=4015 (quad_point_ops) spgist
	amOp(4015, 600, 600, 11, 4161, amSpgist) // strat=11 |>>(point,point) (point x point)
	amOp(4015, 600, 600, 30, 506, amSpgist) // strat=30 >^(point,point) (point x point)
	amOp(4015, 600, 600, 1, 507, amSpgist) // strat=1 <<(point,point) (point x point)
	amOp(4015, 600, 600, 5, 508, amSpgist) // strat=5 >>(point,point) (point x point)
	amOp(4015, 600, 600, 10, 4162, amSpgist) // strat=10 <<|(point,point) (point x point)
	amOp(4015, 600, 600, 29, 509, amSpgist) // strat=29 <^(point,point) (point x point)
	amOp(4015, 600, 600, 6, 510, amSpgist) // strat=6 ~=(point,point) (point x point)
	amOp(4015, 600, 603, 8, 511, amSpgist) // strat=8 <@(point,box) (point x box)
	amOp(4015, 600, 600, 15, 517, amSpgist) // strat=15 <->(point,point) (point x point)

	// family=4016 (kd_point_ops) spgist
	amOp(4016, 600, 600, 11, 4161, amSpgist) // strat=11 |>>(point,point) (point x point)
	amOp(4016, 600, 600, 30, 506, amSpgist) // strat=30 >^(point,point) (point x point)
	amOp(4016, 600, 600, 1, 507, amSpgist) // strat=1 <<(point,point) (point x point)
	amOp(4016, 600, 600, 5, 508, amSpgist) // strat=5 >>(point,point) (point x point)
	amOp(4016, 600, 600, 10, 4162, amSpgist) // strat=10 <<|(point,point) (point x point)
	amOp(4016, 600, 600, 29, 509, amSpgist) // strat=29 <^(point,point) (point x point)
	amOp(4016, 600, 600, 6, 510, amSpgist) // strat=6 ~=(point,point) (point x point)
	amOp(4016, 600, 603, 8, 511, amSpgist) // strat=8 <@(point,box) (point x box)
	amOp(4016, 600, 600, 15, 517, amSpgist) // strat=15 <->(point,point) (point x point)

	// family=4017 (text_ops) spgist
	amOp(4017, 25, 25, 1, 2314, amSpgist) // strat=1 ~<~(text,text) (text x text)
	amOp(4017, 25, 25, 2, 2315, amSpgist) // strat=2 ~<=~(text,text) (text x text)
	amOp(4017, 25, 25, 3, 98, amSpgist) // strat=3 =(text,text) (text x text)
	amOp(4017, 25, 25, 4, 2317, amSpgist) // strat=4 ~>=~(text,text) (text x text)
	amOp(4017, 25, 25, 5, 2318, amSpgist) // strat=5 ~>~(text,text) (text x text)
	amOp(4017, 25, 25, 11, 664, amSpgist) // strat=11 <(text,text) (text x text)
	amOp(4017, 25, 25, 12, 665, amSpgist) // strat=12 <=(text,text) (text x text)
	amOp(4017, 25, 25, 14, 667, amSpgist) // strat=14 >=(text,text) (text x text)
	amOp(4017, 25, 25, 15, 666, amSpgist) // strat=15 >(text,text) (text x text)
	amOp(4017, 25, 25, 28, 3877, amSpgist) // strat=28 ^@(text,text) (text x text)

	// family=4034 (jsonb_ops) hash
	amOp(4034, 3802, 3802, 1, 3240, amHash) // strat=1 =(jsonb,jsonb) (jsonb x jsonb)

	// family=4036 (jsonb_ops) gin
	amOp(4036, 3802, 3802, 7, 3246, amGin) // strat=7 @>(jsonb,jsonb) (jsonb x jsonb)
	amOp(4036, 3802, 25, 9, 3247, amGin) // strat=9 ?(jsonb,text) (jsonb x text)
	amOp(4036, 3802, 1009, 10, 3248, amGin) // strat=10 ?|(jsonb,_text) (jsonb x _text)
	amOp(4036, 3802, 1009, 11, 3249, amGin) // strat=11 ?&(jsonb,_text) (jsonb x _text)
	amOp(4036, 3802, 4072, 15, 4012, amGin) // strat=15 @?(jsonb,jsonpath) (jsonb x jsonpath)
	amOp(4036, 3802, 4072, 16, 4013, amGin) // strat=16 @@(jsonb,jsonpath) (jsonb x jsonpath)

	// family=4037 (jsonb_path_ops) gin
	amOp(4037, 3802, 3802, 7, 3246, amGin) // strat=7 @>(jsonb,jsonb) (jsonb x jsonb)
	amOp(4037, 3802, 4072, 15, 4012, amGin) // strat=15 @?(jsonb,jsonpath) (jsonb x jsonpath)
	amOp(4037, 3802, 4072, 16, 4013, amGin) // strat=16 @@(jsonb,jsonpath) (jsonb x jsonpath)

	// family=3474 (range_ops) spgist
	amOp(3474, 3831, 3831, 1, 3893, amSpgist) // strat=1 <<(anyrange,anyrange) (anyrange x anyrange)
	amOp(3474, 3831, 3831, 2, 3895, amSpgist) // strat=2 &<(anyrange,anyrange) (anyrange x anyrange)
	amOp(3474, 3831, 3831, 3, 3888, amSpgist) // strat=3 &&(anyrange,anyrange) (anyrange x anyrange)
	amOp(3474, 3831, 3831, 4, 3896, amSpgist) // strat=4 &>(anyrange,anyrange) (anyrange x anyrange)
	amOp(3474, 3831, 3831, 5, 3894, amSpgist) // strat=5 >>(anyrange,anyrange) (anyrange x anyrange)
	amOp(3474, 3831, 3831, 6, 3897, amSpgist) // strat=6 -|-(anyrange,anyrange) (anyrange x anyrange)
	amOp(3474, 3831, 3831, 7, 3890, amSpgist) // strat=7 @>(anyrange,anyrange) (anyrange x anyrange)
	amOp(3474, 3831, 3831, 8, 3892, amSpgist) // strat=8 <@(anyrange,anyrange) (anyrange x anyrange)
	amOp(3474, 3831, 2283, 16, 3889, amSpgist) // strat=16 @>(anyrange,anyelement) (anyrange x anyelement)
	amOp(3474, 3831, 3831, 18, 3882, amSpgist) // strat=18 =(anyrange,anyrange) (anyrange x anyrange)

	// family=5000 (box_ops) spgist
	amOp(5000, 603, 603, 1, 493, amSpgist) // strat=1 <<(box,box) (box x box)
	amOp(5000, 603, 603, 2, 494, amSpgist) // strat=2 &<(box,box) (box x box)
	amOp(5000, 603, 603, 3, 500, amSpgist) // strat=3 &&(box,box) (box x box)
	amOp(5000, 603, 603, 4, 495, amSpgist) // strat=4 &>(box,box) (box x box)
	amOp(5000, 603, 603, 5, 496, amSpgist) // strat=5 >>(box,box) (box x box)
	amOp(5000, 603, 603, 6, 499, amSpgist) // strat=6 ~=(box,box) (box x box)
	amOp(5000, 603, 603, 7, 498, amSpgist) // strat=7 @>(box,box) (box x box)
	amOp(5000, 603, 603, 8, 497, amSpgist) // strat=8 <@(box,box) (box x box)
	amOp(5000, 603, 603, 9, 2571, amSpgist) // strat=9 &<|(box,box) (box x box)
	amOp(5000, 603, 603, 10, 2570, amSpgist) // strat=10 <<|(box,box) (box x box)
	amOp(5000, 603, 603, 11, 2573, amSpgist) // strat=11 |>>(box,box) (box x box)
	amOp(5000, 603, 603, 12, 2572, amSpgist) // strat=12 |&>(box,box) (box x box)
	amOp(5000, 603, 600, 15, 606, amSpgist) // strat=15 <->(box,point) (box x point)

	// family=5008 (poly_ops) spgist
	amOp(5008, 604, 604, 1, 485, amSpgist) // strat=1 <<(polygon,polygon) (polygon x polygon)
	amOp(5008, 604, 604, 2, 486, amSpgist) // strat=2 &<(polygon,polygon) (polygon x polygon)
	amOp(5008, 604, 604, 3, 492, amSpgist) // strat=3 &&(polygon,polygon) (polygon x polygon)
	amOp(5008, 604, 604, 4, 487, amSpgist) // strat=4 &>(polygon,polygon) (polygon x polygon)
	amOp(5008, 604, 604, 5, 488, amSpgist) // strat=5 >>(polygon,polygon) (polygon x polygon)
	amOp(5008, 604, 604, 6, 491, amSpgist) // strat=6 ~=(polygon,polygon) (polygon x polygon)
	amOp(5008, 604, 604, 7, 490, amSpgist) // strat=7 @>(polygon,polygon) (polygon x polygon)
	amOp(5008, 604, 604, 8, 489, amSpgist) // strat=8 <@(polygon,polygon) (polygon x polygon)
	amOp(5008, 604, 604, 9, 2575, amSpgist) // strat=9 &<|(polygon,polygon) (polygon x polygon)
	amOp(5008, 604, 604, 10, 2574, amSpgist) // strat=10 <<|(polygon,polygon) (polygon x polygon)
	amOp(5008, 604, 604, 11, 2577, amSpgist) // strat=11 |>>(polygon,polygon) (polygon x polygon)
	amOp(5008, 604, 604, 12, 2576, amSpgist) // strat=12 |&>(polygon,polygon) (polygon x polygon)
	amOp(5008, 604, 600, 15, 3289, amSpgist) // strat=15 <->(polygon,point) (polygon x point)

	// family=3550 (network_ops) gist
	amOp(3550, 869, 869, 3, 3552, amGist) // strat=3 &&(inet,inet) (inet x inet)
	amOp(3550, 869, 869, 18, 1201, amGist) // strat=18 =(inet,inet) (inet x inet)
	amOp(3550, 869, 869, 19, 1202, amGist) // strat=19 <>(inet,inet) (inet x inet)
	amOp(3550, 869, 869, 20, 1203, amGist) // strat=20 <(inet,inet) (inet x inet)
	amOp(3550, 869, 869, 21, 1204, amGist) // strat=21 <=(inet,inet) (inet x inet)
	amOp(3550, 869, 869, 22, 1205, amGist) // strat=22 >(inet,inet) (inet x inet)
	amOp(3550, 869, 869, 23, 1206, amGist) // strat=23 >=(inet,inet) (inet x inet)
	amOp(3550, 869, 869, 24, 931, amGist) // strat=24 <<(inet,inet) (inet x inet)
	amOp(3550, 869, 869, 25, 932, amGist) // strat=25 <<=(inet,inet) (inet x inet)
	amOp(3550, 869, 869, 26, 933, amGist) // strat=26 >>(inet,inet) (inet x inet)
	amOp(3550, 869, 869, 27, 934, amGist) // strat=27 >>=(inet,inet) (inet x inet)

	// family=3794 (network_ops) spgist
	amOp(3794, 869, 869, 3, 3552, amSpgist) // strat=3 &&(inet,inet) (inet x inet)
	amOp(3794, 869, 869, 18, 1201, amSpgist) // strat=18 =(inet,inet) (inet x inet)
	amOp(3794, 869, 869, 19, 1202, amSpgist) // strat=19 <>(inet,inet) (inet x inet)
	amOp(3794, 869, 869, 20, 1203, amSpgist) // strat=20 <(inet,inet) (inet x inet)
	amOp(3794, 869, 869, 21, 1204, amSpgist) // strat=21 <=(inet,inet) (inet x inet)
	amOp(3794, 869, 869, 22, 1205, amSpgist) // strat=22 >(inet,inet) (inet x inet)
	amOp(3794, 869, 869, 23, 1206, amSpgist) // strat=23 >=(inet,inet) (inet x inet)
	amOp(3794, 869, 869, 24, 931, amSpgist) // strat=24 <<(inet,inet) (inet x inet)
	amOp(3794, 869, 869, 25, 932, amSpgist) // strat=25 <<=(inet,inet) (inet x inet)
	amOp(3794, 869, 869, 26, 933, amSpgist) // strat=26 >>(inet,inet) (inet x inet)
	amOp(3794, 869, 869, 27, 934, amSpgist) // strat=27 >>=(inet,inet) (inet x inet)

	// family=4064 (bytea_minmax_ops) brin
	amOp(4064, 17, 17, 1, 1957, amBrin) // strat=1 <(bytea,bytea) (bytea x bytea)
	amOp(4064, 17, 17, 2, 1958, amBrin) // strat=2 <=(bytea,bytea) (bytea x bytea)
	amOp(4064, 17, 17, 3, 1955, amBrin) // strat=3 =(bytea,bytea) (bytea x bytea)
	amOp(4064, 17, 17, 4, 1960, amBrin) // strat=4 >=(bytea,bytea) (bytea x bytea)
	amOp(4064, 17, 17, 5, 1959, amBrin) // strat=5 >(bytea,bytea) (bytea x bytea)

	// family=4578 (bytea_bloom_ops) brin
	amOp(4578, 17, 17, 1, 1955, amBrin) // strat=1 =(bytea,bytea) (bytea x bytea)

	// family=4062 (char_minmax_ops) brin
	amOp(4062, 18, 18, 1, 631, amBrin) // strat=1 <(char,char) (char x char)
	amOp(4062, 18, 18, 2, 632, amBrin) // strat=2 <=(char,char) (char x char)
	amOp(4062, 18, 18, 3, 92, amBrin) // strat=3 =(char,char) (char x char)
	amOp(4062, 18, 18, 4, 634, amBrin) // strat=4 >=(char,char) (char x char)
	amOp(4062, 18, 18, 5, 633, amBrin) // strat=5 >(char,char) (char x char)

	// family=4577 (char_bloom_ops) brin
	amOp(4577, 18, 18, 1, 92, amBrin) // strat=1 =(char,char) (char x char)

	// family=4065 (name_minmax_ops) brin
	amOp(4065, 19, 19, 1, 660, amBrin) // strat=1 <(name,name) (name x name)
	amOp(4065, 19, 19, 2, 661, amBrin) // strat=2 <=(name,name) (name x name)
	amOp(4065, 19, 19, 3, 93, amBrin) // strat=3 =(name,name) (name x name)
	amOp(4065, 19, 19, 4, 663, amBrin) // strat=4 >=(name,name) (name x name)
	amOp(4065, 19, 19, 5, 662, amBrin) // strat=5 >(name,name) (name x name)

	// family=4579 (name_bloom_ops) brin
	amOp(4579, 19, 19, 1, 93, amBrin) // strat=1 =(name,name) (name x name)

	// family=4054 (integer_minmax_ops) brin
	amOp(4054, 20, 20, 1, 412, amBrin) // strat=1 <(int8,int8) (int8 x int8)
	amOp(4054, 20, 20, 2, 414, amBrin) // strat=2 <=(int8,int8) (int8 x int8)
	amOp(4054, 20, 20, 3, 410, amBrin) // strat=3 =(int8,int8) (int8 x int8)
	amOp(4054, 20, 20, 4, 415, amBrin) // strat=4 >=(int8,int8) (int8 x int8)
	amOp(4054, 20, 20, 5, 413, amBrin) // strat=5 >(int8,int8) (int8 x int8)
	amOp(4054, 20, 21, 1, 1870, amBrin) // strat=1 <(int8,int2) (int8 x int2)
	amOp(4054, 20, 21, 2, 1872, amBrin) // strat=2 <=(int8,int2) (int8 x int2)
	amOp(4054, 20, 21, 3, 1868, amBrin) // strat=3 =(int8,int2) (int8 x int2)
	amOp(4054, 20, 21, 4, 1873, amBrin) // strat=4 >=(int8,int2) (int8 x int2)
	amOp(4054, 20, 21, 5, 1871, amBrin) // strat=5 >(int8,int2) (int8 x int2)
	amOp(4054, 20, 23, 1, 418, amBrin) // strat=1 <(int8,int4) (int8 x int4)
	amOp(4054, 20, 23, 2, 420, amBrin) // strat=2 <=(int8,int4) (int8 x int4)
	amOp(4054, 20, 23, 3, 416, amBrin) // strat=3 =(int8,int4) (int8 x int4)
	amOp(4054, 20, 23, 4, 430, amBrin) // strat=4 >=(int8,int4) (int8 x int4)
	amOp(4054, 20, 23, 5, 419, amBrin) // strat=5 >(int8,int4) (int8 x int4)
	amOp(4054, 21, 21, 1, 95, amBrin) // strat=1 <(int2,int2) (int2 x int2)
	amOp(4054, 21, 21, 2, 522, amBrin) // strat=2 <=(int2,int2) (int2 x int2)
	amOp(4054, 21, 21, 3, 94, amBrin) // strat=3 =(int2,int2) (int2 x int2)
	amOp(4054, 21, 21, 4, 524, amBrin) // strat=4 >=(int2,int2) (int2 x int2)
	amOp(4054, 21, 21, 5, 520, amBrin) // strat=5 >(int2,int2) (int2 x int2)
	amOp(4054, 21, 20, 1, 1864, amBrin) // strat=1 <(int2,int8) (int2 x int8)
	amOp(4054, 21, 20, 2, 1866, amBrin) // strat=2 <=(int2,int8) (int2 x int8)
	amOp(4054, 21, 20, 3, 1862, amBrin) // strat=3 =(int2,int8) (int2 x int8)
	amOp(4054, 21, 20, 4, 1867, amBrin) // strat=4 >=(int2,int8) (int2 x int8)
	amOp(4054, 21, 20, 5, 1865, amBrin) // strat=5 >(int2,int8) (int2 x int8)
	amOp(4054, 21, 23, 1, 534, amBrin) // strat=1 <(int2,int4) (int2 x int4)
	amOp(4054, 21, 23, 2, 540, amBrin) // strat=2 <=(int2,int4) (int2 x int4)
	amOp(4054, 21, 23, 3, 532, amBrin) // strat=3 =(int2,int4) (int2 x int4)
	amOp(4054, 21, 23, 4, 542, amBrin) // strat=4 >=(int2,int4) (int2 x int4)
	amOp(4054, 21, 23, 5, 536, amBrin) // strat=5 >(int2,int4) (int2 x int4)
	amOp(4054, 23, 23, 1, 97, amBrin) // strat=1 <(int4,int4) (int4 x int4)
	amOp(4054, 23, 23, 2, 523, amBrin) // strat=2 <=(int4,int4) (int4 x int4)
	amOp(4054, 23, 23, 3, 96, amBrin) // strat=3 =(int4,int4) (int4 x int4)
	amOp(4054, 23, 23, 4, 525, amBrin) // strat=4 >=(int4,int4) (int4 x int4)
	amOp(4054, 23, 23, 5, 521, amBrin) // strat=5 >(int4,int4) (int4 x int4)
	amOp(4054, 23, 21, 1, 535, amBrin) // strat=1 <(int4,int2) (int4 x int2)
	amOp(4054, 23, 21, 2, 541, amBrin) // strat=2 <=(int4,int2) (int4 x int2)
	amOp(4054, 23, 21, 3, 533, amBrin) // strat=3 =(int4,int2) (int4 x int2)
	amOp(4054, 23, 21, 4, 543, amBrin) // strat=4 >=(int4,int2) (int4 x int2)
	amOp(4054, 23, 21, 5, 537, amBrin) // strat=5 >(int4,int2) (int4 x int2)
	amOp(4054, 23, 20, 1, 37, amBrin) // strat=1 <(int4,int8) (int4 x int8)
	amOp(4054, 23, 20, 2, 80, amBrin) // strat=2 <=(int4,int8) (int4 x int8)
	amOp(4054, 23, 20, 3, 15, amBrin) // strat=3 =(int4,int8) (int4 x int8)
	amOp(4054, 23, 20, 4, 82, amBrin) // strat=4 >=(int4,int8) (int4 x int8)
	amOp(4054, 23, 20, 5, 76, amBrin) // strat=5 >(int4,int8) (int4 x int8)

	// family=4602 (integer_minmax_multi_ops) brin
	amOp(4602, 20, 20, 1, 412, amBrin) // strat=1 <(int8,int8) (int8 x int8)
	amOp(4602, 20, 20, 2, 414, amBrin) // strat=2 <=(int8,int8) (int8 x int8)
	amOp(4602, 20, 20, 3, 410, amBrin) // strat=3 =(int8,int8) (int8 x int8)
	amOp(4602, 20, 20, 4, 415, amBrin) // strat=4 >=(int8,int8) (int8 x int8)
	amOp(4602, 20, 20, 5, 413, amBrin) // strat=5 >(int8,int8) (int8 x int8)
	amOp(4602, 20, 21, 1, 1870, amBrin) // strat=1 <(int8,int2) (int8 x int2)
	amOp(4602, 20, 21, 2, 1872, amBrin) // strat=2 <=(int8,int2) (int8 x int2)
	amOp(4602, 20, 21, 3, 1868, amBrin) // strat=3 =(int8,int2) (int8 x int2)
	amOp(4602, 20, 21, 4, 1873, amBrin) // strat=4 >=(int8,int2) (int8 x int2)
	amOp(4602, 20, 21, 5, 1871, amBrin) // strat=5 >(int8,int2) (int8 x int2)
	amOp(4602, 20, 23, 1, 418, amBrin) // strat=1 <(int8,int4) (int8 x int4)
	amOp(4602, 20, 23, 2, 420, amBrin) // strat=2 <=(int8,int4) (int8 x int4)
	amOp(4602, 20, 23, 3, 416, amBrin) // strat=3 =(int8,int4) (int8 x int4)
	amOp(4602, 20, 23, 4, 430, amBrin) // strat=4 >=(int8,int4) (int8 x int4)
	amOp(4602, 20, 23, 5, 419, amBrin) // strat=5 >(int8,int4) (int8 x int4)
	amOp(4602, 21, 21, 1, 95, amBrin) // strat=1 <(int2,int2) (int2 x int2)
	amOp(4602, 21, 21, 2, 522, amBrin) // strat=2 <=(int2,int2) (int2 x int2)
	amOp(4602, 21, 21, 3, 94, amBrin) // strat=3 =(int2,int2) (int2 x int2)
	amOp(4602, 21, 21, 4, 524, amBrin) // strat=4 >=(int2,int2) (int2 x int2)
	amOp(4602, 21, 21, 5, 520, amBrin) // strat=5 >(int2,int2) (int2 x int2)
	amOp(4602, 21, 20, 1, 1864, amBrin) // strat=1 <(int2,int8) (int2 x int8)
	amOp(4602, 21, 20, 2, 1866, amBrin) // strat=2 <=(int2,int8) (int2 x int8)
	amOp(4602, 21, 20, 3, 1862, amBrin) // strat=3 =(int2,int8) (int2 x int8)
	amOp(4602, 21, 20, 4, 1867, amBrin) // strat=4 >=(int2,int8) (int2 x int8)
	amOp(4602, 21, 20, 5, 1865, amBrin) // strat=5 >(int2,int8) (int2 x int8)
	amOp(4602, 21, 23, 1, 534, amBrin) // strat=1 <(int2,int4) (int2 x int4)
	amOp(4602, 21, 23, 2, 540, amBrin) // strat=2 <=(int2,int4) (int2 x int4)
	amOp(4602, 21, 23, 3, 532, amBrin) // strat=3 =(int2,int4) (int2 x int4)
	amOp(4602, 21, 23, 4, 542, amBrin) // strat=4 >=(int2,int4) (int2 x int4)
	amOp(4602, 21, 23, 5, 536, amBrin) // strat=5 >(int2,int4) (int2 x int4)
	amOp(4602, 23, 23, 1, 97, amBrin) // strat=1 <(int4,int4) (int4 x int4)
	amOp(4602, 23, 23, 2, 523, amBrin) // strat=2 <=(int4,int4) (int4 x int4)
	amOp(4602, 23, 23, 3, 96, amBrin) // strat=3 =(int4,int4) (int4 x int4)
	amOp(4602, 23, 23, 4, 525, amBrin) // strat=4 >=(int4,int4) (int4 x int4)
	amOp(4602, 23, 23, 5, 521, amBrin) // strat=5 >(int4,int4) (int4 x int4)
	amOp(4602, 23, 21, 1, 535, amBrin) // strat=1 <(int4,int2) (int4 x int2)
	amOp(4602, 23, 21, 2, 541, amBrin) // strat=2 <=(int4,int2) (int4 x int2)
	amOp(4602, 23, 21, 3, 533, amBrin) // strat=3 =(int4,int2) (int4 x int2)
	amOp(4602, 23, 21, 4, 543, amBrin) // strat=4 >=(int4,int2) (int4 x int2)
	amOp(4602, 23, 21, 5, 537, amBrin) // strat=5 >(int4,int2) (int4 x int2)
	amOp(4602, 23, 20, 1, 37, amBrin) // strat=1 <(int4,int8) (int4 x int8)
	amOp(4602, 23, 20, 2, 80, amBrin) // strat=2 <=(int4,int8) (int4 x int8)
	amOp(4602, 23, 20, 3, 15, amBrin) // strat=3 =(int4,int8) (int4 x int8)
	amOp(4602, 23, 20, 4, 82, amBrin) // strat=4 >=(int4,int8) (int4 x int8)
	amOp(4602, 23, 20, 5, 76, amBrin) // strat=5 >(int4,int8) (int4 x int8)

	// family=4572 (integer_bloom_ops) brin
	amOp(4572, 20, 20, 1, 410, amBrin) // strat=1 =(int8,int8) (int8 x int8)
	amOp(4572, 21, 21, 1, 94, amBrin) // strat=1 =(int2,int2) (int2 x int2)
	amOp(4572, 23, 23, 1, 96, amBrin) // strat=1 =(int4,int4) (int4 x int4)

	// family=4056 (text_minmax_ops) brin
	amOp(4056, 25, 25, 1, 664, amBrin) // strat=1 <(text,text) (text x text)
	amOp(4056, 25, 25, 2, 665, amBrin) // strat=2 <=(text,text) (text x text)
	amOp(4056, 25, 25, 3, 98, amBrin) // strat=3 =(text,text) (text x text)
	amOp(4056, 25, 25, 4, 667, amBrin) // strat=4 >=(text,text) (text x text)
	amOp(4056, 25, 25, 5, 666, amBrin) // strat=5 >(text,text) (text x text)

	// family=4573 (text_bloom_ops) brin
	amOp(4573, 25, 25, 1, 98, amBrin) // strat=1 =(text,text) (text x text)

	// family=4068 (oid_minmax_ops) brin
	amOp(4068, 26, 26, 1, 609, amBrin) // strat=1 <(oid,oid) (oid x oid)
	amOp(4068, 26, 26, 2, 611, amBrin) // strat=2 <=(oid,oid) (oid x oid)
	amOp(4068, 26, 26, 3, 607, amBrin) // strat=3 =(oid,oid) (oid x oid)
	amOp(4068, 26, 26, 4, 612, amBrin) // strat=4 >=(oid,oid) (oid x oid)
	amOp(4068, 26, 26, 5, 610, amBrin) // strat=5 >(oid,oid) (oid x oid)

	// family=4606 (oid_minmax_multi_ops) brin
	amOp(4606, 26, 26, 1, 609, amBrin) // strat=1 <(oid,oid) (oid x oid)
	amOp(4606, 26, 26, 2, 611, amBrin) // strat=2 <=(oid,oid) (oid x oid)
	amOp(4606, 26, 26, 3, 607, amBrin) // strat=3 =(oid,oid) (oid x oid)
	amOp(4606, 26, 26, 4, 612, amBrin) // strat=4 >=(oid,oid) (oid x oid)
	amOp(4606, 26, 26, 5, 610, amBrin) // strat=5 >(oid,oid) (oid x oid)

	// family=4580 (oid_bloom_ops) brin
	amOp(4580, 26, 26, 1, 607, amBrin) // strat=1 =(oid,oid) (oid x oid)

	// family=4069 (tid_minmax_ops) brin
	amOp(4069, 27, 27, 1, 2799, amBrin) // strat=1 <(tid,tid) (tid x tid)
	amOp(4069, 27, 27, 2, 2801, amBrin) // strat=2 <=(tid,tid) (tid x tid)
	amOp(4069, 27, 27, 3, 387, amBrin) // strat=3 =(tid,tid) (tid x tid)
	amOp(4069, 27, 27, 4, 2802, amBrin) // strat=4 >=(tid,tid) (tid x tid)
	amOp(4069, 27, 27, 5, 2800, amBrin) // strat=5 >(tid,tid) (tid x tid)

	// family=4581 (tid_bloom_ops) brin
	amOp(4581, 27, 27, 1, 387, amBrin) // strat=1 =(tid,tid) (tid x tid)

	// family=4607 (tid_minmax_multi_ops) brin
	amOp(4607, 27, 27, 1, 2799, amBrin) // strat=1 <(tid,tid) (tid x tid)
	amOp(4607, 27, 27, 2, 2801, amBrin) // strat=2 <=(tid,tid) (tid x tid)
	amOp(4607, 27, 27, 3, 387, amBrin) // strat=3 =(tid,tid) (tid x tid)
	amOp(4607, 27, 27, 4, 2802, amBrin) // strat=4 >=(tid,tid) (tid x tid)
	amOp(4607, 27, 27, 5, 2800, amBrin) // strat=5 >(tid,tid) (tid x tid)

	// family=4070 (float_minmax_ops) brin
	amOp(4070, 700, 700, 1, 622, amBrin) // strat=1 <(float4,float4) (float4 x float4)
	amOp(4070, 700, 700, 2, 624, amBrin) // strat=2 <=(float4,float4) (float4 x float4)
	amOp(4070, 700, 700, 3, 620, amBrin) // strat=3 =(float4,float4) (float4 x float4)
	amOp(4070, 700, 700, 4, 625, amBrin) // strat=4 >=(float4,float4) (float4 x float4)
	amOp(4070, 700, 700, 5, 623, amBrin) // strat=5 >(float4,float4) (float4 x float4)
	amOp(4070, 700, 701, 1, 1122, amBrin) // strat=1 <(float4,float8) (float4 x float8)
	amOp(4070, 700, 701, 2, 1124, amBrin) // strat=2 <=(float4,float8) (float4 x float8)
	amOp(4070, 700, 701, 3, 1120, amBrin) // strat=3 =(float4,float8) (float4 x float8)
	amOp(4070, 700, 701, 4, 1125, amBrin) // strat=4 >=(float4,float8) (float4 x float8)
	amOp(4070, 700, 701, 5, 1123, amBrin) // strat=5 >(float4,float8) (float4 x float8)
	amOp(4070, 701, 700, 1, 1132, amBrin) // strat=1 <(float8,float4) (float8 x float4)
	amOp(4070, 701, 700, 2, 1134, amBrin) // strat=2 <=(float8,float4) (float8 x float4)
	amOp(4070, 701, 700, 3, 1130, amBrin) // strat=3 =(float8,float4) (float8 x float4)
	amOp(4070, 701, 700, 4, 1135, amBrin) // strat=4 >=(float8,float4) (float8 x float4)
	amOp(4070, 701, 700, 5, 1133, amBrin) // strat=5 >(float8,float4) (float8 x float4)
	amOp(4070, 701, 701, 1, 672, amBrin) // strat=1 <(float8,float8) (float8 x float8)
	amOp(4070, 701, 701, 2, 673, amBrin) // strat=2 <=(float8,float8) (float8 x float8)
	amOp(4070, 701, 701, 3, 670, amBrin) // strat=3 =(float8,float8) (float8 x float8)
	amOp(4070, 701, 701, 4, 675, amBrin) // strat=4 >=(float8,float8) (float8 x float8)
	amOp(4070, 701, 701, 5, 674, amBrin) // strat=5 >(float8,float8) (float8 x float8)

	// family=4608 (float_minmax_multi_ops) brin
	amOp(4608, 700, 700, 1, 622, amBrin) // strat=1 <(float4,float4) (float4 x float4)
	amOp(4608, 700, 700, 2, 624, amBrin) // strat=2 <=(float4,float4) (float4 x float4)
	amOp(4608, 700, 700, 3, 620, amBrin) // strat=3 =(float4,float4) (float4 x float4)
	amOp(4608, 700, 700, 4, 625, amBrin) // strat=4 >=(float4,float4) (float4 x float4)
	amOp(4608, 700, 700, 5, 623, amBrin) // strat=5 >(float4,float4) (float4 x float4)
	amOp(4608, 700, 701, 1, 1122, amBrin) // strat=1 <(float4,float8) (float4 x float8)
	amOp(4608, 700, 701, 2, 1124, amBrin) // strat=2 <=(float4,float8) (float4 x float8)
	amOp(4608, 700, 701, 3, 1120, amBrin) // strat=3 =(float4,float8) (float4 x float8)
	amOp(4608, 700, 701, 4, 1125, amBrin) // strat=4 >=(float4,float8) (float4 x float8)
	amOp(4608, 700, 701, 5, 1123, amBrin) // strat=5 >(float4,float8) (float4 x float8)
	amOp(4608, 701, 700, 1, 1132, amBrin) // strat=1 <(float8,float4) (float8 x float4)
	amOp(4608, 701, 700, 2, 1134, amBrin) // strat=2 <=(float8,float4) (float8 x float4)
	amOp(4608, 701, 700, 3, 1130, amBrin) // strat=3 =(float8,float4) (float8 x float4)
	amOp(4608, 701, 700, 4, 1135, amBrin) // strat=4 >=(float8,float4) (float8 x float4)
	amOp(4608, 701, 700, 5, 1133, amBrin) // strat=5 >(float8,float4) (float8 x float4)
	amOp(4608, 701, 701, 1, 672, amBrin) // strat=1 <(float8,float8) (float8 x float8)
	amOp(4608, 701, 701, 2, 673, amBrin) // strat=2 <=(float8,float8) (float8 x float8)
	amOp(4608, 701, 701, 3, 670, amBrin) // strat=3 =(float8,float8) (float8 x float8)
	amOp(4608, 701, 701, 4, 675, amBrin) // strat=4 >=(float8,float8) (float8 x float8)
	amOp(4608, 701, 701, 5, 674, amBrin) // strat=5 >(float8,float8) (float8 x float8)

	// family=4582 (float_bloom_ops) brin
	amOp(4582, 700, 700, 1, 620, amBrin) // strat=1 =(float4,float4) (float4 x float4)
	amOp(4582, 701, 701, 1, 670, amBrin) // strat=1 =(float8,float8) (float8 x float8)

	// family=4074 (macaddr_minmax_ops) brin
	amOp(4074, 829, 829, 1, 1222, amBrin) // strat=1 <(macaddr,macaddr) (macaddr x macaddr)
	amOp(4074, 829, 829, 2, 1223, amBrin) // strat=2 <=(macaddr,macaddr) (macaddr x macaddr)
	amOp(4074, 829, 829, 3, 1220, amBrin) // strat=3 =(macaddr,macaddr) (macaddr x macaddr)
	amOp(4074, 829, 829, 4, 1225, amBrin) // strat=4 >=(macaddr,macaddr) (macaddr x macaddr)
	amOp(4074, 829, 829, 5, 1224, amBrin) // strat=5 >(macaddr,macaddr) (macaddr x macaddr)

	// family=4609 (macaddr_minmax_multi_ops) brin
	amOp(4609, 829, 829, 1, 1222, amBrin) // strat=1 <(macaddr,macaddr) (macaddr x macaddr)
	amOp(4609, 829, 829, 2, 1223, amBrin) // strat=2 <=(macaddr,macaddr) (macaddr x macaddr)
	amOp(4609, 829, 829, 3, 1220, amBrin) // strat=3 =(macaddr,macaddr) (macaddr x macaddr)
	amOp(4609, 829, 829, 4, 1225, amBrin) // strat=4 >=(macaddr,macaddr) (macaddr x macaddr)
	amOp(4609, 829, 829, 5, 1224, amBrin) // strat=5 >(macaddr,macaddr) (macaddr x macaddr)

	// family=4583 (macaddr_bloom_ops) brin
	amOp(4583, 829, 829, 1, 1220, amBrin) // strat=1 =(macaddr,macaddr) (macaddr x macaddr)

	// family=4109 (macaddr8_minmax_ops) brin
	amOp(4109, 774, 774, 1, 3364, amBrin) // strat=1 <(macaddr8,macaddr8) (macaddr8 x macaddr8)
	amOp(4109, 774, 774, 2, 3365, amBrin) // strat=2 <=(macaddr8,macaddr8) (macaddr8 x macaddr8)
	amOp(4109, 774, 774, 3, 3362, amBrin) // strat=3 =(macaddr8,macaddr8) (macaddr8 x macaddr8)
	amOp(4109, 774, 774, 4, 3367, amBrin) // strat=4 >=(macaddr8,macaddr8) (macaddr8 x macaddr8)
	amOp(4109, 774, 774, 5, 3366, amBrin) // strat=5 >(macaddr8,macaddr8) (macaddr8 x macaddr8)

	// family=4610 (macaddr8_minmax_multi_ops) brin
	amOp(4610, 774, 774, 1, 3364, amBrin) // strat=1 <(macaddr8,macaddr8) (macaddr8 x macaddr8)
	amOp(4610, 774, 774, 2, 3365, amBrin) // strat=2 <=(macaddr8,macaddr8) (macaddr8 x macaddr8)
	amOp(4610, 774, 774, 3, 3362, amBrin) // strat=3 =(macaddr8,macaddr8) (macaddr8 x macaddr8)
	amOp(4610, 774, 774, 4, 3367, amBrin) // strat=4 >=(macaddr8,macaddr8) (macaddr8 x macaddr8)
	amOp(4610, 774, 774, 5, 3366, amBrin) // strat=5 >(macaddr8,macaddr8) (macaddr8 x macaddr8)

	// family=4584 (macaddr8_bloom_ops) brin
	amOp(4584, 774, 774, 1, 3362, amBrin) // strat=1 =(macaddr8,macaddr8) (macaddr8 x macaddr8)

	// family=4075 (network_minmax_ops) brin
	amOp(4075, 869, 869, 1, 1203, amBrin) // strat=1 <(inet,inet) (inet x inet)
	amOp(4075, 869, 869, 2, 1204, amBrin) // strat=2 <=(inet,inet) (inet x inet)
	amOp(4075, 869, 869, 3, 1201, amBrin) // strat=3 =(inet,inet) (inet x inet)
	amOp(4075, 869, 869, 4, 1206, amBrin) // strat=4 >=(inet,inet) (inet x inet)
	amOp(4075, 869, 869, 5, 1205, amBrin) // strat=5 >(inet,inet) (inet x inet)

	// family=4611 (network_minmax_multi_ops) brin
	amOp(4611, 869, 869, 1, 1203, amBrin) // strat=1 <(inet,inet) (inet x inet)
	amOp(4611, 869, 869, 2, 1204, amBrin) // strat=2 <=(inet,inet) (inet x inet)
	amOp(4611, 869, 869, 3, 1201, amBrin) // strat=3 =(inet,inet) (inet x inet)
	amOp(4611, 869, 869, 4, 1206, amBrin) // strat=4 >=(inet,inet) (inet x inet)
	amOp(4611, 869, 869, 5, 1205, amBrin) // strat=5 >(inet,inet) (inet x inet)

	// family=4585 (network_bloom_ops) brin
	amOp(4585, 869, 869, 1, 1201, amBrin) // strat=1 =(inet,inet) (inet x inet)

	// family=4102 (network_inclusion_ops) brin
	amOp(4102, 869, 869, 3, 3552, amBrin) // strat=3 &&(inet,inet) (inet x inet)
	amOp(4102, 869, 869, 7, 934, amBrin) // strat=7 >>=(inet,inet) (inet x inet)
	amOp(4102, 869, 869, 8, 932, amBrin) // strat=8 <<=(inet,inet) (inet x inet)
	amOp(4102, 869, 869, 18, 1201, amBrin) // strat=18 =(inet,inet) (inet x inet)
	amOp(4102, 869, 869, 24, 933, amBrin) // strat=24 >>(inet,inet) (inet x inet)
	amOp(4102, 869, 869, 26, 931, amBrin) // strat=26 <<(inet,inet) (inet x inet)

	// family=4076 (bpchar_minmax_ops) brin
	amOp(4076, 1042, 1042, 1, 1058, amBrin) // strat=1 <(bpchar,bpchar) (bpchar x bpchar)
	amOp(4076, 1042, 1042, 2, 1059, amBrin) // strat=2 <=(bpchar,bpchar) (bpchar x bpchar)
	amOp(4076, 1042, 1042, 3, 1054, amBrin) // strat=3 =(bpchar,bpchar) (bpchar x bpchar)
	amOp(4076, 1042, 1042, 4, 1061, amBrin) // strat=4 >=(bpchar,bpchar) (bpchar x bpchar)
	amOp(4076, 1042, 1042, 5, 1060, amBrin) // strat=5 >(bpchar,bpchar) (bpchar x bpchar)

	// family=4586 (bpchar_bloom_ops) brin
	amOp(4586, 1042, 1042, 1, 1054, amBrin) // strat=1 =(bpchar,bpchar) (bpchar x bpchar)

	// family=4077 (time_minmax_ops) brin
	amOp(4077, 1083, 1083, 1, 1110, amBrin) // strat=1 <(time,time) (time x time)
	amOp(4077, 1083, 1083, 2, 1111, amBrin) // strat=2 <=(time,time) (time x time)
	amOp(4077, 1083, 1083, 3, 1108, amBrin) // strat=3 =(time,time) (time x time)
	amOp(4077, 1083, 1083, 4, 1113, amBrin) // strat=4 >=(time,time) (time x time)
	amOp(4077, 1083, 1083, 5, 1112, amBrin) // strat=5 >(time,time) (time x time)

	// family=4612 (time_minmax_multi_ops) brin
	amOp(4612, 1083, 1083, 1, 1110, amBrin) // strat=1 <(time,time) (time x time)
	amOp(4612, 1083, 1083, 2, 1111, amBrin) // strat=2 <=(time,time) (time x time)
	amOp(4612, 1083, 1083, 3, 1108, amBrin) // strat=3 =(time,time) (time x time)
	amOp(4612, 1083, 1083, 4, 1113, amBrin) // strat=4 >=(time,time) (time x time)
	amOp(4612, 1083, 1083, 5, 1112, amBrin) // strat=5 >(time,time) (time x time)

	// family=4587 (time_bloom_ops) brin
	amOp(4587, 1083, 1083, 1, 1108, amBrin) // strat=1 =(time,time) (time x time)

	// family=4059 (datetime_minmax_ops) brin
	amOp(4059, 1114, 1114, 1, 2062, amBrin) // strat=1 <(timestamp,timestamp) (timestamp x timestamp)
	amOp(4059, 1114, 1114, 2, 2063, amBrin) // strat=2 <=(timestamp,timestamp) (timestamp x timestamp)
	amOp(4059, 1114, 1114, 3, 2060, amBrin) // strat=3 =(timestamp,timestamp) (timestamp x timestamp)
	amOp(4059, 1114, 1114, 4, 2065, amBrin) // strat=4 >=(timestamp,timestamp) (timestamp x timestamp)
	amOp(4059, 1114, 1114, 5, 2064, amBrin) // strat=5 >(timestamp,timestamp) (timestamp x timestamp)
	amOp(4059, 1114, 1082, 1, 2371, amBrin) // strat=1 <(timestamp,date) (timestamp x date)
	amOp(4059, 1114, 1082, 2, 2372, amBrin) // strat=2 <=(timestamp,date) (timestamp x date)
	amOp(4059, 1114, 1082, 3, 2373, amBrin) // strat=3 =(timestamp,date) (timestamp x date)
	amOp(4059, 1114, 1082, 4, 2374, amBrin) // strat=4 >=(timestamp,date) (timestamp x date)
	amOp(4059, 1114, 1082, 5, 2375, amBrin) // strat=5 >(timestamp,date) (timestamp x date)
	amOp(4059, 1114, 1184, 1, 2534, amBrin) // strat=1 <(timestamp,timestamptz) (timestamp x timestamptz)
	amOp(4059, 1114, 1184, 2, 2535, amBrin) // strat=2 <=(timestamp,timestamptz) (timestamp x timestamptz)
	amOp(4059, 1114, 1184, 3, 2536, amBrin) // strat=3 =(timestamp,timestamptz) (timestamp x timestamptz)
	amOp(4059, 1114, 1184, 4, 2537, amBrin) // strat=4 >=(timestamp,timestamptz) (timestamp x timestamptz)
	amOp(4059, 1114, 1184, 5, 2538, amBrin) // strat=5 >(timestamp,timestamptz) (timestamp x timestamptz)
	amOp(4059, 1082, 1082, 1, 1095, amBrin) // strat=1 <(date,date) (date x date)
	amOp(4059, 1082, 1082, 2, 1096, amBrin) // strat=2 <=(date,date) (date x date)
	amOp(4059, 1082, 1082, 3, 1093, amBrin) // strat=3 =(date,date) (date x date)
	amOp(4059, 1082, 1082, 4, 1098, amBrin) // strat=4 >=(date,date) (date x date)
	amOp(4059, 1082, 1082, 5, 1097, amBrin) // strat=5 >(date,date) (date x date)
	amOp(4059, 1082, 1114, 1, 2345, amBrin) // strat=1 <(date,timestamp) (date x timestamp)
	amOp(4059, 1082, 1114, 2, 2346, amBrin) // strat=2 <=(date,timestamp) (date x timestamp)
	amOp(4059, 1082, 1114, 3, 2347, amBrin) // strat=3 =(date,timestamp) (date x timestamp)
	amOp(4059, 1082, 1114, 4, 2348, amBrin) // strat=4 >=(date,timestamp) (date x timestamp)
	amOp(4059, 1082, 1114, 5, 2349, amBrin) // strat=5 >(date,timestamp) (date x timestamp)
	amOp(4059, 1082, 1184, 1, 2358, amBrin) // strat=1 <(date,timestamptz) (date x timestamptz)
	amOp(4059, 1082, 1184, 2, 2359, amBrin) // strat=2 <=(date,timestamptz) (date x timestamptz)
	amOp(4059, 1082, 1184, 3, 2360, amBrin) // strat=3 =(date,timestamptz) (date x timestamptz)
	amOp(4059, 1082, 1184, 4, 2361, amBrin) // strat=4 >=(date,timestamptz) (date x timestamptz)
	amOp(4059, 1082, 1184, 5, 2362, amBrin) // strat=5 >(date,timestamptz) (date x timestamptz)
	amOp(4059, 1184, 1082, 1, 2384, amBrin) // strat=1 <(timestamptz,date) (timestamptz x date)
	amOp(4059, 1184, 1082, 2, 2385, amBrin) // strat=2 <=(timestamptz,date) (timestamptz x date)
	amOp(4059, 1184, 1082, 3, 2386, amBrin) // strat=3 =(timestamptz,date) (timestamptz x date)
	amOp(4059, 1184, 1082, 4, 2387, amBrin) // strat=4 >=(timestamptz,date) (timestamptz x date)
	amOp(4059, 1184, 1082, 5, 2388, amBrin) // strat=5 >(timestamptz,date) (timestamptz x date)
	amOp(4059, 1184, 1114, 1, 2540, amBrin) // strat=1 <(timestamptz,timestamp) (timestamptz x timestamp)
	amOp(4059, 1184, 1114, 2, 2541, amBrin) // strat=2 <=(timestamptz,timestamp) (timestamptz x timestamp)
	amOp(4059, 1184, 1114, 3, 2542, amBrin) // strat=3 =(timestamptz,timestamp) (timestamptz x timestamp)
	amOp(4059, 1184, 1114, 4, 2543, amBrin) // strat=4 >=(timestamptz,timestamp) (timestamptz x timestamp)
	amOp(4059, 1184, 1114, 5, 2544, amBrin) // strat=5 >(timestamptz,timestamp) (timestamptz x timestamp)
	amOp(4059, 1184, 1184, 1, 1322, amBrin) // strat=1 <(timestamptz,timestamptz) (timestamptz x timestamptz)
	amOp(4059, 1184, 1184, 2, 1323, amBrin) // strat=2 <=(timestamptz,timestamptz) (timestamptz x timestamptz)
	amOp(4059, 1184, 1184, 3, 1320, amBrin) // strat=3 =(timestamptz,timestamptz) (timestamptz x timestamptz)
	amOp(4059, 1184, 1184, 4, 1325, amBrin) // strat=4 >=(timestamptz,timestamptz) (timestamptz x timestamptz)
	amOp(4059, 1184, 1184, 5, 1324, amBrin) // strat=5 >(timestamptz,timestamptz) (timestamptz x timestamptz)

	// family=4605 (datetime_minmax_multi_ops) brin
	amOp(4605, 1114, 1114, 1, 2062, amBrin) // strat=1 <(timestamp,timestamp) (timestamp x timestamp)
	amOp(4605, 1114, 1114, 2, 2063, amBrin) // strat=2 <=(timestamp,timestamp) (timestamp x timestamp)
	amOp(4605, 1114, 1114, 3, 2060, amBrin) // strat=3 =(timestamp,timestamp) (timestamp x timestamp)
	amOp(4605, 1114, 1114, 4, 2065, amBrin) // strat=4 >=(timestamp,timestamp) (timestamp x timestamp)
	amOp(4605, 1114, 1114, 5, 2064, amBrin) // strat=5 >(timestamp,timestamp) (timestamp x timestamp)
	amOp(4605, 1114, 1082, 1, 2371, amBrin) // strat=1 <(timestamp,date) (timestamp x date)
	amOp(4605, 1114, 1082, 2, 2372, amBrin) // strat=2 <=(timestamp,date) (timestamp x date)
	amOp(4605, 1114, 1082, 3, 2373, amBrin) // strat=3 =(timestamp,date) (timestamp x date)
	amOp(4605, 1114, 1082, 4, 2374, amBrin) // strat=4 >=(timestamp,date) (timestamp x date)
	amOp(4605, 1114, 1082, 5, 2375, amBrin) // strat=5 >(timestamp,date) (timestamp x date)
	amOp(4605, 1114, 1184, 1, 2534, amBrin) // strat=1 <(timestamp,timestamptz) (timestamp x timestamptz)
	amOp(4605, 1114, 1184, 2, 2535, amBrin) // strat=2 <=(timestamp,timestamptz) (timestamp x timestamptz)
	amOp(4605, 1114, 1184, 3, 2536, amBrin) // strat=3 =(timestamp,timestamptz) (timestamp x timestamptz)
	amOp(4605, 1114, 1184, 4, 2537, amBrin) // strat=4 >=(timestamp,timestamptz) (timestamp x timestamptz)
	amOp(4605, 1114, 1184, 5, 2538, amBrin) // strat=5 >(timestamp,timestamptz) (timestamp x timestamptz)
	amOp(4605, 1082, 1082, 1, 1095, amBrin) // strat=1 <(date,date) (date x date)
	amOp(4605, 1082, 1082, 2, 1096, amBrin) // strat=2 <=(date,date) (date x date)
	amOp(4605, 1082, 1082, 3, 1093, amBrin) // strat=3 =(date,date) (date x date)
	amOp(4605, 1082, 1082, 4, 1098, amBrin) // strat=4 >=(date,date) (date x date)
	amOp(4605, 1082, 1082, 5, 1097, amBrin) // strat=5 >(date,date) (date x date)
	amOp(4605, 1082, 1114, 1, 2345, amBrin) // strat=1 <(date,timestamp) (date x timestamp)
	amOp(4605, 1082, 1114, 2, 2346, amBrin) // strat=2 <=(date,timestamp) (date x timestamp)
	amOp(4605, 1082, 1114, 3, 2347, amBrin) // strat=3 =(date,timestamp) (date x timestamp)
	amOp(4605, 1082, 1114, 4, 2348, amBrin) // strat=4 >=(date,timestamp) (date x timestamp)
	amOp(4605, 1082, 1114, 5, 2349, amBrin) // strat=5 >(date,timestamp) (date x timestamp)
	amOp(4605, 1082, 1184, 1, 2358, amBrin) // strat=1 <(date,timestamptz) (date x timestamptz)
	amOp(4605, 1082, 1184, 2, 2359, amBrin) // strat=2 <=(date,timestamptz) (date x timestamptz)
	amOp(4605, 1082, 1184, 3, 2360, amBrin) // strat=3 =(date,timestamptz) (date x timestamptz)
	amOp(4605, 1082, 1184, 4, 2361, amBrin) // strat=4 >=(date,timestamptz) (date x timestamptz)
	amOp(4605, 1082, 1184, 5, 2362, amBrin) // strat=5 >(date,timestamptz) (date x timestamptz)
	amOp(4605, 1184, 1082, 1, 2384, amBrin) // strat=1 <(timestamptz,date) (timestamptz x date)
	amOp(4605, 1184, 1082, 2, 2385, amBrin) // strat=2 <=(timestamptz,date) (timestamptz x date)
	amOp(4605, 1184, 1082, 3, 2386, amBrin) // strat=3 =(timestamptz,date) (timestamptz x date)
	amOp(4605, 1184, 1082, 4, 2387, amBrin) // strat=4 >=(timestamptz,date) (timestamptz x date)
	amOp(4605, 1184, 1082, 5, 2388, amBrin) // strat=5 >(timestamptz,date) (timestamptz x date)
	amOp(4605, 1184, 1114, 1, 2540, amBrin) // strat=1 <(timestamptz,timestamp) (timestamptz x timestamp)
	amOp(4605, 1184, 1114, 2, 2541, amBrin) // strat=2 <=(timestamptz,timestamp) (timestamptz x timestamp)
	amOp(4605, 1184, 1114, 3, 2542, amBrin) // strat=3 =(timestamptz,timestamp) (timestamptz x timestamp)
	amOp(4605, 1184, 1114, 4, 2543, amBrin) // strat=4 >=(timestamptz,timestamp) (timestamptz x timestamp)
	amOp(4605, 1184, 1114, 5, 2544, amBrin) // strat=5 >(timestamptz,timestamp) (timestamptz x timestamp)
	amOp(4605, 1184, 1184, 1, 1322, amBrin) // strat=1 <(timestamptz,timestamptz) (timestamptz x timestamptz)
	amOp(4605, 1184, 1184, 2, 1323, amBrin) // strat=2 <=(timestamptz,timestamptz) (timestamptz x timestamptz)
	amOp(4605, 1184, 1184, 3, 1320, amBrin) // strat=3 =(timestamptz,timestamptz) (timestamptz x timestamptz)
	amOp(4605, 1184, 1184, 4, 1325, amBrin) // strat=4 >=(timestamptz,timestamptz) (timestamptz x timestamptz)
	amOp(4605, 1184, 1184, 5, 1324, amBrin) // strat=5 >(timestamptz,timestamptz) (timestamptz x timestamptz)

	// family=4576 (datetime_bloom_ops) brin
	amOp(4576, 1114, 1114, 1, 2060, amBrin) // strat=1 =(timestamp,timestamp) (timestamp x timestamp)
	amOp(4576, 1082, 1082, 1, 1093, amBrin) // strat=1 =(date,date) (date x date)
	amOp(4576, 1184, 1184, 1, 1320, amBrin) // strat=1 =(timestamptz,timestamptz) (timestamptz x timestamptz)

	// family=4078 (interval_minmax_ops) brin
	amOp(4078, 1186, 1186, 1, 1332, amBrin) // strat=1 <(interval,interval) (interval x interval)
	amOp(4078, 1186, 1186, 2, 1333, amBrin) // strat=2 <=(interval,interval) (interval x interval)
	amOp(4078, 1186, 1186, 3, 1330, amBrin) // strat=3 =(interval,interval) (interval x interval)
	amOp(4078, 1186, 1186, 4, 1335, amBrin) // strat=4 >=(interval,interval) (interval x interval)
	amOp(4078, 1186, 1186, 5, 1334, amBrin) // strat=5 >(interval,interval) (interval x interval)

	// family=4613 (interval_minmax_multi_ops) brin
	amOp(4613, 1186, 1186, 1, 1332, amBrin) // strat=1 <(interval,interval) (interval x interval)
	amOp(4613, 1186, 1186, 2, 1333, amBrin) // strat=2 <=(interval,interval) (interval x interval)
	amOp(4613, 1186, 1186, 3, 1330, amBrin) // strat=3 =(interval,interval) (interval x interval)
	amOp(4613, 1186, 1186, 4, 1335, amBrin) // strat=4 >=(interval,interval) (interval x interval)
	amOp(4613, 1186, 1186, 5, 1334, amBrin) // strat=5 >(interval,interval) (interval x interval)

	// family=4588 (interval_bloom_ops) brin
	amOp(4588, 1186, 1186, 1, 1330, amBrin) // strat=1 =(interval,interval) (interval x interval)

	// family=4058 (timetz_minmax_ops) brin
	amOp(4058, 1266, 1266, 1, 1552, amBrin) // strat=1 <(timetz,timetz) (timetz x timetz)
	amOp(4058, 1266, 1266, 2, 1553, amBrin) // strat=2 <=(timetz,timetz) (timetz x timetz)
	amOp(4058, 1266, 1266, 3, 1550, amBrin) // strat=3 =(timetz,timetz) (timetz x timetz)
	amOp(4058, 1266, 1266, 4, 1555, amBrin) // strat=4 >=(timetz,timetz) (timetz x timetz)
	amOp(4058, 1266, 1266, 5, 1554, amBrin) // strat=5 >(timetz,timetz) (timetz x timetz)

	// family=4604 (timetz_minmax_multi_ops) brin
	amOp(4604, 1266, 1266, 1, 1552, amBrin) // strat=1 <(timetz,timetz) (timetz x timetz)
	amOp(4604, 1266, 1266, 2, 1553, amBrin) // strat=2 <=(timetz,timetz) (timetz x timetz)
	amOp(4604, 1266, 1266, 3, 1550, amBrin) // strat=3 =(timetz,timetz) (timetz x timetz)
	amOp(4604, 1266, 1266, 4, 1555, amBrin) // strat=4 >=(timetz,timetz) (timetz x timetz)
	amOp(4604, 1266, 1266, 5, 1554, amBrin) // strat=5 >(timetz,timetz) (timetz x timetz)

	// family=4575 (timetz_bloom_ops) brin
	amOp(4575, 1266, 1266, 1, 1550, amBrin) // strat=1 =(timetz,timetz) (timetz x timetz)

	// family=4079 (bit_minmax_ops) brin
	amOp(4079, 1560, 1560, 1, 1786, amBrin) // strat=1 <(bit,bit) (bit x bit)
	amOp(4079, 1560, 1560, 2, 1788, amBrin) // strat=2 <=(bit,bit) (bit x bit)
	amOp(4079, 1560, 1560, 3, 1784, amBrin) // strat=3 =(bit,bit) (bit x bit)
	amOp(4079, 1560, 1560, 4, 1789, amBrin) // strat=4 >=(bit,bit) (bit x bit)
	amOp(4079, 1560, 1560, 5, 1787, amBrin) // strat=5 >(bit,bit) (bit x bit)

	// family=4080 (varbit_minmax_ops) brin
	amOp(4080, 1562, 1562, 1, 1806, amBrin) // strat=1 <(varbit,varbit) (varbit x varbit)
	amOp(4080, 1562, 1562, 2, 1808, amBrin) // strat=2 <=(varbit,varbit) (varbit x varbit)
	amOp(4080, 1562, 1562, 3, 1804, amBrin) // strat=3 =(varbit,varbit) (varbit x varbit)
	amOp(4080, 1562, 1562, 4, 1809, amBrin) // strat=4 >=(varbit,varbit) (varbit x varbit)
	amOp(4080, 1562, 1562, 5, 1807, amBrin) // strat=5 >(varbit,varbit) (varbit x varbit)

	// family=4055 (numeric_minmax_ops) brin
	amOp(4055, 1700, 1700, 1, 1754, amBrin) // strat=1 <(numeric,numeric) (numeric x numeric)
	amOp(4055, 1700, 1700, 2, 1755, amBrin) // strat=2 <=(numeric,numeric) (numeric x numeric)
	amOp(4055, 1700, 1700, 3, 1752, amBrin) // strat=3 =(numeric,numeric) (numeric x numeric)
	amOp(4055, 1700, 1700, 4, 1757, amBrin) // strat=4 >=(numeric,numeric) (numeric x numeric)
	amOp(4055, 1700, 1700, 5, 1756, amBrin) // strat=5 >(numeric,numeric) (numeric x numeric)

	// family=4603 (numeric_minmax_multi_ops) brin
	amOp(4603, 1700, 1700, 1, 1754, amBrin) // strat=1 <(numeric,numeric) (numeric x numeric)
	amOp(4603, 1700, 1700, 2, 1755, amBrin) // strat=2 <=(numeric,numeric) (numeric x numeric)
	amOp(4603, 1700, 1700, 3, 1752, amBrin) // strat=3 =(numeric,numeric) (numeric x numeric)
	amOp(4603, 1700, 1700, 4, 1757, amBrin) // strat=4 >=(numeric,numeric) (numeric x numeric)
	amOp(4603, 1700, 1700, 5, 1756, amBrin) // strat=5 >(numeric,numeric) (numeric x numeric)

	// family=4574 (numeric_bloom_ops) brin
	amOp(4574, 1700, 1700, 1, 1752, amBrin) // strat=1 =(numeric,numeric) (numeric x numeric)

	// family=4081 (uuid_minmax_ops) brin
	amOp(4081, 2950, 2950, 1, 2974, amBrin) // strat=1 <(uuid,uuid) (uuid x uuid)
	amOp(4081, 2950, 2950, 2, 2976, amBrin) // strat=2 <=(uuid,uuid) (uuid x uuid)
	amOp(4081, 2950, 2950, 3, 2972, amBrin) // strat=3 =(uuid,uuid) (uuid x uuid)
	amOp(4081, 2950, 2950, 4, 2977, amBrin) // strat=4 >=(uuid,uuid) (uuid x uuid)
	amOp(4081, 2950, 2950, 5, 2975, amBrin) // strat=5 >(uuid,uuid) (uuid x uuid)

	// family=4614 (uuid_minmax_multi_ops) brin
	amOp(4614, 2950, 2950, 1, 2974, amBrin) // strat=1 <(uuid,uuid) (uuid x uuid)
	amOp(4614, 2950, 2950, 2, 2976, amBrin) // strat=2 <=(uuid,uuid) (uuid x uuid)
	amOp(4614, 2950, 2950, 3, 2972, amBrin) // strat=3 =(uuid,uuid) (uuid x uuid)
	amOp(4614, 2950, 2950, 4, 2977, amBrin) // strat=4 >=(uuid,uuid) (uuid x uuid)
	amOp(4614, 2950, 2950, 5, 2975, amBrin) // strat=5 >(uuid,uuid) (uuid x uuid)

	// family=4589 (uuid_bloom_ops) brin
	amOp(4589, 2950, 2950, 1, 2972, amBrin) // strat=1 =(uuid,uuid) (uuid x uuid)

	// family=4103 (range_inclusion_ops) brin
	amOp(4103, 3831, 3831, 1, 3893, amBrin) // strat=1 <<(anyrange,anyrange) (anyrange x anyrange)
	amOp(4103, 3831, 3831, 2, 3895, amBrin) // strat=2 &<(anyrange,anyrange) (anyrange x anyrange)
	amOp(4103, 3831, 3831, 3, 3888, amBrin) // strat=3 &&(anyrange,anyrange) (anyrange x anyrange)
	amOp(4103, 3831, 3831, 4, 3896, amBrin) // strat=4 &>(anyrange,anyrange) (anyrange x anyrange)
	amOp(4103, 3831, 3831, 5, 3894, amBrin) // strat=5 >>(anyrange,anyrange) (anyrange x anyrange)
	amOp(4103, 3831, 3831, 7, 3890, amBrin) // strat=7 @>(anyrange,anyrange) (anyrange x anyrange)
	amOp(4103, 3831, 3831, 8, 3892, amBrin) // strat=8 <@(anyrange,anyrange) (anyrange x anyrange)
	amOp(4103, 3831, 2283, 16, 3889, amBrin) // strat=16 @>(anyrange,anyelement) (anyrange x anyelement)
	amOp(4103, 3831, 3831, 17, 3897, amBrin) // strat=17 -|-(anyrange,anyrange) (anyrange x anyrange)
	amOp(4103, 3831, 3831, 18, 3882, amBrin) // strat=18 =(anyrange,anyrange) (anyrange x anyrange)
	amOp(4103, 3831, 3831, 20, 3884, amBrin) // strat=20 <(anyrange,anyrange) (anyrange x anyrange)
	amOp(4103, 3831, 3831, 21, 3885, amBrin) // strat=21 <=(anyrange,anyrange) (anyrange x anyrange)
	amOp(4103, 3831, 3831, 22, 3887, amBrin) // strat=22 >(anyrange,anyrange) (anyrange x anyrange)
	amOp(4103, 3831, 3831, 23, 3886, amBrin) // strat=23 >=(anyrange,anyrange) (anyrange x anyrange)

	// family=4082 (pg_lsn_minmax_ops) brin
	amOp(4082, 3220, 3220, 1, 3224, amBrin) // strat=1 <(pg_lsn,pg_lsn) (pg_lsn x pg_lsn)
	amOp(4082, 3220, 3220, 2, 3226, amBrin) // strat=2 <=(pg_lsn,pg_lsn) (pg_lsn x pg_lsn)
	amOp(4082, 3220, 3220, 3, 3222, amBrin) // strat=3 =(pg_lsn,pg_lsn) (pg_lsn x pg_lsn)
	amOp(4082, 3220, 3220, 4, 3227, amBrin) // strat=4 >=(pg_lsn,pg_lsn) (pg_lsn x pg_lsn)
	amOp(4082, 3220, 3220, 5, 3225, amBrin) // strat=5 >(pg_lsn,pg_lsn) (pg_lsn x pg_lsn)

	// family=4615 (pg_lsn_minmax_multi_ops) brin
	amOp(4615, 3220, 3220, 1, 3224, amBrin) // strat=1 <(pg_lsn,pg_lsn) (pg_lsn x pg_lsn)
	amOp(4615, 3220, 3220, 2, 3226, amBrin) // strat=2 <=(pg_lsn,pg_lsn) (pg_lsn x pg_lsn)
	amOp(4615, 3220, 3220, 3, 3222, amBrin) // strat=3 =(pg_lsn,pg_lsn) (pg_lsn x pg_lsn)
	amOp(4615, 3220, 3220, 4, 3227, amBrin) // strat=4 >=(pg_lsn,pg_lsn) (pg_lsn x pg_lsn)
	amOp(4615, 3220, 3220, 5, 3225, amBrin) // strat=5 >(pg_lsn,pg_lsn) (pg_lsn x pg_lsn)

	// family=4590 (pg_lsn_bloom_ops) brin
	amOp(4590, 3220, 3220, 1, 3222, amBrin) // strat=1 =(pg_lsn,pg_lsn) (pg_lsn x pg_lsn)

	// family=4104 (box_inclusion_ops) brin
	amOp(4104, 603, 603, 1, 493, amBrin) // strat=1 <<(box,box) (box x box)
	amOp(4104, 603, 603, 2, 494, amBrin) // strat=2 &<(box,box) (box x box)
	amOp(4104, 603, 603, 3, 500, amBrin) // strat=3 &&(box,box) (box x box)
	amOp(4104, 603, 603, 4, 495, amBrin) // strat=4 &>(box,box) (box x box)
	amOp(4104, 603, 603, 5, 496, amBrin) // strat=5 >>(box,box) (box x box)
	amOp(4104, 603, 603, 6, 499, amBrin) // strat=6 ~=(box,box) (box x box)
	amOp(4104, 603, 603, 7, 498, amBrin) // strat=7 @>(box,box) (box x box)
	amOp(4104, 603, 603, 8, 497, amBrin) // strat=8 <@(box,box) (box x box)
	amOp(4104, 603, 603, 9, 2571, amBrin) // strat=9 &<|(box,box) (box x box)
	amOp(4104, 603, 603, 10, 2570, amBrin) // strat=10 <<|(box,box) (box x box)
	amOp(4104, 603, 603, 11, 2573, amBrin) // strat=11 |>>(box,box) (box x box)
	amOp(4104, 603, 603, 12, 2572, amBrin) // strat=12 |&>(box,box) (box x box)
	amOp(4104, 603, 600, 7, 433, amBrin) // strat=7 @>(box,point) (box x point)

	// Total non-btree amOp calls: 660
	// Grand total: 945 rows (= 945 pg_amop entries)
