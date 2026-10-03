// SPDX-License-Identifier: Apache-2.0

package cryptoerase

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/rayy3535/cryptoerase/perc"
)

// The megaraid_sas driver puts virtual disks on SCSI channels 2 and up and
// physical (JBOD / non-RAID) drives on channels 0 and 1, 128 targets per
// channel, so a physical drive's target is its controller device ID modulo
// 128 (MEGASAS_MAX_PD_CHANNELS, MEGASAS_MAX_DEV_PER_CHANNEL in the driver).
// Seen on a PERC H355 (DID 38 at 0:0:38:0) and an H730P (DID 3 at 0:0:3:0).
const (
	megaraidPDChannels    = 2
	megaraidDevPerChannel = 128
)

// channelTarget returns a SCSI disk's channel and target, from the name of
// its sysfs device ("H:C:T:L", e.g. "0:2:0:0").
func (r *runner) channelTarget(name string) (channel, target int, ok bool) {
	resolved, err := filepath.EvalSymlinks(filepath.Join(r.opts.SysfsRoot, "block", name, "device"))
	if err != nil {
		return 0, 0, false
	}
	parts := strings.Split(filepath.Base(resolved), ":")
	if len(parts) != 4 {
		return 0, 0, false
	}
	var n [4]int
	for i, p := range parts {
		v, err := strconv.Atoi(p)
		if err != nil {
			return 0, 0, false
		}
		n[i] = v
	}
	return n[1], n[2], true
}

// isRAIDVirtualDisk reports whether a SCSI disk is a RAID virtual disk: a
// Dell PERC virtual disk reports the controller as its model, and
// megaraid_sas puts virtual disks on channels 2 and up. Physical drives the
// controller passes through (JBOD, non-RAID) report their own vendor and
// model.
func (r *runner) isRAIDVirtualDisk(name, model, driver string) bool {
	if strings.Contains(model, "PERC") {
		return true
	}
	if driver != "megaraid_sas" {
		return false
	}
	c, _, ok := r.channelTarget(name)
	return ok && c >= megaraidPDChannels
}

// megaraidDID returns the controller device ID of a physical drive on a
// megaraid_sas host.
func (r *runner) megaraidDID(name string) (int, bool) {
	if r.scsiDriver(name) != "megaraid_sas" {
		return 0, false
	}
	c, t, ok := r.channelTarget(name)
	if !ok || c >= megaraidPDChannels {
		return 0, false
	}
	return c*megaraidDevPerChannel + t, true
}

// megaraidHosts lists the SCSI hosts driven by megaraid_sas.
func (r *runner) megaraidHosts() []string {
	var out []string
	hosts, _ := os.ReadDir(filepath.Join(r.opts.SysfsRoot, "class", "scsi_host"))
	for _, h := range hosts {
		if readTrim(filepath.Join(r.opts.SysfsRoot, "class", "scsi_host", h.Name(), "proc_name")) == "megaraid_sas" {
			out = append(out, h.Name())
		}
	}
	return out
}

// vpdSerial returns a SCSI disk's unit serial number (VPD page 0x80).
func (r *runner) vpdSerial(name string) string {
	b, err := os.ReadFile(filepath.Join(r.opts.SysfsRoot, "block", name, "device", "vpd_pg80"))
	if err != nil || len(b) < 4 || b[1] != 0x80 {
		return ""
	}
	n := min(int(b[2])<<8|int(b[3]), len(b)-4)
	return strings.TrimSpace(string(b[4 : 4+n]))
}

// blockMatchesDrive reports whether block device name is the controller's
// physical drive d: by WWN or serial number in the wwid, by the serial
// number in VPD page 0x80 (SAS drives report it padded, e.g.
// "WFK7J2ZJ0000K9..." for "WFK7J2ZJ"), or, with a single megaraid_sas
// host, by the device ID the driver encodes in the SCSI address.
func (r *runner) blockMatchesDrive(name string, d perc.Drive) bool {
	if wwidMatches(readTrim(filepath.Join(r.opts.SysfsRoot, "block", name, "device", "wwid")), d) {
		return true
	}
	if s := r.vpdSerial(name); s != "" && d.Serial != "" && (s == d.Serial || (len(d.Serial) >= 8 && strings.HasPrefix(s, d.Serial))) {
		return true
	}
	if d.DID != nil && len(r.megaraidHosts()) == 1 {
		if did, ok := r.megaraidDID(name); ok && did == *d.DID {
			return true
		}
	}
	return false
}

// percPhysical handles a physical drive behind a megaraid_sas controller
// that hdparm cannot erase (a SAS drive, or a SATA hard disk): only the
// controller's cryptographic erase can, and only if it reports the drive
// capable (ISE or SED). Overwriting is outside this tool's scope.
func (r *runner) percPhysical(ctx context.Context, rec *DriveRecord, name, driver string, rotational bool) *DriveRecord {
	p := filepath.Join(r.opts.SysfsRoot, "block", name)
	rec.Model = rec.Attach.SCSIModel
	rec.Serial = r.vpdSerial(name)
	rec.Firmware = readTrim(filepath.Join(p, "device", "rev"))
	if rec.Interface == "" {
		rec.Interface = "SAS"
	}
	media := "SSD"
	if rotational {
		media = "HDD"
	}
	rec.MediaType = fmt.Sprintf("%s (%s)", media, rec.Interface)
	t, err := r.percEraseTarget(ctx, rec, name)
	if err != nil {
		return r.done(rec, Unhandled, fmt.Sprintf("%s drive behind the %s controller, which cannot crypto-erase it: %v. Overwriting is outside this tool's scope", rec.MediaType, driver, err))
	}
	if t.d.Interface != "" {
		rec.Interface = t.d.Interface
		rec.MediaType = fmt.Sprintf("%s (%s)", media, rec.Interface)
	}
	if t.d.Model != "" {
		rec.Model = t.d.Model
	}
	if t.d.Serial != "" {
		rec.Serial = t.d.Serial
	}
	fc := CheckFirmware(r.opts.FirmwarePolicy, rec.Model, rec.Firmware)
	rec.FirmwarePolicy = &fc
	if fc.Status == "fail" {
		return r.done(rec, Fail, fc.Detail)
	}
	rec.NISTMethod = "Purge"
	rec.Scope = "all user data on the device"
	return r.percErase(ctx, rec, name, driver, false, t)
}
