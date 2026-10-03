// SPDX-License-Identifier: Apache-2.0

package cryptoerase

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/rayy3535/cryptoerase/perc"
)

// controllerLister reads the full configuration of the RAID controllers.
// *perc.Lister implements it.
type controllerLister interface {
	Controllers(ctx context.Context) ([]perc.Controller, error)
}

// megaraidHost returns the first SCSI host driven by megaraid_sas ("host0"),
// or "". The host exists even when no disk on it is visible to the OS.
func (r *runner) megaraidHost() string {
	hosts, _ := os.ReadDir(filepath.Join(r.opts.SysfsRoot, "class", "scsi_host"))
	for _, h := range hosts {
		if readTrim(filepath.Join(r.opts.SysfsRoot, "class", "scsi_host", h.Name(), "proc_name")) == "megaraid_sas" {
			return h.Name()
		}
	}
	return ""
}

// reportHiddenDrives adds a record for every drive behind a PERC/MegaRAID
// controller that the OS does not see and no other record covers: a drive
// in state Ready (UGood) or a hot spare has no block device, so without
// this it would be missing from the report.
//
// Online drives of a virtual disk are covered by the virtual disk's record,
// and drives the RAID reset exposed by reconcileRAID.
func (r *runner) reportHiddenDrives(ctx context.Context, rep *Report) {
	host := r.megaraidHost()
	if !r.opts.RAIDReset && host == "" {
		return
	}
	cl, ok := r.opts.PERC.(controllerLister)
	if !ok {
		return
	}
	now := time.Now().UTC()
	ctrls, err := cl.Controllers(ctx)
	if err != nil {
		rec := &DriveRecord{Device: strings.TrimSpace("RAID controller " + host), StartedAt: now, PERC: &PERCInfo{Error: err.Error()}}
		r.done(rec, Unhandled, fmt.Sprintf("drives behind the controller that the OS does not see could not be listed: %v", err))
		rep.Drives = append(rep.Drives, rec)
		return
	}
	handled := map[string]bool{}
	for _, e := range r.raidExposed {
		handled[perc.Path(e.ctrl, e.drive.Slot)] = true
	}
	plans := map[int]*RAIDReset{}
	for _, rr := range rep.RAIDReset {
		if rr.Controller >= 0 {
			plans[rr.Controller] = rr
		}
	}
	for _, c := range ctrls {
		vdDG := map[int]bool{}
		for _, vd := range c.VDs {
			vdDG[vd.DG] = true
		}
		for _, d := range c.Drives {
			slot := perc.Path(c.Index, d.Slot)
			state := strings.ToUpper(strings.TrimSpace(d.State))
			if handled[slot] || (state == "ONLN" && vdDG[d.DriveGroup()]) || r.recorded(rep, d, slot) {
				continue
			}
			rec := &DriveRecord{
				Device:    slot,
				Interface: d.Interface,
				Model:     d.Model,
				Serial:    d.Serial,
				Attach:    &Attach{RAIDSlot: slot},
				PERC:      &PERCInfo{PhysicalDrives: []perc.Drive{d}},
				StartedAt: now,
			}
			hidden := fmt.Sprintf("behind the PERC/MegaRAID controller in state %s, not visible to the OS", d.State)
			plan := plans[c.Index]
			switch {
			case plan != nil && plan.Skipped != "":
				r.done(rec, Unhandled, hidden+"; the RAID reset skipped this controller: "+plan.Skipped)
			case plan != nil && r.opts.Mode == ModeInventory && slices.ContainsFunc(plan.Expose, func(e perc.Drive) bool { return e.Slot == d.Slot }):
				rec.Planned = "raid-reset"
				r.done(rec, Planned, hidden+"; the RAID reset sets it to non-RAID, then it is checked and erased on its own")
			case plan == nil && !r.opts.RAIDReset:
				r.done(rec, Unhandled, hidden+"; rerun with --raid-reset to set it to non-RAID and erase it")
			default:
				r.done(rec, Unhandled, hidden+"; not erased")
			}
			rep.Drives = append(rep.Drives, rec)
		}
	}
}

// recorded reports whether a drive already has a record.
func (r *runner) recorded(rep *Report, d perc.Drive, slot string) bool {
	return r.recordOf(rep, d, slot) != nil
}

// recordOf returns the record of a controller drive: by serial number, WWN,
// controller slot, or the block device it is visible as; nil if none.
func (r *runner) recordOf(rep *Report, d perc.Drive, slot string) *DriveRecord {
	dev := ""
	if name := r.findExposed(d); name != "" {
		dev = r.devPath(name)
	}
	for _, rec := range rep.Drives {
		if exposedMatches(rec, d) || (rec.Attach != nil && rec.Attach.RAIDSlot == slot) || (dev != "" && rec.Device == dev) {
			return rec
		}
	}
	return nil
}
