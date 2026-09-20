//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package gola

import (
	"os"
	"syscall"
)

func fileStorePlatformSupported() bool { return true }

// Nonblocking open prevents a regular file replaced by a FIFO from hanging the
// server. The caller verifies both regular-file type and identity after opening.
func openStoredFile(root *os.Root, name string) (*os.File, error) {
	return root.OpenFile(name, os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW, 0)
}
