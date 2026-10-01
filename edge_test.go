// SPDX-License-Identifier: Apache-2.0

package cryptoerase

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rayy3535/cryptoerase/ata"
	"github.com/rayy3535/cryptoerase/blockdev"
	"github.com/rayy3535/cryptoerase/nvme"
)

// ------------------------------------------------------------- hooks ---

// nvmeHook overrides selected methods of a fakeNVMe.
type nvmeHook struct {
	*fakeNVMe
	identifyCtrl func() (*nvme.IdentifyController, error)
	identifyNS   func(uint32) (*nvme.IdentifyNamespace, error)
	sanitizeLog  func() (*nvme.SanitizeLog, error)
	smartLog     func() (*nvme.SmartLog, error)
	sanitize     func(nvme.SanitizeAction, bool) error
	rescan       func() error
}

func (n *nvmeHook) IdentifyController() (*nvme.IdentifyController, error) {
	if n.identifyCtrl != nil {
		return n.identifyCtrl()
	}
	return n.fakeNVMe.IdentifyController()
}

func (n *nvmeHook) IdentifyNamespace(nsid uint32) (*nvme.IdentifyNamespace, error) {
	if n.identifyNS != nil {
		return n.identifyNS(nsid)
	}
	return n.fakeNVMe.IdentifyNamespace(nsid)
}

func (n *nvmeHook) SanitizeLog() (*nvme.SanitizeLog, error) {
	if n.sanitizeLog != nil {
		return n.sanitizeLog()
	}
	return n.fakeNVMe.SanitizeLog()
}

func (n *nvmeHook) SmartLog() (*nvme.SmartLog, error) {
	if n.smartLog != nil {
		return n.smartLog()
	}
	return n.fakeNVMe.SmartLog()
}

func (n *nvmeHook) Sanitize(a nvme.SanitizeAction, ause bool) error {
	if n.sanitize != nil {
		return n.sanitize(a, ause)
	}
	return n.fakeNVMe.Sanitize(a, ause)
}

func (n *nvmeHook) Rescan() error {
	if n.rescan != nil {
		return n.rescan()
	}
	return n.fakeNVMe.Rescan()
}

// hookNVMe routes OpenNVMe through hooks; controllers without a hook use
// the plain fake.
func (h *testHost) hookNVMe(o *Options, hooks map[string]*nvmeHook) {
	base := o.OpenNVMe
	o.OpenNVMe = func(p string) (nvme.Device, error) {
		if hk, ok := hooks[filepath.Base(p)]; ok {
			h.opened()
			return hk, nil
		}
		return base(p)
	}
}

// ataHook overrides the fake ATA backend per device.
type ataHook struct {
	*fakeATA
	identify func(dev string) (*ata.Identify, error, bool)
	status   func(dev string) (*ata.SanitizeStatus, error, bool)
	scramble func(dev string) (error, bool)
}

func (a *ataHook) Identify(ctx context.Context, dev string) (*ata.Identify, error) {
	if a.identify != nil {
		if id, err, ok := a.identify(dev); ok {
			return id, err
		}
	}
	return a.fakeATA.Identify(ctx, dev)
}

func (a *ataHook) SanitizeStatus(ctx context.Context, dev string) (*ata.SanitizeStatus, error) {
	if a.status != nil {
		if st, err, ok := a.status(dev); ok {
			return st, err
		}
	}
	return a.fakeATA.SanitizeStatus(ctx, dev)
}

func (a *ataHook) SanitizeCryptoScramble(ctx context.Context, dev string) error {
	if a.scramble != nil {
		if err, ok := a.scramble(dev); ok {
			return err
		}
	}
	return a.fakeATA.SanitizeCryptoScramble(ctx, dev)
}

// faultyBlock injects I/O faults into marker handling.
type faultyBlock struct {
	blockdev.Device
	size                       int64
	sizeErr, syncErr, writeErr error
	readErr                    error
	corrupt                    bool
}

func (f *faultyBlock) Size() (int64, error) {
	if f.sizeErr != nil {
		return 0, f.sizeErr
	}
	if f.size > 0 {
		return f.size, nil
	}
	return f.Device.Size()
}

func (f *faultyBlock) Sync() error {
	if f.syncErr != nil {
		return f.syncErr
	}
	return f.Device.Sync()
}

func (f *faultyBlock) WriteAt(p []byte, off int64) (int, error) {
	if f.writeErr != nil {
		return 0, f.writeErr
	}
	return f.Device.WriteAt(p, off)
}

func (f *faultyBlock) ReadAt(p []byte, off int64) (int, error) {
	if f.readErr != nil {
		return 0, f.readErr
	}
	n, err := f.Device.ReadAt(p, off)
	if f.corrupt && n > 0 {
		p[0] ^= 0xff
	}
	return n, err
}

// openFaulty makes the n-th OpenBlock call (1 = markers before the erase,
// 2 = verification after it) return a faultyBlock built by fault, or fail
// with openErr.
func openFaulty(o *Options, n int32, openErr error, fault func(*faultyBlock)) {
	var calls atomic.Int32
	o.OpenBlock = func(p string, direct bool) (blockdev.Device, error) {
		// Regular files in t.TempDir() need not support O_DIRECT.
		d, err := blockdev.Open(p, false)
		if err != nil {
			return nil, err
		}
		if calls.Add(1) != n {
			return d, nil
		}
		if openErr != nil {
			d.Close()
			return nil, openErr
		}
		fb := &faultyBlock{Device: d}
		fault(fb)
		return fb, nil
	}
}

func wantResult(t *testing.T, rep *Report, name string, res Result, reason string) *DriveRecord {
	t.Helper()
	d := drive(t, rep, name)
	if d.Result != res || !strings.Contains(d.Reason, reason) {
		t.Errorf("%s: got %s %q, want %s containing %q", name, d.Result, d.Reason, res, reason)
	}
	return d
}

// ------------------------------------------------------------- tests ---

func TestOtherBlockDevices(t *testing.T) {
	h := newHost(t)
	for _, n := range []string{"loop0", "ram0", "zram0", "dm-0", "md127", "sr0", "nbd0", "fd0", "mmcblk0boot0", "mmcblk0rpmb", "nvme9c9n1"} {
		must(t, os.MkdirAll(filepath.Join(h.sys, "block", n), 0o755))
	}
	write(t, filepath.Join(h.sys, "block/vda/removable"), "0")
	write(t, filepath.Join(h.sys, "block/mmcblk0/removable"), "1")
	// A namespace not listed under /sys/class/nvme falls back to the
	// controller with the same instance number.
	write(t, filepath.Join(h.sys, "block/nvme3n1/size"), "1")
	rep := h.run(h.options(ModeInventory))
	if len(rep.Drives) != 3 {
		var names []string
		for _, d := range rep.Drives {
			names = append(names, d.Device)
		}
		t.Fatalf("drives %v", names)
	}
	wantResult(t, rep, "vda", Unhandled, "unsupported device type")
	wantResult(t, rep, "mmcblk0", Skipped, "removable")
	d := wantResult(t, rep, "nvme3", Fail, "open controller")
	if len(d.Namespaces) != 1 || filepath.Base(d.Namespaces[0]) != "nvme3n1" {
		t.Errorf("fallback namespaces %v", d.Namespaces)
	}
	if rep.Result != "FAIL" {
		t.Errorf("report result %s", rep.Result)
	}
}

func TestInUseThroughDeviceMapperAndSwap(t *testing.T) {
	h := newHost(t)
	n := h.addNVMe("nvme0", nvmeSpec{sanicap: 1})
	h.addSCSI("sdb", scsiSpec{vendor: "ATA", driver: "ahci", ata: &fakeATADisk{words: ataWords("M", "SWAP", "F", true, false)}})
	h.addNVMe("nvme1", nvmeSpec{sanicap: 1})

	// LVM root on nvme0n1p2: /dev/mapper/vg-root -> dm-0 -> slaves/nvme0n1p2.
	part := filepath.Join(h.sys, "block/nvme0n1/nvme0n1p2")
	write(t, filepath.Join(part, "partition"), "2")
	must(t, symlink(part, filepath.Join(h.sys, "class/block/nvme0n1p2")))
	must(t, os.MkdirAll(filepath.Join(h.sys, "class/block/dm-0/slaves/nvme0n1p2"), 0o755))
	must(t, os.MkdirAll(filepath.Join(h.dev, "mapper"), 0o755))
	write(t, filepath.Join(h.dev, "dm-0"), "")
	must(t, symlink("../dm-0", filepath.Join(h.dev, "mapper/vg-root")))
	appendLine(t, filepath.Join(h.proc, "mounts"), "tmpfs /run tmpfs rw 0 0")
	appendLine(t, filepath.Join(h.proc, "mounts"), "/dev/mapper/vg-root / ext4 rw 0 0")

	// Swap on sdb2.
	resolved, err := filepath.EvalSymlinks(filepath.Join(h.sys, "block/sdb"))
	must(t, err)
	write(t, filepath.Join(resolved, "sdb2/partition"), "2")
	must(t, symlink(filepath.Join(resolved, "sdb2"), filepath.Join(h.sys, "class/block/sdb2")))
	appendLine(t, filepath.Join(h.proc, "swaps"), "/dev/sdb2 partition 1048572 0 -2")

	rep := h.run(h.options(ModeErase))
	wantResult(t, rep, "nvme0", Unhandled, "in use")
	wantResult(t, rep, "sdb", Unhandled, "in use")
	wantResult(t, rep, "nvme1", Pass, "")
	if len(n.calls) != 0 {
		t.Errorf("in-use controller was touched: %v", n.calls)
	}
}

func TestRootMountedAsDevRoot(t *testing.T) {
	// The kernel reports the root filesystem as /dev/root, which does not
	// exist in /dev. The disk must still be found through mountinfo.
	h := newHost(t)
	h.addSCSI("sda", scsiSpec{vendor: "ATA", driver: "ahci", ata: &fakeATADisk{words: ataWords("M", "ROOT", "F", true, false)}})
	n := h.addNVMe("nvme0", nvmeSpec{sanicap: 1})
	h.addNVMe("nvme1", nvmeSpec{sanicap: 1})

	resolved, err := filepath.EvalSymlinks(filepath.Join(h.sys, "block/sda"))
	must(t, err)
	write(t, filepath.Join(resolved, "sda1/partition"), "1")
	must(t, symlink(filepath.Join(resolved, "sda1"), filepath.Join(h.sys, "class/block/sda1")))
	part := filepath.Join(h.sys, "block/nvme0n1/nvme0n1p1")
	write(t, filepath.Join(part, "partition"), "1")
	must(t, symlink(part, filepath.Join(h.sys, "class/block/nvme0n1p1")))
	must(t, os.MkdirAll(filepath.Join(h.sys, "dev/block"), 0o755))
	must(t, symlink(filepath.Join(resolved, "sda1"), filepath.Join(h.sys, "dev/block/8:1")))
	must(t, symlink(part, filepath.Join(h.sys, "dev/block/259:1")))

	appendLine(t, filepath.Join(h.proc, "mounts"), "/dev/root / ext4 rw 0 0")
	write(t, filepath.Join(h.proc, "self/mountinfo"), strings.Join([]string{
		"22 1 8:1 / / rw,relatime shared:1 - ext4 /dev/root rw",
		"23 22 0:21 / /proc rw - proc proc rw",
		"24 22 259:1 / /var rw - xfs /dev/disk/by-label/var rw",
		"25 22 7:3 / /snap/core rw - squashfs /dev/loop3 ro", // no sysfs entry: ignored
		"short line",
	}, "\n")+"\n")

	rep := h.run(h.options(ModeErase))
	wantResult(t, rep, "sda", Unhandled, "in use")
	wantResult(t, rep, "nvme0", Unhandled, "in use")
	wantResult(t, rep, "nvme1", Pass, "")
	if len(n.calls) != 0 {
		t.Errorf("in-use controller was touched: %v", n.calls)
	}
}

func TestNamespaceIDFallback(t *testing.T) {
	h := newHost(t)
	ok := h.addNVMe("nvme0", nvmeSpec{fna: 4})
	h.addNVMe("nvme1", nvmeSpec{fna: 4})
	must(t, os.Remove(filepath.Join(h.sys, "block/nvme0n1/nsid")))
	write(t, filepath.Join(h.sys, "block/nvme1n1/nsid"), "garbage")
	o := h.options(ModeErase)
	o.AllowFormat = true
	o.NamespaceID = func(p string) (uint32, error) {
		if filepath.Base(p) == "nvme0n1" {
			return 1, nil
		}
		return 0, errors.New("NVME_IOCTL_ID: inappropriate ioctl")
	}
	rep := h.run(o)
	wantResult(t, rep, "nvme0", Pass, "")
	wantResult(t, rep, "nvme1", Fail, "namespace id of nvme1n1")
	if ok.called("format nsid=1") != 1 {
		t.Errorf("calls %v", ok.calls)
	}
}

func TestOptionsValidation(t *testing.T) {
	bad := []Options{
		{Mode: ModeInventory, Samples: 1},
		{Mode: ModeInventory, PollInterval: -time.Second},
		{Mode: ModeInventory, NoProgressTimeout: -1},
		{Mode: ModeInventory, FormatTimeout: -1},
		{Mode: ModeInventory, NodeWait: -1},
		{Mode: ModeErase},
	}
	for i, o := range bad {
		if _, err := Run(context.Background(), o); err == nil {
			t.Errorf("case %d: no error", i)
		}
	}
	v, err := (&Options{Mode: ModeInventory}).withDefaults()
	if err != nil {
		t.Fatal(err)
	}
	if v.Samples != 16 || v.PollInterval != 10*time.Second || v.NoProgressTimeout != 20*time.Minute ||
		v.FormatTimeout != 10*time.Minute || v.NodeWait != 10*time.Second || v.SysfsRoot != "/sys" ||
		v.ProcRoot != "/proc" || v.DevRoot != "/dev" || v.Logger == nil || v.OpenNVMe == nil ||
		v.NamespaceID == nil || v.OpenBlock == nil || v.ATA == nil || v.PERC == nil ||
		len(v.FirmwarePolicy) != len(DefaultFirmwarePolicy()) {
		t.Errorf("defaults %+v", v)
	}
	// The resolved constructors must at least fail cleanly on missing paths.
	if _, err := v.OpenNVMe(filepath.Join(t.TempDir(), "nvme0")); err == nil {
		t.Error("OpenNVMe on missing path")
	}
	if _, err := v.OpenBlock(filepath.Join(t.TempDir(), "sda"), false); err == nil {
		t.Error("OpenBlock on missing path")
	}
	if ModeInventory.String() != "inventory" || ModeErase.String() != "erase" {
		t.Error("Mode.String")
	}
}

func TestNVMeDeviceErrors(t *testing.T) {
	h := newHost(t)
	fakes := map[string]*fakeNVMe{}
	for i, s := range []nvmeSpec{
		{sanicap: 1},                    // 0 identify controller fails
		{fna: 4},                        // 1 identify namespace fails
		{fna: 4, behaviour: "reject"},   // 2 format rejected
		{sanicap: 1},                    // 3 sanitize log unreadable before the erase
		{sanicap: 1},                    // 4 sanitize log unreadable after the erase
		{sanicap: 1},                    // 5 rescan fails: recorded, still PASS
		{sanicap: 1},                    // 6 namespace never comes back
		{sanicap: 1, behaviour: "fail"}, // 7 sanitize completes with failure
		{sanicap: 1},                    // 8 SMART unreadable: still PASS
		{sanicap: 1, oacs: 1, level0: []byte{0, 0, 0, 10}}, // 9 Level 0 header too short
		{sanicap: 1, oacs: 1},                              // 10 Security Receive fails
	} {
		name := fmt.Sprintf("nvme%d", i)
		fakes[name] = h.addNVMe(name, s)
	}
	boom := errors.New("boom")
	hooks := map[string]*nvmeHook{
		"nvme0": {fakeNVMe: fakes["nvme0"], identifyCtrl: func() (*nvme.IdentifyController, error) { return nil, boom }},
		"nvme1": {fakeNVMe: fakes["nvme1"], identifyNS: func(uint32) (*nvme.IdentifyNamespace, error) { return nil, boom }},
		"nvme3": {fakeNVMe: fakes["nvme3"], sanitizeLog: func() (*nvme.SanitizeLog, error) { return nil, boom }},
		"nvme5": {fakeNVMe: fakes["nvme5"], rescan: func() error { return boom }},
		"nvme6": {fakeNVMe: fakes["nvme6"], rescan: func() error { return os.Remove(fakes["nvme6"].nsFiles[1]) }},
		"nvme8": {fakeNVMe: fakes["nvme8"], smartLog: func() (*nvme.SmartLog, error) { return nil, boom }},
	}
	f4 := fakes["nvme4"]
	hooks["nvme4"] = &nvmeHook{fakeNVMe: f4, sanitizeLog: func() (*nvme.SanitizeLog, error) {
		if f4.called("sanitize 4") > 0 {
			return nil, boom
		}
		return f4.SanitizeLog()
	}}
	o := h.options(ModeErase)
	o.AllowFormat = true
	h.hookNVMe(&o, hooks)
	rep := h.run(o)

	wantResult(t, rep, "nvme0", Fail, "identify controller: boom")
	wantResult(t, rep, "nvme1", Fail, "identify namespace nvme1n1: boom")
	wantResult(t, rep, "nvme2", Fail, "format SES=010b failed on nvme2n1")
	wantResult(t, rep, "nvme3", Fail, "previous sanitize did not finish: boom")
	if fakes["nvme3"].called("sanitize 4") != 0 {
		t.Error("nvme3: sanitize issued although the status log was unreadable")
	}
	wantResult(t, rep, "nvme4", Fail, "sanitize status unreadable or stalled: boom")
	if d := wantResult(t, rep, "nvme5", Pass, ""); d.DeviceStatus == nil || d.DeviceStatus.RescanError != "boom" {
		t.Errorf("nvme5 device status %+v", d.DeviceStatus)
	}
	wantResult(t, rep, "nvme6", Fail, "nvme6n1 did not reappear after erase")
	wantResult(t, rep, "nvme7", Fail, "exit-failure-mode issued")
	if !fakes["nvme7"].exitFail {
		t.Error("nvme7: Exit Failure Mode not issued")
	}
	if d := wantResult(t, rep, "nvme8", Pass, ""); d.Health != nil {
		t.Errorf("nvme8 health %+v", d.Health)
	}
	if d := wantResult(t, rep, "nvme9", Pass, ""); d.TCG == nil || d.TCG.Error == "" || d.TCG.Level0 != nil {
		t.Errorf("nvme9 tcg %+v", d.TCG)
	}
	if d := wantResult(t, rep, "nvme10", Pass, ""); d.TCG == nil || !strings.Contains(d.TCG.Error, "not supported") {
		t.Errorf("nvme10 tcg %+v", d.TCG)
	}
}

func TestNVMeInterruptedWhileSanitizing(t *testing.T) {
	h := newHost(t)
	f := h.addNVMe("nvme0", nvmeSpec{sanicap: 1, behaviour: "stall"})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	o := h.options(ModeErase)
	o.NoProgressTimeout = time.Hour
	h.hookNVMe(&o, map[string]*nvmeHook{"nvme0": {fakeNVMe: f, sanitize: func(a nvme.SanitizeAction, ause bool) error {
		err := f.Sanitize(a, ause)
		cancel()
		return err
	}}})
	rep, err := Run(ctx, o)
	must(t, err)
	d := wantResult(t, rep, "nvme0", Fail, "interrupted while sanitizing")
	if d.DeviceStatus == nil || d.DeviceStatus.SSTAT != "0x0002" {
		t.Errorf("device status %+v", d.DeviceStatus)
	}
}

func TestPreviousSanitizeStillRunning(t *testing.T) {
	h := newHost(t)
	f := h.addNVMe("nvme0", nvmeSpec{sanicap: 1})
	f.pending = 3 // a sanitize from an earlier attempt is finishing
	rep := h.run(h.options(ModeErase))
	wantResult(t, rep, "nvme0", Pass, "")
	if f.called("sanitize-log") < 4 {
		t.Errorf("calls %v", f.calls)
	}
}

func TestSATAErrors(t *testing.T) {
	h := newHost(t)
	disk := func(serial string) *fakeATADisk {
		return &fakeATADisk{words: ataWords("EXAMPLE SATA", serial, "F", true, false)}
	}
	h.addSCSI("sda", scsiSpec{vendor: "ATA", driver: "ahci", ata: disk("NOHDPARM")})
	h.addSCSI("sdb", scsiSpec{vendor: "ATA", driver: "ahci", ata: disk("STATUSERR")})
	busy := disk("BUSY")
	busy.pending, busy.lastOK = 2, true
	h.addSCSI("sdc", scsiSpec{vendor: "ATA", driver: "ahci", ata: busy})
	h.addSCSI("sdd", scsiSpec{vendor: "ATA", driver: "ahci", ata: disk("STUCK")})
	h.addSCSI("sde", scsiSpec{vendor: "ATA", driver: "ahci", ata: disk("NOTOK")})
	h.addSCSI("sdf", scsiSpec{vendor: "ATA", driver: "ahci", ata: disk("LOSTSTATUS")})
	h.addSCSI("sdg", scsiSpec{vendor: "ATA", driver: "ahci", rotational: 1, ata: disk("HDD")})
	noCrypto := &fakeATADisk{words: ataWords("EXAMPLE SATA", "NOCRYPTO", "F", false, false)}
	h.addSCSI("sdh", scsiSpec{vendor: "ATA", driver: "ahci", ata: noCrypto})
	h.addSCSI("sdi", scsiSpec{vendor: "SEAGATE", model: "ST1200MM0099", driver: "mpt3sas"})
	h.addSCSI("sdj", scsiSpec{vendor: "ATA", driver: "ahci", ata: disk("EMPTY")})
	write(t, filepath.Join(h.sys, "block/sdj/size"), "0")
	h.addSCSI("sdk", scsiSpec{vendor: "ATA", driver: "ahci", usb: true, ata: disk("USB")})

	dev := func(n string) string { return filepath.Join(h.dev, n) }
	scrambled := func(n string) bool {
		d := h.ata.disks[dev(n)]
		d.mu.Lock()
		defer d.mu.Unlock()
		return slices.Contains(d.calls, "crypto-scramble")
	}
	hk := &ataHook{fakeATA: h.ata}
	hk.identify = func(d string) (*ata.Identify, error, bool) {
		if d == dev("sda") {
			return nil, fmt.Errorf("run hdparm: %w", exec.ErrNotFound), true
		}
		return nil, nil, false
	}
	hk.status = func(d string) (*ata.SanitizeStatus, error, bool) {
		switch d {
		case dev("sdb"):
			return nil, errors.New("SG_IO: bad sense data"), true
		case dev("sdd"):
			return &ata.SanitizeStatus{State: "SD2", InProgress: true, Progress: 0x1000}, nil, true
		case dev("sde"):
			if scrambled("sde") {
				return &ata.SanitizeStatus{State: "SD0"}, nil, true
			}
		case dev("sdf"):
			if scrambled("sdf") {
				return nil, errors.New("timeout"), true
			}
		}
		return nil, nil, false
	}
	h.ata.disks[dev("sde")].behaviour = "noop" // keep the disk unchanged
	o := h.options(ModeErase)
	o.ATA = hk
	rep := h.run(o)

	wantResult(t, rep, "sda", Fail, "ATA backend unavailable")
	wantResult(t, rep, "sdb", Fail, "sanitize status unreadable: SG_IO")
	wantResult(t, rep, "sdc", Pass, "")
	wantResult(t, rep, "sdd", Fail, "previous sanitize did not finish: progress stalled")
	wantResult(t, rep, "sde", Fail, "did not report 'Completed Without Error'")
	wantResult(t, rep, "sdf", Fail, "sanitize status unreadable or stalled: timeout")
	if d := wantResult(t, rep, "sdg", Unhandled, "rotational HDD"); d.MediaType != "HDD" {
		t.Errorf("sdg media %q", d.MediaType)
	}
	wantResult(t, rep, "sdh", Fail, "no ATA SANITIZE CRYPTO SCRAMBLE support")
	wantResult(t, rep, "sdi", Unhandled, "not a directly attached ATA device")
	wantResult(t, rep, "sdj", Skipped, "zero size")
	wantResult(t, rep, "sdk", Skipped, "USB")
	for _, n := range []string{"sda", "sdb", "sdd", "sdh", "sdk"} {
		if scrambled(n) {
			t.Errorf("%s was scrambled", n)
		}
	}
}

func TestSATAInterruptedWhileSanitizing(t *testing.T) {
	h := newHost(t)
	d := &fakeATADisk{words: ataWords("M", "S", "F", true, false)}
	h.addSCSI("sda", scsiSpec{vendor: "ATA", driver: "ahci", ata: d})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	hk := &ataHook{fakeATA: h.ata}
	hk.scramble = func(dev string) (error, bool) {
		err := h.ata.SanitizeCryptoScramble(ctx, dev)
		d.mu.Lock()
		d.pending = 1 << 20
		d.mu.Unlock()
		cancel()
		return err, true
	}
	o := h.options(ModeErase)
	o.ATA = hk
	rep, err := Run(ctx, o)
	must(t, err)
	wantResult(t, rep, "sda", Fail, "interrupted while sanitizing")
}

func TestMarkerFaults(t *testing.T) {
	boom := errors.New("boom")
	cases := []struct {
		name    string
		nth     int32
		openErr error
		fault   func(*faultyBlock)
		res     Result
		reason  string
		erased  bool // the erase command must (not) have been issued
	}{
		{"open", 1, boom, nil, Fail, "cannot write markers", false},
		{"size", 1, nil, func(f *faultyBlock) { f.sizeErr = boom }, Fail, "cannot write markers", false},
		{"small", 1, nil, func(f *faultyBlock) { f.size = 512 << 10 }, Fail, "smaller than 1 MiB", false},
		{"write", 1, nil, func(f *faultyBlock) { f.writeErr = boom }, Fail, "cannot write markers", false},
		{"sync", 1, nil, func(f *faultyBlock) { f.syncErr = boom }, Fail, "cannot write markers", false},
		{"readback-error", 1, nil, func(f *faultyBlock) { f.readErr = boom }, Fail, "marker read-back mismatch", false},
		{"readback-corrupt", 1, nil, func(f *faultyBlock) { f.corrupt = true }, Fail, "marker read-back mismatch", false},
		{"verify-open", 2, boom, nil, Fail, "16 marker region(s) unreadable", true},
		{"verify-read", 2, nil, func(f *faultyBlock) { f.readErr = boom }, Fail, "16 marker region(s) unreadable", true},
		{"verify-ok-mmap", 2, nil, func(f *faultyBlock) {}, Pass, "", true},
	}
	for _, tc := range cases {
		for _, kind := range []string{"nvme", "sata"} {
			t.Run(tc.name+"/"+kind, func(t *testing.T) {
				h := newHost(t)
				var f *fakeNVMe
				d := &fakeATADisk{words: ataWords("M", "S", "F", true, false)}
				name := "sda"
				if kind == "nvme" {
					f = h.addNVMe("nvme0", nvmeSpec{sanicap: 1})
					name = "nvme0"
				} else {
					h.addSCSI("sda", scsiSpec{vendor: "ATA", driver: "ahci", ata: d})
				}
				o := h.options(ModeErase)
				if tc.name == "verify-ok-mmap" {
					o.DisableDirectIO = false // exercise the mmap buffers
				}
				openFaulty(&o, tc.nth, tc.openErr, tc.fault)
				rep := h.run(o)
				wantResult(t, rep, name, tc.res, tc.reason)
				erased := slices.Contains(d.calls, "crypto-scramble")
				if f != nil {
					erased = f.called("sanitize 4") > 0
				}
				if erased != tc.erased {
					t.Errorf("erase issued = %v, want %v", erased, tc.erased)
				}
			})
		}
	}
}

func TestVerifiedResult(t *testing.T) {
	for _, tc := range []struct {
		v      verifyResult
		res    Result
		reason string
	}{
		{verifyResult{total: 4, changed: 4}, Pass, ""},
		{verifyResult{total: 4, changed: 3}, Fail, "1 of 4 markers unchanged"},
		{verifyResult{}, Fail, "0 of 0 markers unchanged"},
		{verifyResult{total: 4, changed: 3, unreadable: 1}, Fail, "1 marker region(s) unreadable"},
	} {
		res, reason := verifiedResult(tc.v)
		if res != tc.res || !strings.Contains(reason, tc.reason) {
			t.Errorf("%+v: %s %q", tc.v, res, reason)
		}
	}
}

func TestPERCListError(t *testing.T) {
	h := newHost(t)
	h.addSCSI("sda", scsiSpec{vendor: "DELL", model: "PERC H755 Front", driver: "megaraid_sas"})
	h.addSCSI("sdb", scsiSpec{vendor: "DELL", model: "PERC H755 Front", driver: "megaraid_sas"})
	h.perc.err = errors.New("perccli64: exit status 1")
	rep := h.run(h.options(ModeInventory))
	for _, n := range []string{"sda", "sdb"} {
		if d := wantResult(t, rep, n, Unhandled, "could not be listed"); d.PERC == nil || d.PERC.Error == "" {
			t.Errorf("%s perc %+v", n, d.PERC)
		}
	}
	if c := h.perc.calls.Load(); c != 1 {
		t.Errorf("PERC listed %d times, want once", c)
	}
}
