# The timezone\_abbreviations setting (M0149-0001h1a82)

## Problem

Date/time input always read zone abbreviations from the Default set. `SET timezone_abbreviations = 'India'` was
accepted, but `IST` still meant Israel \(+02\) instead of India \(+05:30\). pg\_timezone\_abbrevs always listed
Default, and any set name was accepted.

## PG behaviour

check\_timezone\_abbreviations and load\_tzoffsets \(tzparser.c\) load the file named by the setting from
share/timezonesets. A file contains:

- `ABBREV offset [D]` lines and `ABBREV zone` lines;
- `@INCLUDE name`, which splices in another file;
- `@OVERRIDE`, which lets the file replace included entries. Without it a conflicting entry is an error.

File names are letters, digits and `_`, so the `*.txt` reference files cannot be named directly. A set that
fails to load is rejected with 22023 `invalid value for parameter "timezone_abbreviations"`.

## Design

- The three loadable sets PG ships \(Default, Australia, India\) are embedded in
  internal/utils/adt/datetime/tznames.
- `parseTzFile` \(abbrev\_sets.go\) ports ParseTzFile, with the same name check and @INCLUDE depth limit, and
  `abbrevSet` caches each loaded set. Default stays the generated table; the parsed Default is checked against it
  in TestAbbrevSetsLoadLikePG.
- `DecodeEnv.AbbrevSet` selects the set. Date and time input looks an abbreviation up there, after the session
  zone's own abbreviations, as before.
- The executor's `dateInput` carries the setting.
- `PlannerSettings.TimezoneAbbreviations` carries it to the plan\-time canonicalizer, which reads literals when
  the statement is planned. It is part of the plan cache key. PREPARE records it with TimeZone and DateStyle.
- pg\_timezone\_abbrevs lists the active set \(`AbbrevRows`\).
- The GUC has a CheckFn that rejects a set that does not load.

## Not done

- utc\_offset is still printed in postgres\_verbose style \(M0149\-0001h1a89\).
- A set can't be installed at run time: there is no share/timezonesets directory to read user files from.
