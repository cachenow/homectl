package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPhysicalDiskCapacityAt(t *testing.T) {
	root := t.TempDir()
	devices := filepath.Join(root, "devices")
	classBlock := filepath.Join(root, "class", "block")
	if err := os.MkdirAll(classBlock, 0o755); err != nil {
		t.Fatal(err)
	}

	makeBlock := func(name, deviceName, sectors string, partition bool) {
		t.Helper()
		block := filepath.Join(classBlock, name)
		if err := os.MkdirAll(block, 0o755); err != nil {
			t.Fatal(err)
		}
		dev := filepath.Join(devices, deviceName)
		if err := os.MkdirAll(dev, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(dev, filepath.Join(block, "device")); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(block, "size"), []byte(sectors+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if partition {
			if err := os.WriteFile(filepath.Join(block, "partition"), []byte("1\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}

	// Whole eMMC device: 1000 sectors. Its partition and boot area must not
	// increase the physical total. The boot node shares the same physical
	// /device target and is smaller than the main block node.
	makeBlock("mmcblk0", "mmc-card-0", "1000", false)
	makeBlock("mmcblk0p1", "mmc-card-0", "800", true)
	makeBlock("mmcblk0boot0", "mmc-card-0", "16", false)

	// A second independent disk.
	makeBlock("nvme0n1", "nvme-ns-0", "2000", false)

	// A logical block node without /device must be ignored.
	loop := filepath.Join(classBlock, "loop0")
	if err := os.MkdirAll(loop, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(loop, "size"), []byte("9999\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	total, count := physicalDiskCapacityAt(classBlock)
	want := uint64(3000 * 512)
	if total != want {
		t.Fatalf("physical total = %d, want %d", total, want)
	}
	if count != 2 {
		t.Fatalf("physical count = %d, want 2", count)
	}
}
