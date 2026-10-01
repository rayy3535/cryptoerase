// SPDX-License-Identifier: Apache-2.0

package blockdev

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"unsafe"
)

func TestBufferAligned(t *testing.T) {
	b, release, err := Buffer(1 << 20)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if len(b) != 1<<20 || uintptr(unsafe.Pointer(&b[0]))%4096 != 0 {
		t.Fatalf("len %d addr %p", len(b), &b[0])
	}
}

func TestRegularFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "disk")
	if err := os.WriteFile(p, make([]byte, 4<<20), 0o644); err != nil {
		t.Fatal(err)
	}
	d, err := Open(p, false)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if n, err := d.Size(); err != nil || n != 4<<20 {
		t.Fatalf("size %d %v", n, err)
	}
	w := bytes.Repeat([]byte{0xa5}, 4096)
	if _, err := d.WriteAt(w, 1<<20); err != nil {
		t.Fatal(err)
	}
	if err := d.Sync(); err != nil {
		t.Fatal(err)
	}
	r := make([]byte, 4096)
	if _, err := d.ReadAt(r, 1<<20); err != nil || !bytes.Equal(r, w) {
		t.Fatalf("read back %v", err)
	}
	if err := d.FlushBuffers(); err != nil {
		t.Fatalf("flush on regular file: %v", err)
	}
}
