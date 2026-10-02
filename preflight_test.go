// SPDX-License-Identifier: Apache-2.0

package cryptoerase

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

type checkedATA struct {
	*fakeATA
	err error
}

func (c checkedATA) Check(context.Context) error { return c.err }

type checkedPERC struct {
	*fakePERC
	err error
}

func (c checkedPERC) Check(context.Context) error { return c.err }

var (
	errNoHdparm = errors.New("hdparm not found (hdparm)")
	errNoCLI    = errors.New("RAID controller CLI not found")
)

func TestPreflight(t *testing.T) {
	for _, tc := range []struct {
		name      string
		setup     func(h *testHost, o *Options)
		ataErr    error
		percErr   error
		wantError []string // empty: the run goes ahead
	}{
		{name: "SATA drive without hdparm", ataErr: errNoHdparm,
			setup: func(h *testHost, o *Options) {
				h.addSCSI("sda", scsiSpec{vendor: "ATA", driver: "ahci", ata: &fakeATADisk{words: ataWords("M", "S", "F", true, false)}})
			},
			wantError: []string{"required tool not available; nothing was changed", "SATA drive sda: hdparm not found"}},
		{name: "PERC virtual disk without CLI", ataErr: errNoHdparm, percErr: errNoCLI,
			setup: func(h *testHost, o *Options) {
				h.addSCSI("sda", scsiSpec{vendor: "DELL", model: "PERC H355 Front", driver: "megaraid_sas"})
			},
			wantError: []string{"PERC/MegaRAID disk sda: RAID controller CLI not found"}},
		{name: "RAID reset without either", ataErr: errNoHdparm, percErr: errNoCLI,
			setup:     func(h *testHost, o *Options) { o.RAIDReset = true },
			wantError: []string{"RAID reset requested: hdparm not found", "RAID reset requested: RAID controller CLI not found"}},
		{name: "NVMe only", ataErr: errNoHdparm, percErr: errNoCLI,
			setup: func(h *testHost, o *Options) { h.addNVMe("nvme0", nvmeSpec{sanicap: 1}) }},
		{name: "excluded and in-use SATA drives need no hdparm", ataErr: errNoHdparm,
			setup: func(h *testHost, o *Options) {
				h.addSCSI("sda", scsiSpec{vendor: "ATA", driver: "ahci"})
				h.addSCSI("sdb", scsiSpec{vendor: "ATA", driver: "ahci"})
				h.addSCSI("sdc", scsiSpec{vendor: "ATA", driver: "ahci", usb: true})
				o.Exclude = []string{"sda"}
				// Swap on sdb2.
				resolved, err := filepath.EvalSymlinks(filepath.Join(h.sys, "block/sdb"))
				must(t, err)
				write(t, filepath.Join(resolved, "sdb2/partition"), "2")
				must(t, symlink(filepath.Join(resolved, "sdb2"), filepath.Join(h.sys, "class/block/sdb2")))
				appendLine(t, filepath.Join(h.proc, "swaps"), "/dev/sdb2 partition 1048572 0 -2")
			}},
		{name: "tools present", setup: func(h *testHost, o *Options) {
			h.addSCSI("sda", scsiSpec{vendor: "ATA", driver: "megaraid_sas", ata: &fakeATADisk{words: ataWords("M", "S", "F", true, false)}})
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHost(t)
			o := h.options(ModeErase)
			o.ATA = checkedATA{h.ata, tc.ataErr}
			o.PERC = checkedPERC{h.perc, tc.percErr}
			tc.setup(h, &o)
			rep, err := Run(context.Background(), o)
			if len(tc.wantError) == 0 {
				if err != nil || rep == nil {
					t.Fatalf("run stopped: %v", err)
				}
				return
			}
			if !errors.Is(err, ErrToolMissing) || rep != nil {
				t.Fatalf("want ErrToolMissing, got %v", err)
			}
			for _, w := range tc.wantError {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("error %q lacks %q", err, w)
				}
			}
		})
	}
}
