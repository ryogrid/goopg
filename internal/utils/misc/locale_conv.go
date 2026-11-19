package misc

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// LocaleConv holds the localeconv fields to_char reads (PGLC_localeconv in
// pg_locale.c). A field is empty when the locale leaves it unset.
type LocaleConv struct {
	DecimalPoint   string
	ThousandsSep   string
	NegativeSign   string
	PositiveSign   string
	CurrencySymbol string
}

// glibc locale categories, as numbered in <locale.h>.
const (
	lcNumeric  = 1
	lcMonetary = 4
)

// String positions inside a category's compiled data (the _NL_ITEM order).
const (
	nlDecimalPoint   = 0
	nlThousandsSep   = 1
	nlCurrencySymbol = 1
	nlPositiveSign   = 5
	nlNegativeSign   = 6
)

// localeFileMagic is LIMAGIC for the categories read here.
func localeFileMagic(category int) uint32 { return 0x20031115 ^ uint32(category) }

// localeFileStrings returns the strings at the given positions of one compiled
// locale category: a header {magic, count}, count offsets, then the data.
func localeFileStrings(data []byte, category int, items ...int) ([]string, bool) {
	if len(data) < 8 {
		return nil, false
	}
	le := binary.LittleEndian
	count := int(le.Uint32(data[4:8]))
	if le.Uint32(data[:4]) != localeFileMagic(category) || count < 0 || 8+count*4 > len(data) {
		return nil, false
	}
	out := make([]string, len(items))
	for i, item := range items {
		if item >= count {
			return nil, false
		}
		off := int(le.Uint32(data[8+item*4:]))
		if off > len(data) {
			return nil, false
		}
		end := off
		for end < len(data) && data[end] != 0 {
			end++
		}
		out[i] = string(data[off:end])
	}
	return out, true
}

// localeCategoryFileName is the file holding a category in a compiled locale.
func localeCategoryFileName(category int) string {
	if category == lcNumeric {
		return "LC_NUMERIC"
	}
	return "LC_MONETARY"
}

// localeArchiveCategory finds a category's bytes in glibc's locale-archive: each
// namehash entry points at a locrec of {refs, then (offset, length) per category}.
func localeArchiveCategory(name string, category int) []byte {
	data, err := os.ReadFile(filepath.Join(localeDir, "locale-archive"))
	if err != nil || len(data) < 32 {
		return nil
	}
	le := binary.LittleEndian
	hashOff := int(le.Uint32(data[8:12]))
	hashSize := int(le.Uint32(data[16:20]))
	for i := 0; i < hashSize; i++ {
		e := hashOff + i*12
		if e+12 > len(data) {
			break
		}
		nameOff, recOff := int(le.Uint32(data[e+4:])), int(le.Uint32(data[e+8:]))
		if nameOff == 0 || nameOff >= len(data) {
			continue
		}
		end := nameOff
		for end < len(data) && data[end] != 0 {
			end++
		}
		if string(data[nameOff:end]) != name {
			continue
		}
		field := recOff + 4 + category*8
		if field+8 > len(data) {
			return nil
		}
		off, size := int(le.Uint32(data[field:])), int(le.Uint32(data[field+4:]))
		if off+size > len(data) {
			return nil
		}
		return data[off : off+size]
	}
	return nil
}

// localeCategoryData loads a category as setlocale would find it.
func localeCategoryData(name string, category int) []byte {
	file := localeCategoryFileName(category)
	for _, candidate := range []string{name, normalizeLocaleName(name)} {
		if candidate == "" || strings.Contains(candidate, "/") {
			continue
		}
		if lp := os.Getenv("LOCPATH"); lp != "" {
			for _, dir := range filepath.SplitList(lp) {
				if data, err := os.ReadFile(filepath.Join(dir, candidate, file)); err == nil {
					return data
				}
			}
			continue
		}
		if data, err := os.ReadFile(filepath.Join(localeDir, candidate, file)); err == nil {
			return data
		}
		if data := localeArchiveCategory(candidate, category); data != nil {
			return data
		}
	}
	return nil
}

// resolveLocaleSetting applies setlocale's rule for an empty name: the category's
// environment variables, then C.
func resolveLocaleSetting(name, categoryEnv string) string {
	if name != "" {
		return name
	}
	for _, key := range []string{"LC_ALL", categoryEnv, "LANG"} {
		if v := os.Getenv(key); v != "" {
			return v
		}
	}
	return "C"
}

var localeStringCache sync.Map // "category\x00name" -> []string

func localeStrings(name string, category int, items ...int) []string {
	key := localeCategoryFileName(category) + "\x00" + name
	if v, ok := localeStringCache.Load(key); ok {
		return v.([]string)
	}
	out := make([]string, len(items))
	if name != "C" && name != "POSIX" {
		if data := localeCategoryData(name, category); data != nil {
			if got, ok := localeFileStrings(data, category, items...); ok {
				out = got
			}
		}
	}
	localeStringCache.Store(key, out)
	return out
}

// LocaleConvFor is localeconv under the lc_numeric and lc_monetary settings. A
// locale whose data cannot be read, such as glibc's built-in C.UTF-8 on a host
// without its files, has the C locale's empty fields.
func LocaleConvFor(numeric, monetary string) LocaleConv {
	n := localeStrings(resolveLocaleSetting(numeric, "LC_NUMERIC"), lcNumeric, nlDecimalPoint, nlThousandsSep)
	m := localeStrings(resolveLocaleSetting(monetary, "LC_MONETARY"), lcMonetary,
		nlCurrencySymbol, nlPositiveSign, nlNegativeSign)
	return LocaleConv{DecimalPoint: n[0], ThousandsSep: n[1], CurrencySymbol: m[0], PositiveSign: m[1], NegativeSign: m[2]}
}

// MonetaryConv is the LC_MONETARY half of localeconv that cash_in and
// cash_out read. The char fields hold CHAR_MAX (127) when the locale leaves
// them unset, as the C locale does.
type MonetaryConv struct {
	CurrencySymbol  string
	MonDecimalPoint string
	MonThousandsSep string
	MonGrouping     int
	PositiveSign    string
	NegativeSign    string
	FracDigits      int
	PCsPrecedes     int
	PSepBySpace     int
	NCsPrecedes     int
	NSepBySpace     int
	PSignPosn       int
	NSignPosn       int
}

// LC_MONETARY item positions (glibc's _NL_ITEM order).
const (
	nlMonDecimalPoint = 2
	nlMonThousandsSep = 3
	nlMonGrouping     = 4
	nlFracDigits      = 8
	nlPCsPrecedes     = 9
	nlPSepBySpace     = 10
	nlNCsPrecedes     = 11
	nlNSepBySpace     = 12
	nlPSignPosn       = 13
	nlNSignPosn       = 14
)

// MonetaryConvFor is localeconv's monetary fields under the lc_monetary setting.
func MonetaryConvFor(monetary string) MonetaryConv {
	name := resolveLocaleSetting(monetary, "LC_MONETARY")
	m := localeStrings(name, lcMonetary, nlCurrencySymbol, nlMonDecimalPoint, nlMonThousandsSep,
		nlMonGrouping, nlPositiveSign, nlNegativeSign, nlFracDigits, nlPCsPrecedes, nlPSepBySpace,
		nlNCsPrecedes, nlNSepBySpace, nlPSignPosn, nlNSignPosn)
	if name == "C" || name == "POSIX" || m[7] == "" && m[1] == "" && m[0] == "" {
		// No data: the C locale, whose char fields are CHAR_MAX.
		return MonetaryConv{MonGrouping: 127, FracDigits: 127, PCsPrecedes: 127, PSepBySpace: 127,
			NCsPrecedes: 127, NSepBySpace: 127, PSignPosn: 127, NSignPosn: 127}
	}
	char := func(s string) int {
		if s == "" {
			return 0
		}
		return int(int8(s[0]))
	}
	return MonetaryConv{
		CurrencySymbol: m[0], MonDecimalPoint: m[1], MonThousandsSep: m[2], MonGrouping: char(m[3]),
		PositiveSign: m[4], NegativeSign: m[5], FracDigits: char(m[6]),
		PCsPrecedes: char(m[7]), PSepBySpace: char(m[8]), NCsPrecedes: char(m[9]), NSepBySpace: char(m[10]),
		PSignPosn: char(m[11]), NSignPosn: char(m[12]),
	}
}

