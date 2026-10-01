// SPDX-License-Identifier: Apache-2.0

//go:build integration && linux

// Integration tests run against the resolved kernel. They need root and are
// opt-in:
//
//	go test -c -tags integration -o integration.test . && sudo ./integration.test -test.v
//
// They only ever write to a loop device backed by a temporary file. The
// host's own drives are touched read-only (inventory mode, Identify).

package cryptoerase

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"

	"github.com/rayy3535/cryptoerase/blockdev"
	"github.com/rayy3535/cryptoerase/nvme"
)

func needRoot(t *testing.T) {
	t.Helper()
	if os.Geteuid() != 0 {
		t.Skip("needs root")
	}
}

// loopDevice attaches a 64 MiB file to a loop device with direct I/O.
func loopDevice(t *testing.T) string {
	t.Helper()
	needRoot(t)
	if _, err := exec.LookPath("losetup"); err != nil {
		t.Skip("losetup not installed")
	}
	backing := filepath.Join(t.TempDir(), "loop.img")
	f, err := os.Create(backing)
	must(t, err)
	must(t, f.Truncate(64<<20))
	must(t, f.Close())
	out, err := exec.Command("losetup", "--find", "--show", "--direct-io=on", backing).CombinedOutput()
	if err != nil {
		// Some kernels or filesystems refuse direct I/O on the backing file.
		out, err = exec.Command("losetup", "--find", "--show", backing).CombinedOutput()
	}
	if err != nil {
		t.Skipf("losetup: %v: %s", err, out)
	}
	dev := strings.TrimSpace(string(out))
	t.Cleanup(func() { _ = exec.Command("losetup", "-d", dev).Run() })
	return dev
}

func TestIntegrationBlockdevOnLoop(t *testing.T) {
	dev := loopDevice(t)
	d, err := blockdev.Open(dev, true)
	must(t, err)
	defer d.Close()
	size, err := d.Size()
	if err != nil || size != 64<<20 {
		t.Fatalf("BLKGETSIZE64: %d %v", size, err)
	}
	buf, release, err := blockdev.Buffer(1 << 20)
	must(t, err)
	defer release()
	for i := range buf {
		buf[i] = byte(i * 7)
	}
	_, err = d.WriteAt(buf, 5<<20)
	must(t, err)
	must(t, d.Sync())
	must(t, d.FlushBuffers())
	rb, release2, err := blockdev.Buffer(1 << 20)
	must(t, err)
	defer release2()
	_, err = d.ReadAt(rb, 5<<20)
	must(t, err)
	if !bytes.Equal(buf, rb) {
		t.Fatal("O_DIRECT read-back differs")
	}
}

func TestIntegrationMarkersOnLoop(t *testing.T) {
	dev := loopDevice(t)
	o, err := (&Options{Mode: ModeInventory, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}).withDefaults()
	must(t, err)
	r := &runner{opts: o, log: o.Logger}

	m, err := r.writeMarkers(dev)
	must(t, err)
	defer m.release()
	if len(m.offsets) != 16 || m.offsets[15] != 63<<20 {
		t.Fatalf("offsets %v", m.offsets)
	}
	// Nothing happened yet: every marker is intact.
	v, err := r.verifyMarkers(dev, m)
	must(t, err)
	if v.total != 16 || v.changed != 0 || v.unreadable != 0 {
		t.Fatalf("before: %+v", v)
	}
	// Simulate a cryptographic erase that returns zeroes (deallocated blocks).
	must(t, os.WriteFile(dev, make([]byte, 64<<20), 0))
	v, err = r.verifyMarkers(dev, m)
	must(t, err)
	if v.changed != 16 || v.zero != 16 {
		t.Fatalf("after: %+v", v)
	}
	if res, _ := verifiedResult(v); res != Pass {
		t.Fatal(res)
	}
}

func TestIntegrationInventoryOnHost(t *testing.T) {
	needRoot(t)
	var logs bytes.Buffer
	rep, err := Run(context.Background(), Options{Mode: ModeInventory, Logger: slog.New(slog.NewTextHandler(&logs, nil))})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("result %s counts %v\n%s", rep.Result, rep.Counts, logs.String())
	if rep.Schema != Schema || rep.Mode != "inventory" || rep.Host.Kernel == "" || rep.FinishedAt.Before(rep.StartedAt) {
		t.Fatalf("report header %+v", rep)
	}
	for _, d := range rep.Drives {
		t.Logf("%-14s %-9s %-40s %s", d.Device, d.Result, d.Model, d.Reason)
		switch d.Result {
		case Planned, Unhandled, Skipped, Fail:
		default:
			t.Errorf("%s: result %s in inventory mode", d.Device, d.Result)
		}
		if d.DeviceStatus != nil || d.Verification != nil {
			t.Errorf("%s: inventory mode touched the device", d.Device)
		}
	}

	// The disk holding / must never be planned for erase.
	var st syscall.Stat_t
	must(t, syscall.Stat("/", &st))
	major := (st.Dev>>8)&0xfff | (st.Dev>>32)&^0xfff
	minor := st.Dev&0xff | (st.Dev>>12)&^0xff
	if major == 0 {
		t.Logf("/ is not on a block device (%d:%d)", major, minor)
		return
	}
	resolved, err := filepath.EvalSymlinks(fmt.Sprintf("/sys/dev/block/%d:%d", major, minor))
	if err != nil {
		t.Logf("/ device %d:%d not in sysfs: %v", major, minor, err)
		return
	}
	r := &runner{opts: Options{SysfsRoot: "/sys"}}
	bases := r.resolveBase(filepath.Base(resolved), 0)
	t.Logf("/ is on %v", bases)
	for _, d := range rep.Drives {
		for _, b := range bases {
			name := filepath.Base(d.Device)
			if name == b || slices.Contains(d.Namespaces, "/dev/"+b) {
				if d.Result != Unhandled || !strings.Contains(d.Reason, "in use") {
					t.Errorf("%s holds / but is %s %q", d.Device, d.Result, d.Reason)
				}
			}
		}
	}
}

func TestIntegrationNVMeIdentify(t *testing.T) {
	needRoot(t)
	ctrls, _ := filepath.Glob("/dev/nvme[0-9]")
	ctrls = append(ctrls, func() []string { m, _ := filepath.Glob("/dev/nvme[0-9][0-9]"); return m }()...)
	if len(ctrls) == 0 {
		t.Skip("no NVMe controller")
	}
	for _, p := range ctrls {
		c, err := nvme.OpenController(p)
		if err != nil {
			t.Errorf("%s: %v", p, err)
			continue
		}
		id, err := c.IdentifyController()
		if err != nil {
			t.Errorf("%s identify: %v", p, err)
		} else {
			t.Logf("%s: %q fw %q ver %s sanicap %#x fna %#x oacs %#x", p, id.Model, id.Firmware, id.VersionString(), id.SANICAP, id.FNA, id.OACS)
			if id.Model == "" {
				t.Errorf("%s: empty model", p)
			}
		}
		if id != nil && id.SanitizeAny() {
			if l, err := c.SanitizeLog(); err != nil {
				t.Errorf("%s sanitize log: %v", p, err)
			} else {
				t.Logf("%s: sstat %#x sprog %#x", p, l.SSTAT, l.SPROG)
			}
		}
		if _, err := c.SmartLog(); err != nil {
			t.Errorf("%s smart log: %v", p, err)
		}
		c.Close()
	}
	nss, _ := filepath.Glob("/dev/nvme*n[0-9]")
	for _, ns := range nss {
		if strings.Contains(filepath.Base(ns), "c") {
			continue
		}
		if id, err := nvme.NamespaceID(ns); err != nil || id == 0 {
			t.Errorf("%s: nsid %d %v", ns, id, err)
		}
	}
}
