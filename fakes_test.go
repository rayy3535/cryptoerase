// SPDX-License-Identifier: Apache-2.0

package cryptoerase

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rayy3535/cryptoerase/ata"
	"github.com/rayy3535/cryptoerase/nvme"
	"github.com/rayy3535/cryptoerase/perc"
)

const diskSize = 64 << 20

// ---------------------------------------------------------------- host ---

type testHost struct {
	t     *testing.T
	sys   string
	proc  string
	dev   string
	nvmes map[string]*fakeNVMe
	ata   *fakeATA
	perc  *fakePERC
	// concurrency probe
	active, peak atomic.Int32
}

func newHost(t *testing.T) *testHost {
	t.Helper()
	root := t.TempDir()
	h := &testHost{
		t: t, sys: filepath.Join(root, "sys"), proc: filepath.Join(root, "proc"), dev: filepath.Join(root, "dev"),
		nvmes: map[string]*fakeNVMe{}, ata: &fakeATA{disks: map[string]*fakeATADisk{}}, perc: &fakePERC{},
	}
	for _, d := range []string{"sys/class/nvme", "sys/block", "sys/class/block", "sys/class/scsi_host", "sys/class/dmi/id", "proc/sys/kernel", "dev"} {
		must(t, os.MkdirAll(filepath.Join(root, d), 0o755))
	}
	write(t, filepath.Join(h.proc, "mounts"), "proc /proc proc rw 0 0\n")
	write(t, filepath.Join(h.proc, "swaps"), "Filename Type Size Used Priority\n")
	write(t, filepath.Join(h.proc, "sys/kernel/osrelease"), "6.8.0-test\n")
	write(t, filepath.Join(h.sys, "class/dmi/id/sys_vendor"), "Dell Inc.\n")
	write(t, filepath.Join(h.sys, "class/dmi/id/product_name"), "PowerEdge R7525\n")
	write(t, filepath.Join(h.sys, "class/dmi/id/product_serial"), "TAG1234\n")
	return h
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func write(t *testing.T, path, s string) {
	t.Helper()
	must(t, os.MkdirAll(filepath.Dir(path), 0o755))
	must(t, os.WriteFile(path, []byte(s), 0o644))
}

func (h *testHost) makeDisk(name string) string {
	p := filepath.Join(h.dev, name)
	f, err := os.Create(p)
	must(h.t, err)
	must(h.t, f.Truncate(diskSize))
	must(h.t, f.Close())
	return p
}

type nvmeSpec struct {
	sanicap   uint32
	fna       uint8
	behaviour string // ok | noop | fail | stall | reject
	model, fw string
	unvmcap   uint64
	oacs      uint16
	level0    []byte
	multipath string // when set, the namespace head is this name (nvme5n1) and the class entry a path device
}

// addNVMe creates a controller with one namespace (nsid 1).
func (h *testHost) addNVMe(ctrl string, s nvmeSpec) *fakeNVMe {
	if s.behaviour == "" {
		s.behaviour = "ok"
	}
	if s.model == "" {
		s.model = "MOCK NVME " + ctrl
	}
	if s.fw == "" {
		s.fw = "FW1"
	}
	ns := ctrl + "n1"
	classEntry := ns
	if s.multipath != "" { // head nvme5n1 -> path device nvme5c<ctrl>n1 under the controller
		ns = s.multipath
		classEntry = strings.Replace(ns, "n1", "c"+strings.TrimPrefix(ctrl, "nvme")+"n1", 1)
	}
	must(h.t, os.MkdirAll(filepath.Join(h.sys, "class/nvme", ctrl, classEntry), 0o755))
	write(h.t, filepath.Join(h.sys, "block", ns, "size"), fmt.Sprint(diskSize/512))
	write(h.t, filepath.Join(h.sys, "block", ns, "removable"), "0")
	write(h.t, filepath.Join(h.sys, "block", ns, "nsid"), "1")
	path := h.makeDisk(ns)
	f := &fakeNVMe{
		host: h, spec: s, nsFiles: map[uint32]string{1: path},
		idns: &nvme.IdentifyNamespace{NLBAF: 18, FLBAS: 0x22, DPS: 0x01, LBAF: make([]nvme.LBAFormat, 19)},
	}
	h.nvmes[filepath.Join(h.dev, ctrl)] = f
	return f
}

type scsiSpec struct {
	vendor, model, driver string
	rotational, removable int
	usb                   bool
	ata                   *fakeATADisk // nil: IDENTIFY fails
}

func (h *testHost) addSCSI(name string, s scsiSpec) {
	if s.model == "" {
		s.model = "MOCKMODEL"
	}
	host := "host" + fmt.Sprint(len(name)*7+int(name[len(name)-1]))
	devpath := filepath.Join(h.sys, "devices/pci0000:00", host, "target0:0:0", "0:0:0:"+name)
	if s.usb {
		devpath = filepath.Join(h.sys, "devices/pci0000:00/usb1", host, "target0:0:0", "0:0:0:"+name)
	}
	blk := filepath.Join(devpath, "block", name)
	write(h.t, filepath.Join(devpath, "vendor"), fmt.Sprintf("%-8s\n", s.vendor))
	write(h.t, filepath.Join(devpath, "model"), s.model+"\n")
	write(h.t, filepath.Join(blk, "queue/rotational"), fmt.Sprint(s.rotational))
	write(h.t, filepath.Join(blk, "removable"), fmt.Sprint(s.removable))
	write(h.t, filepath.Join(blk, "size"), fmt.Sprint(diskSize/512))
	must(h.t, os.Symlink(devpath, filepath.Join(blk, "device")))
	write(h.t, filepath.Join(h.sys, "class/scsi_host", host, "proc_name"), s.driver+"\n")
	must(h.t, os.Symlink(blk, filepath.Join(h.sys, "block", name)))
	must(h.t, os.Symlink(blk, filepath.Join(h.sys, "class/block", name)))
	path := h.makeDisk(name)
	if s.ata != nil {
		s.ata.path = path
		s.ata.host = h
		h.ata.disks[path] = s.ata
	}
}

// mount marks partition <disk>1 as mounted on /.
func (h *testHost) mount(disk string) {
	real, err := filepath.EvalSymlinks(filepath.Join(h.sys, "block", disk))
	must(h.t, err)
	part := filepath.Join(real, disk+"1")
	write(h.t, filepath.Join(part, "partition"), "1")
	must(h.t, os.Symlink(part, filepath.Join(h.sys, "class/block", disk+"1")))
	f, err := os.OpenFile(filepath.Join(h.proc, "mounts"), os.O_APPEND|os.O_WRONLY, 0)
	must(h.t, err)
	fmt.Fprintf(f, "/dev/%s1 / ext4 rw 0 0\n", disk)
	f.Close()
}

func (h *testHost) options(mode Mode) Options {
	return Options{
		Mode: mode, Confirm: mode == ModeErase,
		SysfsRoot: h.sys, ProcRoot: h.proc, DevRoot: h.dev,
		DisableDirectIO:   true,
		PollInterval:      time.Millisecond,
		NoProgressTimeout: 50 * time.Millisecond,
		NodeWait:          100 * time.Millisecond,
		Logger:            slog.New(slog.NewTextHandler(io.Discard, nil)),
		OpenNVMe: func(p string) (nvme.Device, error) {
			if f, ok := h.nvmes[p]; ok {
				h.opened()
				return f, nil
			}
			return nil, fmt.Errorf("no such controller %s", p)
		},
		NamespaceID: func(string) (uint32, error) { return 0, errors.New("unexpected ioctl") },
		ATA:         h.ata,
		PERC:        h.perc,
	}
}

func (h *testHost) run(o Options) *Report {
	h.t.Helper()
	rep, err := Run(context.Background(), o)
	must(h.t, err)
	return rep
}

func (h *testHost) sum(name string) [32]byte {
	b, err := os.ReadFile(filepath.Join(h.dev, name))
	must(h.t, err)
	return sha256.Sum256(b)
}

func drive(t *testing.T, rep *Report, name string) *DriveRecord {
	t.Helper()
	for _, d := range rep.Drives {
		if filepath.Base(d.Device) == name {
			return d
		}
	}
	t.Fatalf("drive %s not in report", name)
	return nil
}

func symlink(oldname, newname string) error { return os.Symlink(oldname, newname) }

func appendLine(t *testing.T, path, line string) {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	must(t, err)
	defer f.Close()
	_, err = f.WriteString(line + "\n")
	must(t, err)
}

func fill(t *testing.T, path string, zero bool) {
	b := make([]byte, diskSize)
	if !zero {
		_, _ = rand.Read(b)
	}
	must(t, os.WriteFile(path, b, 0o644))
}

// ---------------------------------------------------------------- NVMe ---

type fakeNVMe struct {
	host    *testHost
	spec    nvmeSpec
	nsFiles map[uint32]string
	idns    *nvme.IdentifyNamespace

	mu       sync.Mutex
	calls    []string
	pending  int
	sstat    uint16
	sprog    uint16
	exitFail bool
	formats  []nvme.FormatSpec
}

func (f *fakeNVMe) record(c string) {
	f.mu.Lock()
	f.calls = append(f.calls, c)
	f.mu.Unlock()
}

func (f *fakeNVMe) called(prefix string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if strings.HasPrefix(c, prefix) {
			n++
		}
	}
	return n
}

func (f *fakeNVMe) IdentifyController() (*nvme.IdentifyController, error) {
	f.record("identify-ctrl")
	return &nvme.IdentifyController{
		Serial: "SN-" + filepath.Base(f.nsFiles[1]), Model: f.spec.model, Firmware: f.spec.fw,
		Version: 0x00010300, OACS: f.spec.oacs | 0x2,
		TNVMCAP: nvme.Uint128{Lo: diskSize}, UNVMCAP: nvme.Uint128{Lo: f.spec.unvmcap},
		SANICAP: f.spec.sanicap, NN: 1, FNA: f.spec.fna,
	}, nil
}

func (f *fakeNVMe) IdentifyNamespace(nsid uint32) (*nvme.IdentifyNamespace, error) {
	f.record("identify-ns")
	return f.idns, nil
}

func (f *fakeNVMe) SanitizeLog() (*nvme.SanitizeLog, error) {
	f.record("sanitize-log")
	if f.spec.sanicap&7 == 0 {
		return nil, &nvme.StatusError{Op: "sanitize-log", Status: 0x4109} // invalid log page
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.pending > 0 {
		if f.spec.behaviour != "stall" {
			f.pending--
		}
		prog := uint16(1000)
		if f.spec.behaviour != "stall" {
			prog = uint16(65535 - f.pending*1000)
		}
		return &nvme.SanitizeLog{SPROG: prog, SSTAT: nvme.SanitizeInProgress, ETCE: 30}, nil
	}
	return &nvme.SanitizeLog{SPROG: f.sprog, SSTAT: f.sstat, ETCE: 30}, nil
}

func (f *fakeNVMe) SmartLog() (*nvme.SmartLog, error) {
	return &nvme.SmartLog{AvailableSpare: 100, AvailableSpareThreshold: 10}, nil
}

func (f *fakeNVMe) Sanitize(a nvme.SanitizeAction, ause bool) error {
	f.record(fmt.Sprintf("sanitize %d ause=%v", a, ause))
	if a == nvme.SanitizeExitFailureMode {
		f.exitFail = true
		return nil
	}
	time.Sleep(5 * time.Millisecond)
	f.mu.Lock()
	defer f.mu.Unlock()
	switch f.spec.behaviour {
	case "ok":
		for _, p := range f.nsFiles {
			fill(f.host.t, p, true)
		}
		f.sstat, f.sprog, f.pending = 0x101, 0xffff, 2
	case "noop":
		f.sstat, f.sprog, f.pending = 0x101, 0xffff, 1
	case "fail":
		f.sstat, f.sprog, f.pending = nvme.SanitizeFailed, 0xffff, 1
	case "stall":
		f.pending = 1
	case "reject":
		return &nvme.StatusError{Op: "sanitize", Status: 0x4002}
	}
	return nil
}

func (f *fakeNVMe) Format(nsid uint32, spec nvme.FormatSpec, timeout time.Duration) error {
	f.record(fmt.Sprintf("format nsid=%d cdw10=%#x", nsid, spec.CDW10()))
	f.mu.Lock()
	f.formats = append(f.formats, spec)
	f.mu.Unlock()
	if f.spec.behaviour == "reject" {
		return &nvme.StatusError{Op: "format", Status: 0x410a}
	}
	if f.spec.behaviour != "noop" {
		fill(f.host.t, f.nsFiles[nsid], false)
	}
	return nil
}

func (f *fakeNVMe) SecurityReceive(secp uint8, spsp uint16, size int) ([]byte, error) {
	f.record("security-recv")
	if f.spec.level0 == nil {
		return nil, errors.New("not supported")
	}
	b := make([]byte, size)
	copy(b, f.spec.level0)
	return b, nil
}

func (f *fakeNVMe) Rescan() error { f.record("rescan"); return nil }
func (f *fakeNVMe) Close() error  { f.host.active.Add(-1); return nil }

func (h *testHost) opened() {
	cur := h.active.Add(1)
	for {
		p := h.peak.Load()
		if cur <= p || h.peak.CompareAndSwap(p, cur) {
			return
		}
	}
}

// level0Locking builds a Level 0 Discovery response with a Locking feature.
func level0Locking(flags byte) []byte {
	b := make([]byte, 48)
	b = append(b, 0x02, 0x03, 0x10, 16) // Opal 2 feature
	b = append(b, make([]byte, 16)...)
	b = append(b, 0x00, 0x02, 0x10, 12, flags) // Locking feature
	b = append(b, make([]byte, 11)...)
	binary.BigEndian.PutUint32(b[0:4], uint32(len(b)-4))
	return b
}

// ----------------------------------------------------------------- ATA ---

type fakeATADisk struct {
	host      *testHost
	path      string
	words     []uint16
	behaviour string // ok | reject | noop
	frozen    bool

	mu      sync.Mutex
	pending int
	lastOK  bool
	calls   []string
}

type fakeATA struct{ disks map[string]*fakeATADisk }

func ataWords(model, serial, fw string, crypto, locked bool) []uint16 {
	w := make([]uint16, 256)
	put := func(first, n int, s string) {
		b := []byte(s)
		for len(b) < 2*n {
			b = append(b, ' ')
		}
		for i := range n {
			w[first+i] = uint16(b[2*i])<<8 | uint16(b[2*i+1])
		}
	}
	put(10, 10, serial)
	put(23, 4, fw)
	put(27, 20, model)
	w[59] = 0x9000 // sanitize feature set + block erase
	if crypto {
		w[59] |= 0x2000
	}
	w[128] = 0x0009 // supported, frozen
	if locked {
		w[128] |= 0x0006
	}
	w[217] = 1
	return w
}

func (a *fakeATA) disk(dev string) (*fakeATADisk, error) {
	d, ok := a.disks[dev]
	if !ok {
		return nil, &ata.HdparmError{Args: []string{dev}, Err: errors.New("exit status 5"), Stderr: "SG_IO: bad/missing sense data"}
	}
	return d, nil
}

func (a *fakeATA) Identify(ctx context.Context, dev string) (*ata.Identify, error) {
	d, err := a.disk(dev)
	if err != nil {
		return nil, err
	}
	return ata.FromWords(d.words)
}

func (a *fakeATA) SanitizeStatus(ctx context.Context, dev string) (*ata.SanitizeStatus, error) {
	d, err := a.disk(dev)
	if err != nil {
		return nil, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.calls = append(d.calls, "status")
	if d.frozen {
		return &ata.SanitizeStatus{State: "SD1", Frozen: true}, nil
	}
	if d.pending > 0 {
		d.pending--
		return &ata.SanitizeStatus{State: "SD2", InProgress: true, Progress: uint16(0xffff - d.pending*0x1000)}, nil
	}
	return &ata.SanitizeStatus{State: "SD0", CompletedWithoutError: d.lastOK}, nil
}

func (a *fakeATA) SanitizeCryptoScramble(ctx context.Context, dev string) error {
	d, err := a.disk(dev)
	if err != nil {
		return err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.calls = append(d.calls, "crypto-scramble")
	switch d.behaviour {
	case "reject":
		return &ata.HdparmError{Args: []string{dev}, Err: errors.New("exit status 5"), Stderr: "SANITIZE device error reason: Device in FROZEN state"}
	case "noop":
		d.lastOK, d.pending = true, 1
	default:
		fill(d.host.t, d.path, false)
		d.lastOK, d.pending = true, 2
	}
	return nil
}

func (a *fakeATA) Version(ctx context.Context) string { return "hdparm v9.65 (fake)" }

// ---------------------------------------------------------------- PERC ---

type fakePERC struct {
	drives []perc.Drive
	err    error
	calls  atomic.Int32
}

func (p *fakePERC) List(ctx context.Context) ([]perc.Drive, error) {
	p.calls.Add(1)
	return p.drives, p.err
}
