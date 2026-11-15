//go:build linux

package store

import (
	"fmt"
	"syscall"
)

// networkFilesystemWarning flags state directories on network filesystems,
// where SQLite locking and durability are not assumed to be reliable.
func networkFilesystemWarning(dir string) string {
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return ""
	}
	names := map[int64]string{
		0x6969:     "NFS",
		0x517B:     "SMB",
		0xFF534D42: "CIFS",
		0xFE534D42: "SMB2",
		0x564c:     "NCP",
		0x01021997: "9P",
	}
	if name, ok := names[int64(st.Type)]; ok {
		return fmt.Sprintf("state directory %s is on a %s network filesystem; this is not a supported state-store location. Use local storage", dir, name)
	}
	return ""
}
