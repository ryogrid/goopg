package config

import _ "embed"

//go:embed postgresql.conf.sample
var sampleConfigBytes []byte

// SampleConfig returns the embedded `postgresql.conf.sample` bytes.
//
// The template enumerates every file-settable GUC registered by
// BuildDefaultRegistry with PG-style sections and inline hints. All
// entries are commented out, so writing these bytes to <datadir>/
// postgresql.conf produces a freshly-initted cluster whose effective
// configuration equals the registry's BootVals.
//
// Callers (notably `initdb.SampleFiles()`) must not mutate the returned
// slice — go:embed returns a backing array shared across the process.
func SampleConfig() []byte {
	return sampleConfigBytes
}
