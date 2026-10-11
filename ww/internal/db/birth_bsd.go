//go:build darwin || freebsd

package db

import (
	"time"

	"golang.org/x/sys/unix"
)

// birthTime reports when the file at path was created. ok is false when it
// cannot be read or its filesystem records no birth time, which FreeBSD
// reports as a time at or before the epoch.
func birthTime(path string) (born time.Time, ok bool) {
	var st unix.Stat_t
	if err := unix.Stat(path, &st); err != nil || st.Btim.Sec <= 0 {
		return time.Time{}, false
	}
	return time.Unix(st.Btim.Unix()), true
}
