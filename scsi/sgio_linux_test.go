// SPDX-License-Identifier: Apache-2.0

//go:build linux

package scsi

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"unsafe"
)

func TestHeaderLayout(t *testing.T) {
	if unsafe.Sizeof(uintptr(0)) != 8 {
		t.Skip("layout checked on 64-bit only")
	}
	var h sgIOHdr
	for name, got := range map[string]uintptr{
		"size": unsafe.Sizeof(h), "dxferp": unsafe.Offsetof(h.Dxferp), "cmdp": unsafe.Offsetof(h.Cmdp),
		"sbp": unsafe.Offsetof(h.Sbp), "timeout": unsafe.Offsetof(h.Timeout), "usr_ptr": unsafe.Offsetof(h.UsrPtr),
		"status": unsafe.Offsetof(h.Status), "host_status": unsafe.Offsetof(h.HostStatus), "resid": unsafe.Offsetof(h.Resid),
		"info": unsafe.Offsetof(h.Info),
	} {
		want := map[string]uintptr{"size": 88, "dxferp": 16, "cmdp": 24, "sbp": 32, "timeout": 40, "usr_ptr": 56,
			"status": 64, "host_status": 68, "resid": 72, "info": 80}[name]
		if got != want {
			t.Errorf("%s: %d, want %d", name, got, want)
		}
	}
}

// kernel is what the fake ioctl sees: the header and, by the addresses in
// it, the CDB, sense buffer and data.
type kernel struct {
	h               *sgIOHdr
	cdb, sense, buf []byte
}

// at returns the n bytes at address p of mem, failing the test when p does
// not point into mem.
func at(t *testing.T, mem []byte, p uintptr, n int) []byte {
	t.Helper()
	base := uintptr(unsafe.Pointer(&mem[0]))
	if p < base || p-base+uintptr(n) > uintptr(len(mem)) {
		t.Fatalf("address %#x (+%d) outside the buffer", p, n)
	}
	off := int(p - base)
	return mem[off : off+n]
}

func fakeDevice(t *testing.T, reply func(k kernel) syscall.Errno) (*Device, *[]sgIOHdr) {
	var log []sgIOHdr
	d := &Device{path: "/dev/sd-test"}
	d.ioctl = func(h *sgIOHdr, mem []byte) syscall.Errno {
		log = append(log, *h)
		k := kernel{h: h, cdb: at(t, mem, h.Cmdp, int(h.CmdLen)), sense: at(t, mem, h.Sbp, int(h.MxSbLen))}
		if h.DxferLen > 0 {
			k.buf = at(t, mem, h.Dxferp, int(h.DxferLen))
		}
		return reply(k)
	}
	return d, &log
}

func TestExecFromDevice(t *testing.T) {
	d, log := fakeDevice(t, func(k kernel) syscall.Errno {
		h := k.h
		if h.InterfaceID != 'S' || h.DxferDirection != sgDxferFromDev || h.CmdLen != 10 || h.MxSbLen != senseLen || h.DxferLen != 12 || h.Timeout != 10000 {
			t.Errorf("header %+v", *h)
		}
		if k.cdb[0] != 0x5a {
			t.Errorf("CDB % x", k.cdb)
		}
		copy(k.buf, "control page")
		return 0
	})
	buf := make([]byte, 12)
	if err := d.Exec([]byte{0x5a, 0x08, 0x0a, 0, 0, 0, 0, 0, 12, 0}, DirFromDevice, buf); err != nil {
		t.Fatal(err)
	}
	if string(buf) != "control page" || len(*log) != 1 {
		t.Fatalf("%q", buf)
	}
}

func TestExecToDevice(t *testing.T) {
	var got []byte
	d, _ := fakeDevice(t, func(k kernel) syscall.Errno {
		if k.h.DxferDirection != sgDxferToDev {
			t.Errorf("direction %d", k.h.DxferDirection)
		}
		got = append(got, k.buf...)
		return 0
	})
	if err := d.Exec([]byte{0x55, 0x10, 0, 0, 0, 0, 0, 0, 3, 0}, DirToDevice, []byte{1, 2, 3}); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, []byte{1, 2, 3}) {
		t.Fatalf("% x", got)
	}
	d, _ = fakeDevice(t, func(k kernel) syscall.Errno {
		if k.h.DxferDirection != sgDxferNone || k.h.Dxferp != 0 {
			t.Errorf("no-data header %+v", *k.h)
		}
		return 0
	})
	if err := d.Exec([]byte{0, 0, 0, 0, 0, 0}, DirNone, nil); err != nil {
		t.Fatal(err)
	}
}

func TestExecErrors(t *testing.T) {
	cc, _ := fakeDevice(t, func(k kernel) syscall.Errno {
		copy(k.sense, illegalRequest)
		k.h.Status, k.h.SbLenWr, k.h.DriverStatus = statusCheckCondition, uint8(len(illegalRequest)), driverSense
		return 0
	})
	err := cc.Exec([]byte{0x5a, 0, 0x0a, 0, 0, 0, 0, 0, 8, 0}, DirFromDevice, make([]byte, 8))
	var ce *CheckCondition
	if !errors.As(err, &ce) || !bytes.Equal(ce.Sense, illegalRequest) {
		t.Fatalf("check condition: %v", err)
	}
	for name, tc := range map[string]struct {
		reply func(k kernel) syscall.Errno
		want  string
	}{
		"errno":  {func(kernel) syscall.Errno { return syscall.ENOTTY }, "inappropriate ioctl"},
		"status": {func(k kernel) syscall.Errno { k.h.Status = 0x08; return 0 }, "SCSI status 0x8"},
		"host":   {func(k kernel) syscall.Errno { k.h.HostStatus = 0x07; return 0 }, "host status 0x7"},
		"driver": {func(k kernel) syscall.Errno { k.h.DriverStatus = 0x06; return 0 }, "driver status 0x6"},
	} {
		d, _ := fakeDevice(t, tc.reply)
		if err := d.Exec([]byte{0, 0, 0, 0, 0, 0}, DirNone, nil); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v", name, err)
		}
	}
	// Sense data with GOOD status (a recovered error) is not a failure.
	ok, _ := fakeDevice(t, func(k kernel) syscall.Errno { k.h.DriverStatus, k.h.SbLenWr = driverSense, 8; return 0 })
	if err := ok.Exec([]byte{0, 0, 0, 0, 0, 0}, DirNone, nil); err != nil {
		t.Errorf("good status with sense: %v", err)
	}
	for _, cdb := range [][]byte{nil, make([]byte, 17)} {
		if err := ok.Exec(cdb, DirNone, nil); !errors.Is(err, errTooLarge) {
			t.Errorf("cdb of %d bytes: %v", len(cdb), err)
		}
	}
	if err := ok.Exec([]byte{0}, DirFromDevice, make([]byte, bufferSize)); !errors.Is(err, errTooLarge) {
		t.Errorf("large transfer: %v", err)
	}
}

// A regular file is no SCSI device: SG_IO fails, and the error says where.
func TestSetDescriptorSenseOnFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "sdz")
	if err := os.WriteFile(p, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := SetDescriptorSenseOn(p); err == nil || !strings.Contains(err.Error(), "SG_IO on "+p) {
		t.Fatalf("%v", err)
	}
	if _, err := SetDescriptorSenseOn(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("missing device accepted")
	}
	if err := (&Device{}).Close(); err != nil {
		t.Fatal(err)
	}
}
