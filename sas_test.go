// SPDX-License-Identifier: Apache-2.0

package cryptoerase

import (
	"errors"
	"strings"
	"testing"

	"github.com/rayy3535/cryptoerase/scsi"
)

// sasDisk adds a SAS SSD on an HBA (seen: Seagate Nytro XS1920LE70134 on a
// Dell HBA355i, driver mpt3sas).
func sasDisk(h *testHost, name string, d *fakeSASDisk, rotational int) {
	h.addSCSI(name, scsiSpec{vendor: "SEAGATE", model: "XS1920LE70134", driver: "mpt3sas", rotational: rotational, vpd80: "EXAMPLESAS0001", sas: d})
}

func TestSASSanitizeCryptoErase(t *testing.T) {
	h := newHost(t)
	d := &fakeSASDisk{support: scsi.SupportYes, ua: 1}
	sasDisk(h, "sda", d, 0)

	inv := h.run(h.options(ModeInventory))
	r := wantResult(t, inv, "sda", Planned, "")
	if r.TechniqueDetail != "SCSI SANITIZE, CRYPTOGRAPHIC ERASE" || r.Interface != "SAS" || r.MediaType != "SSD (SAS)" ||
		r.Model != "SEAGATE XS1920LE70134" || r.Serial != "EXAMPLESAS0001" || r.SCSI == nil || r.SCSI.SanitizeCryptoErase != "supported" ||
		r.Planned != "scsi-sanitize-crypto-erase" {
		t.Fatalf("%+v", r)
	}
	if strings.Contains(strings.Join(d.calls, ","), "sanitize") {
		t.Fatalf("inventory sanitized: %v", d.calls)
	}

	d.calls = nil
	rep := h.run(h.options(ModeErase))
	r = wantResult(t, rep, "sda", Pass, "")
	if r.DeviceStatus == nil || r.DeviceStatus.SCSISanitize == nil || r.DeviceStatus.SCSISanitize.State != scsi.SanitizeIdle || r.Verification == nil {
		t.Fatalf("%+v", r)
	}
	if got := strings.Join(d.calls, ","); !strings.HasPrefix(got, "support,status,sanitize,status") {
		t.Fatalf("calls %s", got)
	}
}

func TestSASSanitizeOutcomes(t *testing.T) {
	for name, tc := range map[string]struct {
		disk       func() *fakeSASDisk
		rotational int
		mode       Mode
		result     Result
		reason     string
	}{
		"no change":         {func() *fakeSASDisk { return &fakeSASDisk{support: scsi.SupportYes, behaviour: "noop"} }, 0, ModeErase, Fail, "unchanged"},
		"sanitize failed":   {func() *fakeSASDisk { return &fakeSASDisk{support: scsi.SupportYes, behaviour: "fail"} }, 0, ModeErase, Fail, "the drive reports the sanitize failed (SANITIZE COMMAND FAILED)"},
		"rejected":          {func() *fakeSASDisk { return &fakeSASDisk{support: scsi.SupportYes, behaviour: "reject"} }, 0, ModeErase, Fail, "SANITIZE CRYPTOGRAPHIC ERASE rejected: SANITIZE: check condition"},
		"previous failure":  {func() *fakeSASDisk { return &fakeSASDisk{support: scsi.SupportYes, failed: true} }, 0, ModeInventory, Fail, "a previous sanitize failed"},
		"sense stuck":       {func() *fakeSASDisk { return &fakeSASDisk{support: scsi.SupportYes, ua: 9} }, 0, ModeInventory, Fail, "unexpected sense data 6/29/00"},
		"SSD not supported": {func() *fakeSASDisk { return &fakeSASDisk{support: scsi.SupportNo} }, 0, ModeInventory, Fail, "no SCSI SANITIZE CRYPTOGRAPHIC ERASE support"},
		"HDD not supported": {func() *fakeSASDisk { return &fakeSASDisk{support: scsi.SupportNo} }, 1, ModeInventory, Unhandled, "hard disk without a cryptographic erase"},
		"unknown":           {func() *fakeSASDisk { return &fakeSASDisk{support: scsi.SupportUnknown} }, 0, ModeInventory, Unhandled, "does not report its supported commands"},
		"query fails":       {func() *fakeSASDisk { return &fakeSASDisk{supErr: errors.New("SG_IO on /dev/sda: EIO")} }, 0, ModeInventory, Unhandled, "cannot ask the drive which SANITIZE actions it supports (driver=mpt3sas)"},
	} {
		t.Run(name, func(t *testing.T) {
			h := newHost(t)
			d := tc.disk()
			sasDisk(h, "sda", d, tc.rotational)
			r := wantResult(t, h.run(h.options(tc.mode)), "sda", tc.result, tc.reason)
			if tc.mode == ModeInventory && strings.Contains(strings.Join(d.calls, ","), "sanitize") {
				t.Fatalf("inventory sanitized: %v", d.calls)
			}
			if tc.result != Pass && r.Planned != "" && tc.mode == ModeErase {
				t.Errorf("planned %q", r.Planned)
			}
		})
	}
}

// A sanitize already running is waited for before the markers are written.
func TestSASSanitizeInProgressFirst(t *testing.T) {
	h := newHost(t)
	d := &fakeSASDisk{support: scsi.SupportYes, pending: 2}
	sasDisk(h, "sda", d, 0)
	wantResult(t, h.run(h.options(ModeErase)), "sda", Pass, "")
}
