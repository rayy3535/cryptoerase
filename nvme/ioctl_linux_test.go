// SPDX-License-Identifier: Apache-2.0

//go:build linux

package nvme

import (
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
	"unsafe"
)

// sent is one recorded ioctl.
type sent struct {
	req  uintptr
	cmd  passthruCmd
	size int
}

// fakeController returns a Controller whose ioctl is answered by reply, which
// may fill data and returns the ioctl result.
func fakeController(t *testing.T, reply func(cmd *passthruCmd, data []byte) (uintptr, syscall.Errno)) (*Controller, *[]sent) {
	t.Helper()
	var log []sent
	c := &Controller{path: "/dev/nvme-test"}
	c.ioctl = func(req uintptr, cmd *passthruCmd, data []byte) (uintptr, syscall.Errno) {
		s := sent{req: req, size: len(data)}
		if cmd != nil {
			s.cmd = *cmd
			if len(data) > 0 && cmd.Addr != uint64(uintptr(unsafe.Pointer(&data[0]))) {
				t.Errorf("Addr %#x does not point at the data buffer", cmd.Addr)
			}
			if int(cmd.DataLen) != len(data) {
				t.Errorf("DataLen %d, buffer %d", cmd.DataLen, len(data))
			}
		}
		log = append(log, s)
		if reply == nil {
			return 0, 0
		}
		return reply(cmd, data)
	}
	return c, &log
}

func TestIdentifyControllerTransport(t *testing.T) {
	c, log := fakeController(t, func(cmd *passthruCmd, data []byte) (uintptr, syscall.Errno) {
		copy(data, idCtrlBuf())
		return 0, 0
	})
	id, err := c.IdentifyController()
	if err != nil {
		t.Fatal(err)
	}
	if id.Model != "Dell Ent NVMe v2 AGN MU U.2 6.4TB" {
		t.Errorf("model %q", id.Model)
	}
	s := (*log)[0]
	if s.req != ioctlAdminCmd || s.cmd.Opcode != opIdentify || s.cmd.CDW10 != 1 || s.size != 4096 {
		t.Errorf("sent %+v", s)
	}
	if s.cmd.TimeoutMs != uint32(defaultTimeout/time.Millisecond) {
		t.Errorf("default timeout %d", s.cmd.TimeoutMs)
	}
}

func TestIdentifyNamespaceTransport(t *testing.T) {
	c, log := fakeController(t, func(cmd *passthruCmd, data []byte) (uintptr, syscall.Errno) {
		data[25], data[26], data[128+2] = 0, 0, 9
		return 0, 0
	})
	ns, err := c.IdentifyNamespace(3)
	if err != nil || ns.BlockSize() != 512 {
		t.Fatalf("%v %+v", err, ns)
	}
	if s := (*log)[0]; s.cmd.NSID != 3 || s.cmd.CDW10 != 0 || s.cmd.Opcode != opIdentify {
		t.Errorf("sent %+v", s)
	}
}

func TestLogPagesTransport(t *testing.T) {
	c, log := fakeController(t, func(cmd *passthruCmd, data []byte) (uintptr, syscall.Errno) {
		switch cmd.CDW10 & 0xff {
		case LogSanitizeStatus:
			binary.LittleEndian.PutUint16(data[2:], 0x0101)
		case LogSmartHealth:
			data[0], data[3] = 0x04, 87
		}
		return 0, 0
	})
	l, err := c.SanitizeLog()
	if err != nil || l.Status() != SanitizeSucceeded || !l.GlobalDataErased() {
		t.Fatalf("%v %+v", err, l)
	}
	sm, err := c.SmartLog()
	if err != nil || sm.CriticalWarning != 4 || sm.AvailableSpare != 87 {
		t.Fatalf("%v %+v", err, sm)
	}
	for i, lid := range []uint32{LogSanitizeStatus, LogSmartHealth} {
		s := (*log)[i]
		if s.cmd.Opcode != opGetLogPage || s.cmd.NSID != NSIDAll || s.cmd.CDW10 != lid|127<<16 || s.cmd.CDW11 != 0 || s.size != 512 {
			t.Errorf("log %#x sent %+v", lid, s.cmd)
		}
	}
}

func TestSanitizeFormatRescanTransport(t *testing.T) {
	c, log := fakeController(t, nil)
	if err := c.Sanitize(SanitizeCryptoErase, true); err != nil {
		t.Fatal(err)
	}
	spec := FormatSpec{LBAF: 0x12, PI: 1, SES: SESCryptoErase}
	if err := c.Format(7, spec, 3*time.Minute); err != nil {
		t.Fatal(err)
	}
	if err := c.Rescan(); err != nil {
		t.Fatal(err)
	}
	l := *log
	if l[0].cmd.Opcode != opSanitize || l[0].cmd.CDW10 != 0xc || l[0].size != 0 || l[0].cmd.Addr != 0 {
		t.Errorf("sanitize sent %+v", l[0])
	}
	if l[1].cmd.Opcode != opFormatNVM || l[1].cmd.NSID != 7 || l[1].cmd.CDW10 != spec.CDW10() || l[1].cmd.TimeoutMs != 180000 {
		t.Errorf("format sent %+v", l[1])
	}
	if l[2].req != ioctlRescan {
		t.Errorf("rescan req %#x", l[2].req)
	}
}

func TestSecurityReceiveTransport(t *testing.T) {
	c, log := fakeController(t, func(cmd *passthruCmd, data []byte) (uintptr, syscall.Errno) {
		copy(data, []byte{0, 0, 0, 44})
		return 0, 0
	})
	b, err := c.SecurityReceive(1, 1, 2048)
	if err != nil || len(b) != 2048 || b[3] != 44 {
		t.Fatalf("%v len=%d", err, len(b))
	}
	if s := (*log)[0]; s.cmd.Opcode != opSecurityRecv || s.cmd.CDW10 != 0x01000100 || s.cmd.CDW11 != 2048 {
		t.Errorf("sent %+v", s.cmd)
	}
}

func TestTransportErrors(t *testing.T) {
	c, _ := fakeController(t, func(cmd *passthruCmd, data []byte) (uintptr, syscall.Errno) {
		if cmd == nil {
			return 0, syscall.ENOTTY
		}
		if cmd.Opcode == opSanitize {
			return 0x401d, 0 // DNR + Sanitize In Progress
		}
		return 0, syscall.EPERM
	})
	err := c.Sanitize(SanitizeCryptoErase, true)
	var se *StatusError
	if !errors.As(err, &se) || se.SC() != 0x1d || !se.DNR() {
		t.Fatalf("status error %v", err)
	}
	if _, err := c.IdentifyController(); !errors.Is(err, syscall.EPERM) {
		t.Fatalf("errno error %v", err)
	}
	if err := c.Rescan(); !errors.Is(err, syscall.ENOTTY) {
		t.Fatalf("rescan error %v", err)
	}
}

func TestOpenControllerAndNamespaceIDOnRegularFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "nvme0")
	if err := os.WriteFile(p, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := OpenController(p)
	if err != nil {
		t.Fatal(err)
	}
	if c.Path() != p {
		t.Errorf("path %q", c.Path())
	}
	// A regular file is not an NVMe device: the real ioctl must fail cleanly.
	if _, err := c.IdentifyController(); !errors.Is(err, syscall.ENOTTY) {
		t.Errorf("identify on regular file: %v", err)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := NamespaceID(p); !errors.Is(err, syscall.ENOTTY) {
		t.Errorf("NamespaceID on regular file: %v", err)
	}
	if _, err := OpenController(filepath.Join(t.TempDir(), "missing")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("open missing: %v", err)
	}
	if _, err := NamespaceID(filepath.Join(t.TempDir(), "missing")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("NamespaceID missing: %v", err)
	}
}

func TestDMABufferAligned(t *testing.T) {
	b, release, err := dmaBuffer(4096)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if len(b) != 4096 || uintptr(unsafe.Pointer(&b[0]))%4096 != 0 {
		t.Fatalf("len %d addr %p", len(b), &b[0])
	}
}
