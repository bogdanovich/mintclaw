//go:build linux

package companion

import (
	"testing"

	"golang.org/x/sys/unix"
)

func TestAlwaysDeniedFileSystemTypeIncludesHighBitMagicValues(t *testing.T) {
	t.Parallel()

	for name, filesystemType := range map[string]uint32{
		"bpf":       unix.BPF_FS_MAGIC,
		"efivarfs":  unix.EFIVARFS_MAGIC,
		"hugetlbfs": unix.HUGETLBFS_MAGIC,
		"selinux":   unix.SELINUX_MAGIC,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if !alwaysDeniedFileSystemType(filesystemType) {
				t.Fatalf("alwaysDeniedFileSystemType(%#x) = false, want true", filesystemType)
			}
		})
	}
}

func TestAlwaysDeniedFileSystemTypeAllowsOrdinaryFileSystems(t *testing.T) {
	t.Parallel()

	if alwaysDeniedFileSystemType(unix.EXT4_SUPER_MAGIC) {
		t.Fatal("alwaysDeniedFileSystemType(EXT4_SUPER_MAGIC) = true, want false")
	}
}
