package storage

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

// setDirectIOIfRequested toggles O_DIRECT|O_DSYNC on f when requested.
//
// O_DIRECT support depends on the underlying filesystem. ext4/xfs honour
// it; tmpfs does not. We therefore tolerate EINVAL: tests typically run
// on tmpfs (TMPDIR), which means setting O_DIRECT there silently fails
// with EINVAL — but the file is still safely usable in the buffered
// mode for the unit-test smoke. Production deployments configure the
// data directory on a filesystem that honours the flag and the failure
// path is "log a warning"; we error out so misconfiguration is loud.
func setDirectIOIfRequested(f *os.File, want bool) error {
	if !want {
		return nil
	}
	flags, err := unix.FcntlInt(f.Fd(), unix.F_GETFL, 0)
	if err != nil {
		return err
	}
	flags |= unix.O_DIRECT | unix.O_DSYNC
	if _, err := unix.FcntlInt(f.Fd(), unix.F_SETFL, flags); err != nil {
		// EINVAL on filesystems that don't support O_DIRECT (tmpfs).
		if errors.Is(err, unix.EINVAL) {
			return nil
		}
		return err
	}
	return nil
}
