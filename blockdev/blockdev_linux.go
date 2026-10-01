// SPDX-License-Identifier: Apache-2.0

// Package blockdev does aligned, O_DIRECT reads and writes on block devices,
// used to place and check the verification markers.
package blockdev

import (
	"fmt"
	"io"
	"os"
	"syscall"
	"unsafe"
)

const (
	ioctlBLKGETSIZE64 = 0x80081272
	ioctlBLKFLSBUF    = 0x1261
)

// Device is the I/O surface used for markers. *File implements it.
type Device interface {
	io.ReaderAt
	io.WriterAt
	Size() (int64, error)
	Sync() error
	Close() error
}

// File is an open block device (or, in tests, a regular file).
type File struct {
	f      *os.File
	direct bool
}

// Open opens path read-write. With direct=true it uses O_DIRECT, so every
// buffer, offset and length must be aligned to the logical block size; use
// Buffer for allocation.
func Open(path string, direct bool) (*File, error) {
	flags := os.O_RDWR
	if direct {
		flags |= syscall.O_DIRECT
	}
	f, err := os.OpenFile(path, flags, 0)
	if err != nil {
		return nil, err
	}
	return &File{f: f, direct: direct}, nil
}

// ReadAt implements io.ReaderAt.
func (d *File) ReadAt(p []byte, off int64) (int, error) { return d.f.ReadAt(p, off) }

// WriteAt implements io.WriterAt.
func (d *File) WriteAt(p []byte, off int64) (int, error) { return d.f.WriteAt(p, off) }

// Sync flushes the device write cache path (fsync).
func (d *File) Sync() error { return d.f.Sync() }

// Close closes the device.
func (d *File) Close() error { return d.f.Close() }

// Size returns the device size in bytes (BLKGETSIZE64), or the file size for
// regular files.
func (d *File) Size() (int64, error) {
	st, err := d.f.Stat()
	if err != nil {
		return 0, err
	}
	if st.Mode()&os.ModeDevice == 0 {
		return st.Size(), nil
	}
	var size uint64
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, d.f.Fd(), ioctlBLKGETSIZE64, uintptr(unsafe.Pointer(&size)))
	if errno != 0 {
		return 0, fmt.Errorf("BLKGETSIZE64 %s: %w", d.f.Name(), errno)
	}
	return int64(size), nil
}

// FlushBuffers drops the kernel's buffer cache for the device (BLKFLSBUF).
// No-op for regular files.
func (d *File) FlushBuffers() error {
	st, err := d.f.Stat()
	if err != nil || st.Mode()&os.ModeDevice == 0 {
		return err
	}
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, d.f.Fd(), ioctlBLKFLSBUF, 0)
	if errno != 0 {
		return fmt.Errorf("BLKFLSBUF %s: %w", d.f.Name(), errno)
	}
	return nil
}

// Buffer returns n bytes of page-aligned memory suitable for O_DIRECT, and a
// release function.
func Buffer(n int) ([]byte, func(), error) {
	b, err := syscall.Mmap(-1, 0, n, syscall.PROT_READ|syscall.PROT_WRITE, syscall.MAP_ANON|syscall.MAP_PRIVATE)
	if err != nil {
		return nil, nil, fmt.Errorf("mmap %d bytes: %w", n, err)
	}
	return b, func() { _ = syscall.Munmap(b) }, nil
}

var _ Device = (*File)(nil)
