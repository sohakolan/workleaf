package main

import (
	"encoding/binary"
	"fmt"
	"unsafe"

	"golang.org/x/sys/unix"
)

// markerAttr marks a directory as a workspace, so `ls` and `rm` can tell it apart from
// the intermediate folders of names like feature/test. Invisible to tools.
const markerAttr = "com.workleaf.workspace"

func isWorkspace(dir string) bool {
	_, err := unix.Getxattr(dir, markerAttr, nil)
	return err == nil
}

// privateSize returns the bytes of a file that APFS does not share with any clone
// (getattrlist ATTR_CMNEXT_PRIVATESIZE, which x/sys/unix does not wrap).
func privateSize(path string) (int64, error) {
	const attrCmnextPrivateSize = 0x8
	al := unix.Attrlist{Bitmapcount: unix.ATTR_BIT_MAP_COUNT, Forkattr: attrCmnextPrivateSize}
	var buf [12]byte // u_int32_t length, then off_t private size
	p, err := unix.BytePtrFromString(path)
	if err != nil {
		return 0, err
	}
	_, _, errno := unix.Syscall6(unix.SYS_GETATTRLIST,
		uintptr(unsafe.Pointer(p)), uintptr(unsafe.Pointer(&al)),
		uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)),
		unix.FSOPT_NOFOLLOW|unix.FSOPT_ATTR_CMN_EXTENDED, 0)
	if errno != 0 {
		return 0, fmt.Errorf("getattrlist %s: %w", path, errno)
	}
	return int64(binary.LittleEndian.Uint64(buf[4:])), nil
}
