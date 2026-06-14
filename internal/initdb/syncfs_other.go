//go:build !linux

package initdb

import "errors"

// syncfsSupported is false on builds without syncfs(2), mirroring upstream
// initdb's HAVE_SYNCFS gate. resolveSyncMethod rejects --sync-method=syncfs
// before syncfsPath is ever reached on these platforms, matching the
// command_fails arm of 001_initdb.pl's syncfs test.
const syncfsSupported = false

// syncfsPath is unreachable on non-Linux builds (resolveSyncMethod errors
// out first); it exists only to satisfy the syncDataDir reference.
func syncfsPath(path string) error {
	return errors.New("syncfs sync method is not supported on this platform")
}
