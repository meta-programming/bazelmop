//go:build linux

package dedupe

import "golang.org/x/sys/unix"

func umask(mask int) int { return unix.Umask(mask) }
