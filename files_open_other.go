//go:build !aix && !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris

package gola

import (
	"os"
	"runtime"
)

// Windows Root rejects reserved devices and constrains reparse points. Platforms
// without the needed confinement and file-opening guarantees fail closed.
func fileStorePlatformSupported() bool { return runtime.GOOS == "windows" }

func openStoredFile(root *os.Root, name string) (*os.File, error) {
	return root.Open(name)
}
