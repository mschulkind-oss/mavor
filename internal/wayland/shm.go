package wayland

import (
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

// allocShared creates an anonymous shared-memory file of the given size and
// maps it. The compositor reads pixels straight out of this mapping, so a
// frame costs a memcpy into it and no copy out.
//
// memfd is used rather than a file under XDG_RUNTIME_DIR because it needs no
// path, cannot collide, and disappears with the last descriptor — there is no
// tmpfile to leak if the daemon is killed.
func allocShared(size int) (*os.File, []byte, error) {
	if size <= 0 {
		return nil, nil, fmt.Errorf("wayland: shared buffer size must be positive, got %d", size)
	}

	fd, err := unix.MemfdCreate("mavor-overlay", unix.MFD_CLOEXEC|unix.MFD_ALLOW_SEALING)
	if err != nil {
		return nil, nil, fmt.Errorf("wayland: memfd_create: %w", err)
	}
	f := os.NewFile(uintptr(fd), "mavor-overlay")

	if err := unix.Ftruncate(fd, int64(size)); err != nil {
		f.Close()
		return nil, nil, fmt.Errorf("wayland: sizing shared buffer to %d bytes: %w", size, err)
	}

	// Seal against further resizing. The compositor maps this by the size it
	// was told; a later shrink would turn its reads into SIGBUS.
	if _, err := unix.FcntlInt(uintptr(fd), unix.F_ADD_SEALS, unix.F_SEAL_SHRINK|unix.F_SEAL_GROW); err != nil {
		f.Close()
		return nil, nil, fmt.Errorf("wayland: sealing shared buffer: %w", err)
	}

	pix, err := unix.Mmap(fd, 0, size, unix.PROT_READ|unix.PROT_WRITE, unix.MAP_SHARED)
	if err != nil {
		f.Close()
		return nil, nil, fmt.Errorf("wayland: mmap %d bytes: %w", size, err)
	}
	return f, pix, nil
}

func unmapShared(pix []byte) error {
	if len(pix) == 0 {
		return nil
	}
	return unix.Munmap(pix)
}
