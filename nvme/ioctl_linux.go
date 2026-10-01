// SPDX-License-Identifier: Apache-2.0

//go:build linux

package nvme

import (
	"fmt"
	"os"
	"runtime"
	"syscall"
	"time"
	"unsafe"
)

const (
	opGetLogPage   = 0x02
	opIdentify     = 0x06
	opFormatNVM    = 0x80
	opSecurityRecv = 0x82
	opSanitize     = 0x84

	ioctlAdminCmd = 0xC0484E41 // _IOWR('N', 0x41, struct nvme_passthru_cmd)
	ioctlID       = 0x4E40     // _IO('N', 0x40)
	ioctlRescan   = 0x4E46     // _IO('N', 0x46)

	defaultTimeout = 60 * time.Second
)

// passthruCmd mirrors struct nvme_passthru_cmd from <linux/nvme_ioctl.h>
// (72 bytes, identical layout on all 64-bit Linux ABIs).
type passthruCmd struct {
	Opcode      uint8
	Flags       uint8
	Rsvd1       uint16
	NSID        uint32
	CDW2        uint32
	CDW3        uint32
	Metadata    uint64
	Addr        uint64
	MetadataLen uint32
	DataLen     uint32
	CDW10       uint32
	CDW11       uint32
	CDW12       uint32
	CDW13       uint32
	CDW14       uint32
	CDW15       uint32
	TimeoutMs   uint32
	Result      uint32
}

// Controller is an open NVMe controller character device (/dev/nvmeN).
// Admin passthrough requires CAP_SYS_ADMIN.
type Controller struct {
	f    *os.File
	path string
	// ioctl issues one request; cmd is nil for requests without an argument.
	// It returns the ioctl return value (the NVMe status for admin commands).
	// Tests replace it to inspect exactly what would be sent to the kernel.
	ioctl func(req uintptr, cmd *passthruCmd, data []byte) (uintptr, syscall.Errno)
}

// OpenController opens the controller character device.
func OpenController(path string) (*Controller, error) {
	f, err := os.OpenFile(path, os.O_RDONLY, 0)
	if err != nil {
		return nil, err
	}
	c := &Controller{f: f, path: path}
	c.ioctl = c.sysIoctl
	return c, nil
}

func (c *Controller) sysIoctl(req uintptr, cmd *passthruCmd, data []byte) (uintptr, syscall.Errno) {
	r1, _, errno := syscall.Syscall(syscall.SYS_IOCTL, c.f.Fd(), req, uintptr(unsafe.Pointer(cmd)))
	runtime.KeepAlive(data)
	return r1, errno
}

// Close closes the device.
func (c *Controller) Close() error {
	if c.f == nil {
		return nil
	}
	return c.f.Close()
}

// Path returns the device path.
func (c *Controller) Path() string { return c.path }

// dmaBuffer returns page-aligned memory outside the Go heap, so its address
// stays valid while the kernel uses it.
func dmaBuffer(n int) ([]byte, func(), error) {
	b, err := syscall.Mmap(-1, 0, n, syscall.PROT_READ|syscall.PROT_WRITE, syscall.MAP_ANON|syscall.MAP_PRIVATE)
	if err != nil {
		return nil, nil, fmt.Errorf("mmap %d bytes: %w", n, err)
	}
	return b, func() { _ = syscall.Munmap(b) }, nil
}

func (c *Controller) admin(op string, cmd *passthruCmd, data []byte) (uint32, error) {
	if len(data) > 0 {
		cmd.Addr = uint64(uintptr(unsafe.Pointer(&data[0])))
		cmd.DataLen = uint32(len(data))
	}
	if cmd.TimeoutMs == 0 {
		cmd.TimeoutMs = uint32(defaultTimeout / time.Millisecond)
	}
	r1, errno := c.ioctl(ioctlAdminCmd, cmd, data)
	if errno != 0 {
		return 0, fmt.Errorf("nvme %s on %s: %w", op, c.path, errno)
	}
	if r1 != 0 {
		return 0, &StatusError{Op: op, Status: uint32(r1)}
	}
	return cmd.Result, nil
}

// readCmd runs a command that returns size bytes and hands a copy to parse.
func (c *Controller) readCmd(op string, cmd *passthruCmd, size int) ([]byte, error) {
	buf, release, err := dmaBuffer(size)
	if err != nil {
		return nil, err
	}
	defer release()
	if _, err := c.admin(op, cmd, buf); err != nil {
		return nil, err
	}
	out := make([]byte, size)
	copy(out, buf)
	return out, nil
}

// IdentifyController issues Identify with CNS 01h.
func (c *Controller) IdentifyController() (*IdentifyController, error) {
	b, err := c.readCmd("identify-ctrl", &passthruCmd{Opcode: opIdentify, CDW10: 1}, 4096)
	if err != nil {
		return nil, err
	}
	return ParseIdentifyController(b)
}

// IdentifyNamespace issues Identify with CNS 00h.
func (c *Controller) IdentifyNamespace(nsid uint32) (*IdentifyNamespace, error) {
	b, err := c.readCmd("identify-ns", &passthruCmd{Opcode: opIdentify, NSID: nsid, CDW10: 0}, 4096)
	if err != nil {
		return nil, err
	}
	return ParseIdentifyNamespace(b)
}

func (c *Controller) getLog(op string, lid uint8, size int) ([]byte, error) {
	cdw10, cdw11 := GetLogPageCDW(lid, size)
	return c.readCmd(op, &passthruCmd{Opcode: opGetLogPage, NSID: NSIDAll, CDW10: cdw10, CDW11: cdw11}, size)
}

// SanitizeLog reads the Sanitize Status log page.
func (c *Controller) SanitizeLog() (*SanitizeLog, error) {
	b, err := c.getLog("sanitize-log", LogSanitizeStatus, 512)
	if err != nil {
		return nil, err
	}
	return ParseSanitizeLog(b)
}

// SmartLog reads the SMART / Health Information log page.
func (c *Controller) SmartLog() (*SmartLog, error) {
	b, err := c.getLog("smart-log", LogSmartHealth, 512)
	if err != nil {
		return nil, err
	}
	return ParseSmartLog(b)
}

// Sanitize starts a sanitize operation. The command completes once the
// operation has started; progress is reported in the Sanitize Status log.
func (c *Controller) Sanitize(action SanitizeAction, ause bool) error {
	_, err := c.admin("sanitize", &passthruCmd{Opcode: opSanitize, CDW10: SanitizeCDW10(action, ause)}, nil)
	return err
}

// Format issues Format NVM for nsid and waits up to timeout for completion.
func (c *Controller) Format(nsid uint32, spec FormatSpec, timeout time.Duration) error {
	cmd := &passthruCmd{Opcode: opFormatNVM, NSID: nsid, CDW10: spec.CDW10(), TimeoutMs: uint32(timeout / time.Millisecond)}
	_, err := c.admin("format", cmd, nil)
	return err
}

// SecurityReceive issues Security Receive (used for TCG Level 0 Discovery:
// SECP 01h, SPSP 0001h).
func (c *Controller) SecurityReceive(secp uint8, spsp uint16, size int) ([]byte, error) {
	cmd := &passthruCmd{Opcode: opSecurityRecv, CDW10: SecurityReceiveCDW10(secp, spsp), CDW11: uint32(size)}
	return c.readCmd("security-recv", cmd, size)
}

// Rescan asks the kernel to rescan the controller's namespaces, so block
// devices reflect a format or sanitize.
func (c *Controller) Rescan() error {
	if _, errno := c.ioctl(ioctlRescan, nil, nil); errno != 0 {
		return fmt.Errorf("nvme rescan on %s: %w", c.path, errno)
	}
	return nil
}

// NamespaceID returns the NSID behind a namespace block device
// (NVME_IOCTL_ID).
func NamespaceID(blockDev string) (uint32, error) {
	f, err := os.OpenFile(blockDev, os.O_RDONLY, 0)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	r1, _, errno := syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), ioctlID, 0)
	if errno != 0 {
		return 0, fmt.Errorf("NVME_IOCTL_ID on %s: %w", blockDev, errno)
	}
	return uint32(r1), nil
}

var _ Device = (*Controller)(nil)
