package executor

import (
	"testing"
	"time"

	"github.com/goopg/goopg/internal/utils/misc"
)

// dchOracleFormats and dchOracleCases were captured from PostgreSQL 18.3: each
// case holds to_char of one timestamp with every format, and its text under each
// DateStyle. Years are astronomical (1 BC is year 0).
var dchOracleFormats = []string{
	"YYYY YYY YY Y Y,YYY IYYY IYY IY I",
	"MM MON Mon mon MONTH Month month|",
	"DD DDD D ID IDDD WW W IW Q CC J",
	"HH HH12 HH24 MI SS SSSS SSSSS MS US FF1 FF2 FF3 FF4 FF5 FF6",
	"AM PM am pm A.M. P.M. a.m. p.m. AD BC ad bc A.D. B.C. a.d. b.c.",
	"DAY Day day DY Dy dy|RM rm|",
	"FMMonth FMDay FMDD FMMM FMHH24 FMYYYY FMRM FMCC FMDDD",
	"DDth DDTH MMth HH24TH YYYYth Qth Jth Dth",
	"TMMonth TMMON TMmon TMDay TMDY TMdy|",
	"TZ tz OF TZH TZM FX SP \"quoted YYYY\" \\\"lit\\\" x%y_z",
	"yyyy mm dd hh24 mi ss ms us iw iyyy cc q j w ww ddd d id rm",
}

var dchOracleCases = []struct {
	y, mo, d, h, mi, s, us int
	toChar                 []string
	styles                 [][4]string // style, order, timestamp text, date text
}{
	{-4, 2, 29, 13, 4, 5, 500000,
		[]string{
			"0005 005 05 5 0,005 0005 005 05 5",
			"02 FEB Feb feb FEBRUARY  February  february |",
			"29 060 5 4 060 09 5 09 1 -01 1719658",
			"01 01 13 04 05 47045 47045 500 500000 5 50 500 5000 50000 500000",
			"PM PM pm pm P.M. P.M. p.m. p.m. BC BC bc bc B.C. B.C. b.c. b.c.",
			"THURSDAY  Thursday  thursday  THU Thu thu|II   ii  |",
			"February Thursday 29 2 13 5 II -1 60",
			"29th 29TH 02nd 13TH 0005th 1st 1719658th 5th",
			"February FEB feb Thursday THU thu|",
			"  +00 +00 00  SP quoted YYYY \"l5t\" x%5_z",
			"0005 02 29 13 04 05 500 500000 09 0005 -01 1 1719658 5 09 060 5 4 ii  ",
		},
		[][4]string{
			{"ISO", "YMD", "0005-02-29 13:04:05.5 BC", "0005-02-29 BC"},
			{"SQL", "MDY", "02/29/0005 13:04:05.5 BC", "02/29/0005 BC"},
			{"SQL", "DMY", "29/02/0005 13:04:05.5 BC", "29/02/0005 BC"},
			{"Postgres", "MDY", "Thu Feb 29 13:04:05.5 0005 BC", "02-29-0005 BC"},
			{"Postgres", "DMY", "Thu 29 Feb 13:04:05.5 0005 BC", "29-02-0005 BC"},
			{"German", "DMY", "29.02.0005 13:04:05.5 BC", "29.02.0005 BC"},
		},
	},
	{-43, 3, 15, 0, 0, 0, 0,
		[]string{
			"0044 044 44 4 0,044 0044 044 44 4",
			"03 MAR Mar mar MARCH     March     march    |",
			"15 074 6 5 075 11 3 11 1 -01 1705428",
			"12 12 00 00 00 0 0 000 000000 0 00 000 0000 00000 000000",
			"AM AM am am A.M. A.M. a.m. a.m. BC BC bc bc B.C. B.C. b.c. b.c.",
			"FRIDAY    Friday    friday    FRI Fri fri|III  iii |",
			"March Friday 15 3 0 44 III -1 74",
			"15th 15TH 03rd 00TH 0044th 1st 1705428th 6th",
			"March MAR mar Friday FRI fri|",
			"  +00 +00 00  SP quoted YYYY \"l4t\" x%4_z",
			"0044 03 15 00 00 00 000 000000 11 0044 -01 1 1705428 3 11 074 6 5 iii ",
		},
		[][4]string{
			{"ISO", "YMD", "0044-03-15 00:00:00 BC", "0044-03-15 BC"},
			{"SQL", "MDY", "03/15/0044 00:00:00 BC", "03/15/0044 BC"},
			{"SQL", "DMY", "15/03/0044 00:00:00 BC", "15/03/0044 BC"},
			{"Postgres", "MDY", "Fri Mar 15 00:00:00 0044 BC", "03-15-0044 BC"},
			{"Postgres", "DMY", "Fri 15 Mar 00:00:00 0044 BC", "15-03-0044 BC"},
			{"German", "DMY", "15.03.0044 00:00:00 BC", "15.03.0044 BC"},
		},
	},
	{0, 12, 31, 23, 59, 59, 0,
		[]string{
			"0001 001 01 1 0,001 0001 001 01 1",
			"12 DEC Dec dec DECEMBER  December  december |",
			"31 366 1 7 364 53 5 52 4 -01 1721425",
			"11 11 23 59 59 86399 86399 000 000000 0 00 000 0000 00000 000000",
			"PM PM pm pm P.M. P.M. p.m. p.m. BC BC bc bc B.C. B.C. b.c. b.c.",
			"SUNDAY    Sunday    sunday    SUN Sun sun|XII  xii |",
			"December Sunday 31 12 23 1 XII -1 366",
			"31st 31ST 12th 23RD 0001st 4th 1721425th 1st",
			"December DEC dec Sunday SUN sun|",
			"  +00 +00 00  SP quoted YYYY \"l1t\" x%1_z",
			"0001 12 31 23 59 59 000 000000 52 0001 -01 4 1721425 5 53 366 1 7 xii ",
		},
		[][4]string{
			{"ISO", "YMD", "0001-12-31 23:59:59 BC", "0001-12-31 BC"},
			{"SQL", "MDY", "12/31/0001 23:59:59 BC", "12/31/0001 BC"},
			{"SQL", "DMY", "31/12/0001 23:59:59 BC", "31/12/0001 BC"},
			{"Postgres", "MDY", "Sun Dec 31 23:59:59 0001 BC", "12-31-0001 BC"},
			{"Postgres", "DMY", "Sun 31 Dec 23:59:59 0001 BC", "31-12-0001 BC"},
			{"German", "DMY", "31.12.0001 23:59:59 BC", "31.12.0001 BC"},
		},
	},
	{-4712, 11, 24, 0, 0, 0, 0,
		[]string{
			"4713 713 13 3 4,713 4713 713 13 3",
			"11 NOV Nov nov NOVEMBER  November  november |",
			"24 329 4 3 332 47 4 48 4 -48 366",
			"12 12 00 00 00 0 0 000 000000 0 00 000 0000 00000 000000",
			"AM AM am am A.M. A.M. a.m. a.m. BC BC bc bc B.C. B.C. b.c. b.c.",
			"WEDNESDAY Wednesday wednesday WED Wed wed|XI   xi  |",
			"November Wednesday 24 11 0 4713 XI -48 329",
			"24th 24TH 11th 00TH 4713th 4th 366th 4th",
			"November NOV nov Wednesday WED wed|",
			"  +00 +00 00  SP quoted YYYY \"l3t\" x%3_z",
			"4713 11 24 00 00 00 000 000000 48 4713 -48 4 366 4 47 329 4 3 xi  ",
		},
		[][4]string{
			{"ISO", "YMD", "4713-11-24 00:00:00 BC", "4713-11-24 BC"},
			{"SQL", "MDY", "11/24/4713 00:00:00 BC", "11/24/4713 BC"},
			{"SQL", "DMY", "24/11/4713 00:00:00 BC", "24/11/4713 BC"},
			{"Postgres", "MDY", "Wed Nov 24 00:00:00 4713 BC", "11-24-4713 BC"},
			{"Postgres", "DMY", "Wed 24 Nov 00:00:00 4713 BC", "24-11-4713 BC"},
			{"German", "DMY", "24.11.4713 00:00:00 BC", "24.11.4713 BC"},
		},
	},
	{12345, 6, 7, 8, 9, 10, 123456,
		[]string{
			"12345 345 45 5 12,345 12345 345 45 5",
			"06 JUN Jun jun JUNE      June      june     |",
			"07 158 5 4 158 23 1 23 2 124 6230136",
			"08 08 08 09 10 29350 29350 123 123456 1 12 123 1234 12345 123456",
			"AM AM am am A.M. A.M. a.m. a.m. AD AD ad ad A.D. A.D. a.d. a.d.",
			"THURSDAY  Thursday  thursday  THU Thu thu|VI   vi  |",
			"June Thursday 7 6 8 12345 VI 124 158",
			"07th 07TH 06th 08TH 12345th 2nd 6230136th 5th",
			"June JUN jun Thursday THU thu|",
			"  +00 +00 00  SP quoted YYYY \"l5t\" x%5_z",
			"12345 06 07 08 09 10 123 123456 23 12345 124 2 6230136 1 23 158 5 4 vi  ",
		},
		[][4]string{
			{"ISO", "YMD", "12345-06-07 08:09:10.123456", "12345-06-07"},
			{"SQL", "MDY", "06/07/12345 08:09:10.123456", "06/07/12345"},
			{"SQL", "DMY", "07/06/12345 08:09:10.123456", "07/06/12345"},
			{"Postgres", "MDY", "Thu Jun 07 08:09:10.123456 12345", "06-07-12345"},
			{"Postgres", "DMY", "Thu 07 Jun 08:09:10.123456 12345", "07-06-12345"},
			{"German", "DMY", "07.06.12345 08:09:10.123456", "07.06.12345"},
		},
	},
	{2020, 2, 29, 13, 4, 5, 678901,
		[]string{
			"2020 020 20 0 2,020 2020 020 20 0",
			"02 FEB Feb feb FEBRUARY  February  february |",
			"29 060 7 6 062 09 5 09 1 21 2458909",
			"01 01 13 04 05 47045 47045 678 678901 6 67 678 6789 67890 678901",
			"PM PM pm pm P.M. P.M. p.m. p.m. AD AD ad ad A.D. A.D. a.d. a.d.",
			"SATURDAY  Saturday  saturday  SAT Sat sat|II   ii  |",
			"February Saturday 29 2 13 2020 II 21 60",
			"29th 29TH 02nd 13TH 2020th 1st 2458909th 7th",
			"February FEB feb Saturday SAT sat|",
			"  +00 +00 00  SP quoted YYYY \"l0t\" x%0_z",
			"2020 02 29 13 04 05 678 678901 09 2020 21 1 2458909 5 09 060 7 6 ii  ",
		},
		[][4]string{
			{"ISO", "YMD", "2020-02-29 13:04:05.678901", "2020-02-29"},
			{"SQL", "MDY", "02/29/2020 13:04:05.678901", "02/29/2020"},
			{"SQL", "DMY", "29/02/2020 13:04:05.678901", "29/02/2020"},
			{"Postgres", "MDY", "Sat Feb 29 13:04:05.678901 2020", "02-29-2020"},
			{"Postgres", "DMY", "Sat 29 Feb 13:04:05.678901 2020", "29-02-2020"},
			{"German", "DMY", "29.02.2020 13:04:05.678901", "29.02.2020"},
		},
	},
	{1999, 12, 31, 23, 0, 0, 0,
		[]string{
			"1999 999 99 9 1,999 1999 999 99 9",
			"12 DEC Dec dec DECEMBER  December  december |",
			"31 365 6 5 362 53 5 52 4 20 2451544",
			"11 11 23 00 00 82800 82800 000 000000 0 00 000 0000 00000 000000",
			"PM PM pm pm P.M. P.M. p.m. p.m. AD AD ad ad A.D. A.D. a.d. a.d.",
			"FRIDAY    Friday    friday    FRI Fri fri|XII  xii |",
			"December Friday 31 12 23 1999 XII 20 365",
			"31st 31ST 12th 23RD 1999th 4th 2451544th 6th",
			"December DEC dec Friday FRI fri|",
			"  +00 +00 00  SP quoted YYYY \"l9t\" x%9_z",
			"1999 12 31 23 00 00 000 000000 52 1999 20 4 2451544 5 53 365 6 5 xii ",
		},
		[][4]string{
			{"ISO", "YMD", "1999-12-31 23:00:00", "1999-12-31"},
			{"SQL", "MDY", "12/31/1999 23:00:00", "12/31/1999"},
			{"SQL", "DMY", "31/12/1999 23:00:00", "31/12/1999"},
			{"Postgres", "MDY", "Fri Dec 31 23:00:00 1999", "12-31-1999"},
			{"Postgres", "DMY", "Fri 31 Dec 23:00:00 1999", "31-12-1999"},
			{"German", "DMY", "31.12.1999 23:00:00", "31.12.1999"},
		},
	},
	{99, 1, 1, 0, 0, 0, 0,
		[]string{
			"0099 099 99 9 0,099 0099 099 99 9",
			"01 JAN Jan jan JANUARY   January   january  |",
			"01 001 5 4 004 01 1 01 1 01 1757220",
			"12 12 00 00 00 0 0 000 000000 0 00 000 0000 00000 000000",
			"AM AM am am A.M. A.M. a.m. a.m. AD AD ad ad A.D. A.D. a.d. a.d.",
			"THURSDAY  Thursday  thursday  THU Thu thu|I    i   |",
			"January Thursday 1 1 0 99 I 1 1",
			"01st 01ST 01st 00TH 0099th 1st 1757220th 5th",
			"January JAN jan Thursday THU thu|",
			"  +00 +00 00  SP quoted YYYY \"l9t\" x%9_z",
			"0099 01 01 00 00 00 000 000000 01 0099 01 1 1757220 1 01 001 5 4 i   ",
		},
		[][4]string{
			{"ISO", "YMD", "0099-01-01 00:00:00", "0099-01-01"},
			{"SQL", "MDY", "01/01/0099 00:00:00", "01/01/0099"},
			{"SQL", "DMY", "01/01/0099 00:00:00", "01/01/0099"},
			{"Postgres", "MDY", "Thu Jan 01 00:00:00 0099", "01-01-0099"},
			{"Postgres", "DMY", "Thu 01 Jan 00:00:00 0099", "01-01-0099"},
			{"German", "DMY", "01.01.0099 00:00:00", "01.01.0099"},
		},
	},
	{2021, 1, 3, 12, 0, 0, 0,
		[]string{
			"2021 021 21 1 2,021 2020 020 20 0",
			"01 JAN Jan jan JANUARY   January   january  |",
			"03 003 1 7 371 01 1 53 1 21 2459218",
			"12 12 12 00 00 43200 43200 000 000000 0 00 000 0000 00000 000000",
			"PM PM pm pm P.M. P.M. p.m. p.m. AD AD ad ad A.D. A.D. a.d. a.d.",
			"SUNDAY    Sunday    sunday    SUN Sun sun|I    i   |",
			"January Sunday 3 1 12 2021 I 21 3",
			"03rd 03RD 01st 12TH 2021st 1st 2459218th 1st",
			"January JAN jan Sunday SUN sun|",
			"  +00 +00 00  SP quoted YYYY \"l0t\" x%1_z",
			"2021 01 03 12 00 00 000 000000 53 2020 21 1 2459218 1 01 003 1 7 i   ",
		},
		[][4]string{
			{"ISO", "YMD", "2021-01-03 12:00:00", "2021-01-03"},
			{"SQL", "MDY", "01/03/2021 12:00:00", "01/03/2021"},
			{"SQL", "DMY", "03/01/2021 12:00:00", "03/01/2021"},
			{"Postgres", "MDY", "Sun Jan 03 12:00:00 2021", "01-03-2021"},
			{"Postgres", "DMY", "Sun 03 Jan 12:00:00 2021", "03-01-2021"},
			{"German", "DMY", "03.01.2021 12:00:00", "03.01.2021"},
		},
	},
}

// TestToCharMatchesPostgres compares every template pattern with PostgreSQL's
// output, for BC dates, BC leap days, five-digit years and ordinary dates.
func TestToCharMatchesPostgres(t *testing.T) {
	for _, tc := range dchOracleCases {
		ts := time.Date(tc.y, time.Month(tc.mo), tc.d, tc.h, tc.mi, tc.s, tc.us*1000, time.UTC)
		for i, format := range dchOracleFormats {
			if got, err := formatDCH(format, newDCHValue(ts, "", 0)); err != nil || got != tc.toChar[i] {
				t.Errorf("%v %q:\n got %q\nwant %q", ts, format, got, tc.toChar[i])
			}
		}
	}
}

// TestDateStylesMatchPostgresAcrossEras: the DateStyle formatters print BC years,
// including BC leap days, and long years as PostgreSQL does.
func TestDateStylesMatchPostgresAcrossEras(t *testing.T) {
	for _, tc := range dchOracleCases {
		ts := time.Date(tc.y, time.Month(tc.mo), tc.d, tc.h, tc.mi, tc.s, tc.us*1000, time.UTC)
		for _, st := range tc.styles {
			if got := misc.FormatTimestamp(ts, st[0], st[1]); got != st[2] {
				t.Errorf("timestamp %v %s,%s = %q, want %q", ts, st[0], st[1], got, st[2])
			}
			if got := misc.FormatDate(ts, st[0], st[1]); got != st[3] {
				t.Errorf("date %v %s,%s = %q, want %q", ts, st[0], st[1], got, st[3])
			}
		}
	}
}

// TestBCDatumText: the type-agnostic renderers print an era, not a negative year.
func TestBCDatumText(t *testing.T) {
	ts := time.Date(-43, 3, 15, 1, 2, 3, 500000000, time.UTC)
	if got, want := NewTimeDatum(ts).Format(), "0044-03-15 01:02:03.500000 BC"; got != want {
		t.Errorf("Format = %q, want %q", got, want)
	}
	if got, want := string(NewTimeDatum(ts).AppendValueText(nil)), "0044-03-15 01:02:03.500000 BC"; got != want {
		t.Errorf("AppendValueText = %q, want %q", got, want)
	}
	date := NewDateDatum(time.Date(-43, 3, 15, 0, 0, 0, 0, time.UTC))
	if got, want := date.Format(), "03-15-0044 BC"; got != want {
		t.Errorf("date Format = %q, want %q", got, want)
	}
}

// TestToCharDateUsesSessionZone: to_char(date, ...) resolves to the timestamptz
// form, so its zone fields describe midnight in the session zone.
func TestToCharDateUsesSessionZone(t *testing.T) {
	d := NewDateDatum(time.Date(2020, 7, 5, 0, 0, 0, 0, time.UTC))
	ctx := &Context{GetSetting: func(name string) (string, bool) {
		if name == "timezone" {
			return "America/New_York", true
		}
		return "", false
	}}
	got, _ := toCharDatetime(d, "YYYY-MM-DD HH24 TZ OF", ctx)
	if want := "2020-07-05 00 EDT -04"; got.StringValue() != want {
		t.Errorf("to_char(date) = %q, want %q", got.StringValue(), want)
	}
}
