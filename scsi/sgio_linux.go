// SPDX-License-Identifier: Apache-2.0

//go:build linux

package scsi

import (
	"errors"
	"fmt"
	"os"
	"syscall"
	"time"
	"unsafe"
)

const (
	ioctlSGIO = 0x2285 // SG_IO

	sgDxferNone    = -1
	sgDxferToDev   = -2
	sgDxferFromDev = -3

	statusGood           = 0x00
	statusCheckCondition = 0x02
	driverSense          = 0x08 // DRIVER_SENSE: sense data is valid

	senseLen   = 64
	bufferSize = 4096
	cmdTimeout = 10 * time.Second
)

// sgIOHdr mirrors struct sg_io_hdr from <scsi/sg.h> (88 bytes on 64-bit
// Linux; Go aligns the fields the same way as C).
type sgIOHdr struct {
	InterfaceID    int32
	DxferDirection int32
	CmdLen         uint8
	MxSbLen        uint8
	IovecCount     uint16
	DxferLen       uint32
	Dxferp         uintptr
	Cmdp           uintptr
	Sbp            uintptr
	Timeout        uint32
	Flags          uint32
	PackID         int32
	UsrPtr         uintptr
	Status         uint8
	MaskedStatus   uint8
	MsgStatus      uint8
	SbLenWr        uint8
	HostStatus     uint16
	DriverStatus   uint16
	Resid          int32
	Duration       uint32
	Info           uint32
}

// Device is a SCSI disk opened for SG_IO.
type Device struct {
	f    *os.File
	path string
	// ioctl issues SG_IO; mem is the memory hdr points into. Tests replace
	// it.
	ioctl func(hdr *sgIOHdr, mem []byte) syscall.Errno
}

// Open opens a SCSI disk (/dev/sdX) read-only. Root (CAP_SYS_RAWIO) may send
// any command through a read-only handle, and closing one does not make
// udev re-read the disk, as closing a handle opened for writing does.
func Open(path string) (*Device, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	d := &Device{f: f, path: path}
	d.ioctl = d.sysIoctl
	return d, nil
}

func (d *Device) sysIoctl(hdr *sgIOHdr, _ []byte) syscall.Errno {
	// G103: the kernel ABI takes a pointer to struct sg_io_hdr.
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, d.f.Fd(), ioctlSGIO, uintptr(unsafe.Pointer(hdr))) //nolint:gosec // G103, see above
	return errno
}

// Close closes the device.
func (d *Device) Close() error {
	if d.f == nil {
		return nil
	}
	return d.f.Close()
}

var errTooLarge = errors.New("data transfer too large")

// Exec sends one CDB. The CDB, the data and the sense buffer live in memory
// outside the Go heap while the kernel uses them.
func (d *Device) Exec(cdb []byte, dir int, data []byte) error {
	if len(cdb) == 0 || len(cdb) > 16 || len(data) > bufferSize-16-senseLen {
		return fmt.Errorf("SG_IO on %s: %w", d.path, errTooLarge)
	}
	mem, err := syscall.Mmap(-1, 0, bufferSize, syscall.PROT_READ|syscall.PROT_WRITE, syscall.MAP_ANON|syscall.MAP_PRIVATE)
	if err != nil {
		return fmt.Errorf("SG_IO on %s: mmap: %w", d.path, err)
	}
	defer func() { _ = syscall.Munmap(mem) }()
	cmdp, sbp, datap := mem[0:16], mem[16:16+senseLen], mem[16+senseLen:]
	copy(cmdp, cdb)
	hdr := sgIOHdr{
		InterfaceID:    'S',
		DxferDirection: sgDxferNone,
		CmdLen:         uint8(len(cdb)), //nolint:gosec // G115: at most 16, checked above
		MxSbLen:        senseLen,
		// G103: the kernel ABI takes user-space addresses; mem is not Go heap.
		Cmdp:    uintptr(unsafe.Pointer(&cmdp[0])), //nolint:gosec // G103, see above
		Sbp:     uintptr(unsafe.Pointer(&sbp[0])),  //nolint:gosec // G103, see above
		Timeout: uint32(cmdTimeout / time.Millisecond),
	}
	if len(data) > 0 && dir != DirNone {
		hdr.DxferDirection = sgDxferFromDev
		if dir == DirToDevice {
			hdr.DxferDirection = sgDxferToDev
			copy(datap, data)
		}
		hdr.DxferLen = uint32(len(data))                //nolint:gosec // G115: bounded by the check above
		hdr.Dxferp = uintptr(unsafe.Pointer(&datap[0])) //nolint:gosec // G103, see above
	}
	if errno := d.ioctl(&hdr, mem); errno != 0 {
		return fmt.Errorf("SG_IO on %s: %w", d.path, errno)
	}
	if hdr.DxferDirection == sgDxferFromDev {
		copy(data, datap)
	}
	switch {
	case hdr.Status == statusCheckCondition:
		n := min(int(hdr.SbLenWr), senseLen)
		return &CheckCondition{Sense: append([]byte(nil), sbp[:n]...)}
	case hdr.Status != statusGood:
		return fmt.Errorf("SG_IO on %s: SCSI status %#x", d.path, hdr.Status)
	case hdr.HostStatus != 0:
		return fmt.Errorf("SG_IO on %s: host status %#x", d.path, hdr.HostStatus)
	case hdr.DriverStatus&0x0f != 0 && hdr.DriverStatus&0x0f != driverSense:
		return fmt.Errorf("SG_IO on %s: driver status %#x", d.path, hdr.DriverStatus)
	}
	return nil
}

// SetDescriptorSenseOn opens the SCSI disk at path and sets D_SENSE (see
// SetDescriptorSense).
func SetDescriptorSenseOn(path string) (bool, error) {
	d, err := Open(path)
	if err != nil {
		return false, err
	}
	defer d.Close()
	return SetDescriptorSense(d.Exec)
}
