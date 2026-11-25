package datetime

import (
	"strings"
	"time"
)

// DecodeTimeOnlyInput is time_in's and timetz_in's parser: ParseDateTime and
// then DecodeTimeOnly (datetime.c). withZone is DecodeTimeOnly's tzp != NULL,
// true for timetz: zone fields are accepted and TZWest is set, from the input
// or, failing that, from the session zone at the input's date or today's.
func DecodeTimeOnlyInput(s string, env *DecodeEnv, withZone bool) (Decoded, error) {
	fields, err := parseDateTimeFields(s)
	if err != nil {
		return Decoded{}, err
	}
	x := &decoder{env: env}
	return x.runTimeOnly(fields, withZone)
}

func (x *decoder) runTimeOnly(fields []dateField, withZone bool) (Decoded, error) {
	var (
		fmask, tmask, ptype int
		mer                 = valHR24
		bc                  bool
		namedTz, abbrevTz   *time.Location
		abbrev              string
		err                 error
	)
	d := &x.d
	d.Type = DecodedDate
	nf := len(fields)
	for i, f := range fields {
		text := f.text
		switch f.kind {
		case dtkDate:
			if !withZone {
				return *d, ErrBadFormat
			}
			switch {
			case i == 0 && nf >= 2 && (fields[nf-1].kind == dtkDate || fields[1].kind == dtkTime):
				// A date followed by a time, or a date-like zone at the end.
				if tmask, err = x.decodeDate(text, fmask); err != nil {
					return *d, err
				}
			case isDigit(text[0]):
				// A concatenated time with a numeric zone, as from "hhmmss-zz".
				if fmask&dtkTimeM == dtkTimeM {
					return *d, ErrBadFormat
				}
				cp := strings.IndexByte(text, '-')
				if cp < 0 {
					return *d, ErrBadFormat
				}
				if d.TZWest, err = decodeTimezone(text[cp:]); err != nil {
					return *d, err
				}
				if tmask, err = x.decodeNumberField(text[:cp], fmask|dtkDateM); err != nil {
					return *d, err
				}
				tmask |= dtkM(tokTZ)
			default:
				loc, ok := x.env.LookupZone(text)
				if !ok {
					return *d, &BadTimezoneError{Name: text}
				}
				namedTz = loc
				tmask = dtkM(tokTZ)
			}
		case dtkTime:
			if ptype != 0 {
				if ptype != unitTime {
					return *d, ErrBadFormat
				}
				ptype = 0
			}
			tmask = dtkTimeM
			if err = x.decodeTime(text); err != nil {
				return *d, err
			}
		case dtkTZ:
			if !withZone {
				return *d, ErrBadFormat
			}
			if d.TZWest, err = decodeTimezone(text); err != nil {
				return *d, err
			}
			tmask = dtkM(tokTZ)
		case dtkNumber:
			if ptype != 0 {
				value, cp, overflow, _ := strtoint(text)
				if overflow {
					return *d, ErrFieldOutOfRange
				}
				if cp != "" && cp[0] != '.' {
					return *d, ErrBadFormat
				}
				switch ptype {
				case unitJulian:
					if !withZone {
						return *d, ErrBadFormat
					}
					if value < 0 {
						return *d, ErrFieldOutOfRange
					}
					tmask = dtkDateM
					d.Year, d.Mon, d.Mday = j2date(int64(value))
					x.isjulian = true
					if cp != "" {
						frac, err := parseFraction(cp)
						if err != nil {
							return *d, err
						}
						us := int64(frac * 86400000000)
						d.Hour = int(us / 3600000000)
						us -= int64(d.Hour) * 3600000000
						d.Min = int(us / 60000000)
						us -= int64(d.Min) * 60000000
						d.Sec = int(us / 1000000)
						d.Usec = int(us - int64(d.Sec)*1000000)
						tmask |= dtkTimeM
					}
				case unitTime:
					if tmask, err = x.decodeNumberField(text, fmask|dtkDateM); err != nil {
						return *d, err
					}
					if tmask != dtkTimeM {
						return *d, ErrBadFormat
					}
				default:
					return *d, ErrBadFormat
				}
				ptype = 0
				break
			}
			cp := strings.IndexByte(text, '.')
			switch {
			case cp >= 0 && i == 0 && nf >= 2 && fields[nf-1].kind == dtkDate:
				tmask, err = x.decodeDate(text, fmask)
			case cp > 2:
				tmask, err = x.decodeNumberField(text, fmask|dtkDateM)
			case cp >= 0:
				return *d, ErrBadFormat
			case len(text) > 4:
				tmask, err = x.decodeNumberField(text, fmask|dtkDateM)
			default:
				tmask, err = x.decodeNumber(text, fmask|dtkDateM)
			}
			if err != nil {
				return *d, err
			}
		case dtkString, dtkSpecial:
			kind, val := tokUnknown, 0
			var dynZone *time.Location
			if off, isDST, fixed, ok := abbrevInZone(text, x.location()); ok {
				d.UsedSession = true
				switch {
				case !fixed:
					kind, dynZone = tokDynTZ, x.location()
				case isDST:
					kind, val = tokDTZ, off
				default:
					kind, val = tokTZ, off
				}
			} else if ab, ok := x.env.abbrev(text); ok {
				kind, val = ab.kind, ab.offset
				if ab.kind == tokDynTZ {
					if dynZone, ok = x.env.LookupZone(ab.zone); !ok {
						return *d, &BadTimezoneError{Name: ab.zone}
					}
				}
			} else if tok, ok := lookupDateToken(text); ok {
				kind, val = tok.kind, tok.value
			}
			if kind == tokIgnore {
				continue
			}
			tmask = dtkM(kind)
			switch kind {
			case tokReserv:
				switch val {
				case valNow:
					tmask = dtkTimeM
					d.UsedSession = true
					now := x.env.Now.In(x.location())
					d.Hour, d.Min, d.Sec = now.Clock()
					d.Usec = now.Nanosecond() / 1000
				case valZulu:
					tmask = dtkTimeM | dtkM(tokTZ)
					d.Hour, d.Min, d.Sec, d.TZWest = 0, 0, 0, 0
				default:
					return *d, ErrBadFormat
				}
			case tokDTZMod:
				tmask |= dtkM(tokDTZ)
				if !withZone {
					return *d, ErrBadFormat
				}
				d.TZWest -= val
			case tokDTZ:
				tmask |= dtkM(tokTZ)
				if !withZone {
					return *d, ErrBadFormat
				}
				d.TZWest = -val
			case tokTZ:
				if !withZone {
					return *d, ErrBadFormat
				}
				d.TZWest = -val
			case tokDynTZ:
				tmask |= dtkM(tokTZ)
				if !withZone {
					return *d, ErrBadFormat
				}
				abbrevTz, abbrev = dynZone, text
			case tokAmPm:
				mer = val
			case tokADBC:
				bc = val == valBC
			case tokUnits, tokISOTime:
				tmask = 0
				if ptype != 0 {
					return *d, ErrBadFormat
				}
				ptype = val
			case tokUnknown:
				loc, ok := x.env.LookupZone(text)
				if !ok {
					return *d, ErrBadFormat
				}
				namedTz = loc
				tmask = dtkM(tokTZ)
			default:
				return *d, ErrBadFormat
			}
		default:
			return *d, ErrBadFormat
		}
		if tmask&fmask != 0 {
			return *d, ErrBadFormat
		}
		fmask |= tmask
	}
	if ptype != 0 {
		return *d, ErrBadFormat
	}
	if err := x.validateDate(fmask, bc); err != nil {
		return *d, err
	}
	if mer != valHR24 && d.Hour > 12 {
		return *d, ErrFieldOutOfRange
	}
	if mer == valAM && d.Hour == 12 {
		d.Hour = 0
	} else if mer == valPM && d.Hour != 12 {
		d.Hour += 12
	}
	if timeOverflows(d.Hour, d.Min, d.Sec, d.Usec) {
		return *d, ErrFieldOutOfRange
	}
	if fmask&dtkTimeM != dtkTimeM {
		return *d, ErrBadFormat
	}
	hasMod := fmask&dtkM(tokDTZMod) != 0
	// withDate gives the zone lookups a date: the input's, which must then be
	// complete, or today's in the session zone (GetCurrentDateTime).
	withDate := func() error {
		if fmask&dtkDateM == 0 {
			now := x.env.Now.In(x.location())
			d.Year, d.Mon, d.Mday = now.Year(), int(now.Month()), now.Day()
			return nil
		}
		if fmask&dtkDateM != dtkDateM {
			return ErrBadFormat
		}
		return nil
	}
	if namedTz != nil {
		if hasMod {
			return *d, ErrBadFormat
		}
		if off, fixed := fixedZoneEast(namedTz); fixed {
			d.TZWest = -off
		} else {
			if fmask&dtkDateM != dtkDateM {
				return *d, ErrBadFormat
			}
			d.TZWest = x.westOffsetOf(namedTz)
		}
	}
	if abbrevTz != nil {
		if hasMod {
			return *d, ErrBadFormat
		}
		if err := withDate(); err != nil {
			return *d, err
		}
		d.TZWest = x.westOffsetOf(abbrevTz)
		probe := d.Wall().Unix() + int64(d.TZWest)
		if east, ok := abbrevOffsetAt(abbrev, abbrevTz, probe); ok {
			d.TZWest = -east
		}
	}
	if withZone && fmask&dtkM(tokTZ) == 0 {
		if hasMod {
			return *d, ErrBadFormat
		}
		if err := withDate(); err != nil {
			return *d, err
		}
		d.UsedSession = true
		d.TZWest = x.westOffsetOf(x.location())
	}
	return *d, nil
}

// fixedZoneEast is pg_get_timezone_offset: the offset of a zone that has
// only ever had one.
func fixedZoneEast(loc *time.Location) (int, bool) {
	periods := zonePeriods(loc)
	if len(periods) == 0 {
		return 0, false
	}
	off := periods[0].offset
	for _, p := range periods[1:] {
		if p.offset != off {
			return 0, false
		}
	}
	return off, true
}
