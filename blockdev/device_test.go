// SPDX-License-Identifier: Apache-2.0

package blockdev

import (
	"errors"
	"os"
	"syscall"
	"testing"
)

// /dev/null is a device node but not a block device: the block ioctls must
// fail cleanly instead of being skipped as for a regular file.
func TestCharacterDeviceIoctlsFail(t *testing.T) {
	d, err := Open(os.DevNull, false)
	if err != nil {
		t.Skip(err)
	}
	defer d.Close()
	if _, err := d.Size(); !errors.Is(err, syscall.ENOTTY) {
		t.Errorf("Size: %v", err)
	}
	if err := d.FlushBuffers(); !errors.Is(err, syscall.ENOTTY) {
		t.Errorf("FlushBuffers: %v", err)
	}
}

func TestOpenMissing(t *testing.T) {
	if _, err := Open("/nonexistent/cryptoerase", true); !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
}

func TestClosedFile(t *testing.T) {
	p := t.TempDir() + "/disk"
	if err := os.WriteFile(p, make([]byte, 4096), 0o600); err != nil {
		t.Fatal(err)
	}
	d, err := Open(p, false)
	if err != nil {
		t.Fatal(err)
	}
	d.Close()
	if _, err := d.Size(); err == nil {
		t.Error("Size on closed file")
	}
	if err := d.FlushBuffers(); err == nil {
		t.Error("FlushBuffers on closed file")
	}
}
