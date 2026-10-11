package db

import (
	"time"

	"golang.org/x/sys/unix"
)

// birthTime reports when the file at path was created. ok is false when it
// cannot be read or its filesystem records no birth time, which statx says by
// leaving STATX_BTIME out of the mask it answers with.
func birthTime(path string) (born time.Time, ok bool) {
	var st unix.Statx_t
	if err := unix.Statx(unix.AT_FDCWD, path, 0, unix.STATX_BTIME, &st); err != nil || st.Mask&unix.STATX_BTIME == 0 {
		return time.Time{}, false
	}
	return time.Unix(st.Btime.Sec, int64(st.Btime.Nsec)), true
}
