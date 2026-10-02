// SPDX-License-Identifier: Apache-2.0

package cryptoerase

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/rayy3535/cryptoerase/ata"
)

func (r *runner) scsiDrive(ctx context.Context, name string) *DriveRecord {
	p := filepath.Join(r.opts.SysfsRoot, "block", name)
	rec := r.newRecord(r.devPath(name))
	vendor := readTrim(filepath.Join(p, "device", "vendor"))
	model := readTrim(filepath.Join(p, "device", "model"))
	driver := r.scsiDriver(name)
	rec.Attach = &Attach{Driver: driver, SCSIVendor: vendor, SCSIModel: model, WWID: readTrim(filepath.Join(p, "device", "wwid"))}
	resolved, _ := filepath.EvalSymlinks(p)

	if readTrim(filepath.Join(p, "removable")) == "1" || strings.Contains(resolved, "/usb") {
		return r.done(rec, Skipped, "removable or USB device (virtual media / install media)")
	}
	sectors, _ := strconv.ParseUint(readTrim(filepath.Join(p, "size")), 10, 64)
	if sectors == 0 {
		return r.done(rec, Skipped, "zero size (no medium)")
	}
	rec.CapacityBytes = sectors * 512 // sysfs size is always in 512-byte units
	if r.isExcluded(name) {
		return r.done(rec, Unhandled, "excluded by operator")
	}
	if r.inUse[name] {
		return r.done(rec, Unhandled, "in use by the running OS")
	}
	if first, _, _ := strings.Cut(vendor, " "); first != "ATA" {
		if driver == "megaraid_sas" || strings.Contains(model, "PERC") {
			return r.percDisk(ctx, rec, driver)
		}
		return r.done(rec, Unhandled, fmt.Sprintf("not a directly attached ATA device (vendor=%q, model=%q, driver=%s); SAS or other controller, use controller tool", vendor, model, driver))
	}
	rec.Interface = "SATA"
	if readTrim(filepath.Join(p, "queue", "rotational")) == "1" {
		rec.MediaType = "HDD"
		return r.done(rec, Unhandled, "rotational HDD: outside crypto-erase scope, use overwrite")
	}
	rec.MediaType = "SSD (SATA)"
	return r.sataDrive(ctx, rec, name, driver)
}

func (r *runner) percDisk(ctx context.Context, rec *DriveRecord, driver string) *DriveRecord {
	if note, ok := r.raidNotes[filepath.Base(rec.Device)]; ok {
		switch {
		case note.skipped != "":
			return r.done(rec, Unhandled, "PERC virtual disk; RAID reset skipped this controller: "+note.skipped)
		case note.deleted && r.opts.Mode == ModeInventory:
			rec.Planned = "raid-reset"
			return r.done(rec, Planned, fmt.Sprintf("PERC virtual disk; the RAID reset deletes it and sets its %d drive(s) to non-RAID, then each drive is checked and erased on its own", note.drives))
		case note.deleted:
			return r.done(rec, Fail, "PERC virtual disk still present after the RAID reset deleted it")
		}
	}
	r.percOnce.Do(func() { r.percDrives, r.percErr = r.opts.PERC.List(ctx) })
	switch {
	case r.percErr != nil:
		rec.PERC = &PERCInfo{Error: r.percErr.Error()}
		return r.done(rec, Unhandled, fmt.Sprintf("PERC virtual disk (driver=%s); physical drives could not be listed: %v", driver, r.percErr))
	case len(r.percDrives) == 0:
		return r.done(rec, Unhandled, fmt.Sprintf("PERC virtual disk (driver=%s); physical drives not visible to the OS. Install perccli64 to list them, or check with racadm storage get pdisks", driver))
	}
	rec.PERC = &PERCInfo{PhysicalDrives: r.percDrives}
	sed := 0
	for _, d := range r.percDrives {
		if d.SED == "Y" {
			sed++
		}
	}
	return r.done(rec, Unhandled, fmt.Sprintf("PERC virtual disk; %d physical drive(s) behind the controller, %d SED. SED/ISE drives: delete the virtual disk, then crypto-erase via iDRAC/PERC. Other drives: set them non-RAID (or the controller to HBA mode) and rerun", len(r.percDrives), sed))
}

func (r *runner) sataDrive(ctx context.Context, rec *DriveRecord, name, driver string) *DriveRecord {
	dev := r.devPath(name)
	id, err := r.opts.ATA.Identify(ctx, dev)
	if err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return r.done(rec, Fail, fmt.Sprintf("ATA backend unavailable: %v", err))
		}
		return r.done(rec, Unhandled, fmt.Sprintf("ATA IDENTIFY not available through %s (ATA pass-through blocked?): %v; use controller tool", driver, err))
	}
	rec.Model, rec.Serial, rec.Firmware = id.Model(), id.Serial(), id.Firmware()
	sec := id.Security()
	rec.ATA = &ATAInfo{
		SanitizeFeatureSet: id.SanitizeSupported(),
		CryptoScramble:     id.CryptoScrambleSupported(),
		BlockErase:         id.BlockEraseSupported(),
		Overwrite:          id.OverwriteSupported(),
		NonRotating:        id.NonRotating(),
		Security:           sec,
	}
	fc := CheckFirmware(r.opts.FirmwarePolicy, rec.Model, rec.Firmware)
	rec.FirmwarePolicy = &fc
	if fc.Status == "fail" {
		return r.done(rec, Fail, fc.Detail)
	}
	rec.NISTMethod = "Purge"
	if !id.SanitizeSupported() || !id.CryptoScrambleSupported() {
		return r.done(rec, Fail, "no ATA SANITIZE CRYPTO SCRAMBLE support")
	}
	rec.Technique = "Cryptographic Erase"
	rec.TechniqueDetail = "ATA SANITIZE CRYPTO SCRAMBLE EXT"
	rec.Scope = "all user data on the device"
	rec.Command = "hdparm --yes-i-know-what-i-am-doing --sanitize-crypto-scramble " + dev
	rec.Planned = "ata-sanitize-crypto-scramble"
	if r.opts.Mode == ModeInventory {
		return r.done(rec, Planned, "")
	}
	rec.Planned = ""
	if sec.Locked {
		return r.done(rec, Fail, "ATA security is locked (a user password is set); unlock or PSID revert required")
	}
	st, err := r.opts.ATA.SanitizeStatus(ctx, dev)
	if err != nil {
		return r.done(rec, Fail, fmt.Sprintf("sanitize status unreadable: %v", err))
	}
	if st.Frozen {
		return r.done(rec, Fail, "sanitize is frozen (SANITIZE FREEZE LOCK issued by firmware); power-cycle with the freeze disabled, then rerun")
	}
	if st.InProgress {
		if _, err := r.waitATASanitize(ctx, dev); err != nil {
			return r.done(rec, Fail, fmt.Sprintf("previous sanitize did not finish: %v", err))
		}
	}
	if ctx.Err() != nil {
		return r.done(rec, Fail, "interrupted before erase; drive not modified")
	}

	m, err := r.writeMarkers(dev)
	if err != nil {
		if errors.Is(err, errMarkerReadback) {
			return r.done(rec, Fail, fmt.Sprintf("marker read-back mismatch before erase: %v", err))
		}
		return r.done(rec, Fail, fmt.Sprintf("cannot write markers before erase (locked or read-only?): %v", err))
	}
	defer m.release()

	start := time.Now()
	if err := r.opts.ATA.SanitizeCryptoScramble(ctx, dev); err != nil {
		return r.done(rec, Fail, fmt.Sprintf("SANITIZE CRYPTO SCRAMBLE rejected: %v", err))
	}
	st, err = r.waitATASanitize(ctx, dev)
	rec.DeviceStatus = &DeviceStatus{ATASanitize: st}
	if err != nil {
		if ctx.Err() != nil {
			return r.done(rec, Fail, "interrupted while sanitizing; the device continues the operation on its own, rerun to verify")
		}
		return r.done(rec, Fail, fmt.Sprintf("sanitize status unreadable or stalled: %v", err))
	}
	if !st.CompletedWithoutError {
		return r.done(rec, Fail, "sanitize did not report 'Completed Without Error'")
	}
	rec.EraseDurationSec = seconds(time.Since(start))
	m.release() // the erase is over: udev may probe the disk now

	v, verr := r.verifyMarkers(dev, m)
	if verr != nil {
		r.log.Warn("verify", "device", dev, "error", verr)
	}
	rec.Verification = r.verification(v)
	res, reason := verifiedResult(v)
	return r.done(rec, res, reason)
}

// waitATASanitize polls SANITIZE STATUS EXT until no operation is in
// progress.
func (r *runner) waitATASanitize(ctx context.Context, dev string) (*ata.SanitizeStatus, error) {
	var last *ata.SanitizeStatus
	lastChange := time.Now()
	errs := 0
	for {
		st, err := r.opts.ATA.SanitizeStatus(ctx, dev)
		if err != nil {
			errs++
			if errs > 3 {
				return last, err
			}
		} else {
			errs = 0
			if !st.InProgress {
				return st, nil
			}
			if last == nil || st.Progress != last.Progress {
				lastChange = time.Now()
			} else if time.Since(lastChange) >= r.opts.NoProgressTimeout {
				return st, fmt.Errorf("%w at progress %#x for %s", errStalled, st.Progress, r.opts.NoProgressTimeout)
			}
			last = st
		}
		if err := sleep(ctx, r.opts.PollInterval); err != nil {
			return last, err
		}
	}
}
