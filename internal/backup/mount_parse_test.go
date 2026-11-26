package backup

import "testing"

func TestMountInfoTypeFindsMostSpecificNFSMount(t *testing.T) {
	info := "36 25 8:1 / /mnt rw,relatime - ext4 /dev/sda1 rw\n" +
		"45 36 0:55 / /mnt/NAS\\040Share rw,relatime - nfs4 server:/export rw\n" +
		"46 45 0:56 /nested /mnt/NAS\\040Share/private rw,relatime - ext4 /dev/mapper/secure rw\n"
	if got := mountTypeFromInfo("/mnt/NAS Share/backups", info); got != "nfs4" {
		t.Fatalf("mount type under NFS = %q", got)
	}
	if got := mountTypeFromInfo("/mnt/NAS Share/private", info); got != "ext4" {
		t.Fatalf("most-specific mount type = %q, want ext4", got)
	}
	if got := mountTypeFromInfo("/mnt/NAS Share-other", info); got != "ext4" {
		t.Fatalf("prefix-only path incorrectly matched NFS: %q", got)
	}
}
