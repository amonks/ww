//go:build !darwin && !freebsd && !linux

package db

import "time"

// birthTime reports when the file at path was created. On this platform ww
// does not read birth times, so it never knows.
func birthTime(string) (time.Time, bool) { return time.Time{}, false }
