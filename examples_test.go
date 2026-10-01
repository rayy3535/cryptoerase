// SPDX-License-Identifier: Apache-2.0

package cryptoerase

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rayy3535/cryptoerase/perc"
)

// TestExamples checks that examples/*.json match what a simulated host
// produces, so the published sample reports never drift from the code.
// Regenerate them after an intended change with:
//
//	CRYPTOERASE_WRITE_EXAMPLES=1 go test -run TestExamples .
func TestExamples(t *testing.T) {
	update := os.Getenv("CRYPTOERASE_WRITE_EXAMPLES") != ""
	for _, mode := range []Mode{ModeInventory, ModeErase} {
		h := newHost(t)
		write(t, filepath.Join(h.sys, "class/dmi/id/product_serial"), "EXAMPLE01\n")
		write(t, filepath.Join(h.sys, "class/dmi/id/sys_vendor"), "Example Vendor\n")
		write(t, filepath.Join(h.sys, "class/dmi/id/product_name"), "Example Server\n")
		h.addNVMe("nvme0", nvmeSpec{sanicap: 7, fna: 4, oacs: 1, level0: level0Locking(0x09), model: "EXAMPLE NVME SSD 6.4TB (Sanitize)", fw: "1.0"})
		h.addNVMe("nvme1", nvmeSpec{sanicap: 0, fna: 4, model: "EXAMPLE NVME SSD 960GB (Format only)", fw: "1.0"})
		h.addSCSI("sda", scsiSpec{vendor: "DELL", model: "PERC Example Front", driver: "megaraid_sas"})
		h.addSCSI("sdb", scsiSpec{vendor: "ATA", driver: "ahci", ata: &fakeATADisk{words: ataWords("EXAMPLE SATA SSD 960GB", "EXAMPLESATA01", "1.0", true, false)}})
		h.perc.drives = []perc.Drive{
			{Controller: new(0), Slot: "64:0", DID: new(0), State: "Onln", DG: 0, Interface: "SATA", Media: "SSD", SED: "N", Model: "EXAMPLE SATA SSD 960GB", Tool: "perccli64"},
			{Controller: new(0), Slot: "64:1", DID: new(1), State: "Onln", DG: 0, Interface: "SATA", Media: "SSD", SED: "N", Model: "EXAMPLE SATA SSD 960GB", Tool: "perccli64"},
		}
		o := h.options(mode)
		o.AllowFormat = true
		o.JobID = "EXAMPLE-JOB-0001"
		rep := h.run(o)

		// Normalise the simulated environment.
		rep.Host.Hostname, rep.Host.Kernel = "example-host", "6.8.0-example"
		rep.Tool.GoVersion, rep.Tool.ATABackend = "go1.27.1", "hdparm v9.65"
		base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
		rep.StartedAt, rep.FinishedAt = base, base.Add(40*time.Second)
		for _, d := range rep.Drives {
			d.StartedAt, d.FinishedAt = base, base.Add(30*time.Second)
			if d.EraseDurationSec > 0 {
				d.EraseDurationSec = 4.2
			}
		}
		b, err := json.MarshalIndent(rep, "", "  ")
		must(t, err)
		out := strings.ReplaceAll(string(b), h.dev, "/dev")
		out = strings.ReplaceAll(out, "SN-nvme0n1", "EXAMPLENVME0001")
		out = strings.ReplaceAll(out, "SN-nvme1n1", "EXAMPLENVME0002")
		out += "\n"
		path := filepath.Join("examples", "report-"+mode.String()+".json")
		if update {
			must(t, os.MkdirAll("examples", 0o755))
			must(t, os.WriteFile(path, []byte(out), 0o644))
			continue
		}
		want, err := os.ReadFile(path)
		must(t, err)
		if string(want) != out {
			t.Errorf("%s is stale; regenerate with CRYPTOERASE_WRITE_EXAMPLES=1 go test -run TestExamples .\n%s", path, firstDiff(string(want), out))
		}
	}
}

// firstDiff returns the first differing line of two texts.
func firstDiff(a, b string) string {
	al, bl := strings.Split(a, "\n"), strings.Split(b, "\n")
	for i := range max(len(al), len(bl)) {
		var x, y string
		if i < len(al) {
			x = al[i]
		}
		if i < len(bl) {
			y = bl[i]
		}
		if x != y {
			return fmt.Sprintf("line %d:\n  file: %s\n  code: %s", i+1, x, y)
		}
	}
	return ""
}
