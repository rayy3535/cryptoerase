// SPDX-License-Identifier: Apache-2.0

package cryptoerase

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/rayy3535/cryptoerase/nvme"
	"github.com/rayy3535/cryptoerase/tcg"
)

var errStalled = errors.New("progress stalled")

func (r *runner) nvmeDrive(ctx context.Context, c nvmeCtrl) *DriveRecord {
	cdev := r.devPath(c.name)
	rec := r.newRecord(cdev)
	rec.Interface, rec.MediaType = "NVMe", "SSD (NVMe)"
	for _, ns := range c.namespaces {
		rec.Namespaces = append(rec.Namespaces, r.devPath(ns))
	}
	if r.isExcluded(c.name) {
		return r.done(rec, Unhandled, "excluded by operator")
	}
	for _, ns := range c.namespaces {
		if r.isExcluded(ns) {
			return r.done(rec, Unhandled, fmt.Sprintf("namespace %s excluded by operator", ns))
		}
		if r.inUse[ns] {
			return r.done(rec, Unhandled, fmt.Sprintf("namespace %s is in use by the running OS; sanitize would destroy it", ns))
		}
	}

	dev, err := r.opts.OpenNVMe(cdev)
	if err != nil {
		return r.done(rec, Fail, fmt.Sprintf("open controller: %v", err))
	}
	defer dev.Close()
	idc, err := dev.IdentifyController()
	if err != nil {
		return r.done(rec, Fail, fmt.Sprintf("identify controller: %v", err))
	}
	rec.Model, rec.Serial, rec.Firmware = idc.Model, idc.Serial, idc.Firmware
	if idc.TNVMCAP.Hi == 0 {
		rec.CapacityBytes = idc.TNVMCAP.Lo
	}
	rec.NVMe = &NVMeInfo{
		Version: idc.VersionString(), OACS: idc.OACS, SANICAP: idc.SANICAP, FNA: idc.FNA,
		TNVMCAP: idc.TNVMCAP, UNVMCAP: idc.UNVMCAP,
		SanitizeCryptoErase:               idc.SanitizeCryptoErase(),
		SanitizeBlockErase:                idc.SanitizeBlockErase(),
		SanitizeOverwrite:                 idc.SanitizeOverwrite(),
		FormatCryptoErase:                 idc.FormatCryptoErase(),
		FormatAppliesToAllNamespaces:      idc.FormatAppliesToAllNamespaces(),
		SecureEraseAppliesToAllNamespaces: idc.SecureEraseAppliesToAllNamespaces(),
	}
	if sl, err := dev.SmartLog(); err == nil {
		rec.Health = sl
	}
	if idc.SecuritySendReceive() {
		rec.TCG = level0(dev)
	}
	fc := CheckFirmware(r.opts.FirmwarePolicy, idc.Model, idc.Firmware)
	rec.FirmwarePolicy = &fc
	if fc.Status == "fail" {
		return r.done(rec, Fail, fc.Detail)
	}

	rec.NISTMethod = "Purge"
	useSanitize := false
	switch {
	case idc.SanitizeCryptoErase():
		useSanitize = true
		rec.TechniqueDetail = "NVMe Sanitize, SANACT=" + nvme.SanitizeCryptoErase.String()
		rec.Scope = "entire NVM subsystem: all namespaces, unallocated capacity, caches"
		rec.Command = fmt.Sprintf("Sanitize (opcode 84h) CDW10=%#x on %s", nvme.SanitizeCDW10(nvme.SanitizeCryptoErase, true), cdev)
		rec.Planned = "nvme-sanitize-crypto-erase"
	case r.opts.AllowFormat && idc.FormatCryptoErase():
		rec.TechniqueDetail = "NVMe Format NVM, SES=" + nvme.SESCryptoErase.String()
		rec.Scope = "all user data of the formatted namespace(s)"
		if idc.SecureEraseAppliesToAllNamespaces() {
			rec.Scope += "; controller applies secure erase to all namespaces"
		}
		rec.Command = "Format NVM (opcode 80h) per namespace, SES=010b, current LBA format / metadata / PI kept"
		rec.Planned = "nvme-format-ses2"
		if !idc.UNVMCAP.IsZero() {
			rec.Planned = ""
			return r.done(rec, Fail, fmt.Sprintf("unallocated NVM capacity %s bytes is outside every namespace and is not covered by Format NVM; recreate a full-capacity namespace or use Sanitize", idc.UNVMCAP))
		}
	default:
		hint := ""
		if idc.FormatCryptoErase() {
			hint = "; Format NVM SES=010b is supported, rerun with --allow-format if accepted"
		}
		return r.done(rec, Fail, fmt.Sprintf("no cryptographic erase support (sanicap=%#x, fna=%#x)%s", idc.SANICAP, idc.FNA, hint))
	}
	rec.Technique = "Cryptographic Erase"

	if r.opts.Mode == ModeInventory {
		if len(c.namespaces) == 0 {
			return r.done(rec, Unhandled, "no namespace attached; restore namespace layout first")
		}
		return r.done(rec, Planned, "")
	}
	rec.Planned = ""
	if len(c.namespaces) == 0 {
		return r.done(rec, Unhandled, "no namespace attached; marker verification impossible, restore namespace layout and rerun")
	}
	if rec.TCG != nil && rec.TCG.Level0 != nil && rec.TCG.Locked {
		return r.done(rec, Fail, "TCG locking range is locked; PSID revert required")
	}
	// A sanitize left over from an earlier attempt must finish first. Drives
	// without any sanitize capability (NVMe 1.2) have no Sanitize Status log.
	if idc.SanitizeAny() {
		if _, err := r.waitNVMeSanitize(ctx, dev); err != nil {
			return r.done(rec, Fail, fmt.Sprintf("previous sanitize did not finish: %v", err))
		}
	}
	if ctx.Err() != nil {
		return r.done(rec, Fail, "interrupted before erase; drive not modified")
	}

	marks := map[string]*markers{}
	for _, ns := range c.namespaces {
		m, err := r.writeMarkers(r.devPath(ns))
		if err != nil {
			if errors.Is(err, errMarkerReadback) {
				return r.done(rec, Fail, fmt.Sprintf("marker read-back mismatch on %s before erase: %v", ns, err))
			}
			return r.done(rec, Fail, fmt.Sprintf("cannot write markers to %s before erase (locked or read-only?): %v", ns, err))
		}
		marks[ns] = m
	}

	rec.DeviceStatus = &DeviceStatus{}
	start := time.Now()
	if useSanitize {
		if err := dev.Sanitize(nvme.SanitizeCryptoErase, true); err != nil {
			return r.done(rec, Fail, fmt.Sprintf("sanitize crypto erase rejected: %v", err))
		}
		l, err := r.waitNVMeSanitize(ctx, dev)
		if l != nil {
			sprog, gde, etce := l.SPROG, l.GlobalDataErased(), l.ETCE
			rec.DeviceStatus.SSTAT = fmt.Sprintf("0x%04x", l.SSTAT)
			rec.DeviceStatus.SPROG = &sprog
			rec.DeviceStatus.GlobalDataErased = &gde
			if etce != 0xffffffff {
				rec.DeviceStatus.EstimatedCryptoEraseSecs = &etce
			}
		}
		if err != nil {
			if ctx.Err() != nil {
				return r.done(rec, Fail, "interrupted while sanitizing; the device continues the operation on its own, rerun to verify")
			}
			return r.done(rec, Fail, fmt.Sprintf("sanitize status unreadable or stalled: %v", err))
		}
		if s := l.Status(); s != nvme.SanitizeSucceeded && s != nvme.SanitizeSucceededNoDealloc {
			// AUSE was set, so Exit Failure Mode is permitted.
			_ = dev.Sanitize(nvme.SanitizeExitFailureMode, false)
			return r.done(rec, Fail, fmt.Sprintf("sanitize completed with status %d (expected 1 or 4); exit-failure-mode issued", s))
		}
	} else {
		for _, ns := range c.namespaces {
			nsid, err := r.namespaceID(ns)
			if err != nil {
				return r.done(rec, Fail, fmt.Sprintf("namespace id of %s: %v", ns, err))
			}
			idns, err := dev.IdentifyNamespace(nsid)
			if err != nil {
				return r.done(rec, Fail, fmt.Sprintf("identify namespace %s: %v", ns, err))
			}
			spec := nvme.KeepCurrentFormat(idns, nvme.SESCryptoErase)
			if err := dev.Format(nsid, spec, r.opts.FormatTimeout); err != nil {
				return r.done(rec, Fail, fmt.Sprintf("format SES=010b failed on %s: %v", ns, err))
			}
			rec.DeviceStatus.Format = append(rec.DeviceStatus.Format, FormatResult{
				Namespace: r.devPath(ns), NSID: nsid, LBAF: spec.LBAF, CDW10: fmt.Sprintf("%#x", spec.CDW10()),
			})
		}
	}
	rec.EraseDurationSec = seconds(time.Since(start))
	if err := dev.Rescan(); err != nil {
		rec.DeviceStatus.RescanError = err.Error()
	}

	var v verifyResult
	for _, ns := range c.namespaces {
		path := r.devPath(ns)
		if !r.waitNode(ctx, path) {
			return r.done(rec, Fail, fmt.Sprintf("%s did not reappear after erase", ns))
		}
		res, err := r.verifyMarkers(path, marks[ns])
		if err != nil {
			r.log.Warn("verify", "namespace", ns, "error", err)
		}
		v.add(res)
	}
	rec.Verification = r.verification(v)
	res, reason := verifiedResult(v)
	return r.done(rec, res, reason)
}

// waitNVMeSanitize polls the Sanitize Status log while a sanitize is in
// progress. It fails after three consecutive read errors, when SPROG does
// not change for NoProgressTimeout, or when ctx is cancelled.
func (r *runner) waitNVMeSanitize(ctx context.Context, dev nvme.Device) (*nvme.SanitizeLog, error) {
	var last *nvme.SanitizeLog
	lastChange := time.Now()
	errs := 0
	for {
		l, err := dev.SanitizeLog()
		if err != nil {
			errs++
			if errs > 3 {
				return last, err
			}
		} else {
			errs = 0
			if l.Status() != nvme.SanitizeInProgress {
				return l, nil
			}
			if last == nil || l.SPROG != last.SPROG {
				lastChange = time.Now()
			} else if time.Since(lastChange) >= r.opts.NoProgressTimeout {
				return l, fmt.Errorf("%w at SPROG %#x for %s", errStalled, l.SPROG, r.opts.NoProgressTimeout)
			}
			last = l
		}
		if err := sleep(ctx, r.opts.PollInterval); err != nil {
			return last, err
		}
	}
}

// namespaceID reads /sys/block/<ns>/nsid, falling back to NVME_IOCTL_ID.
func (r *runner) namespaceID(ns string) (uint32, error) {
	if s := readTrim(filepath.Join(r.opts.SysfsRoot, "block", ns, "nsid")); s != "" {
		v, err := strconv.ParseUint(s, 10, 32)
		if err == nil {
			return uint32(v), nil
		}
	}
	if _, err := os.Stat(r.devPath(ns)); err != nil {
		return 0, err
	}
	return r.opts.NamespaceID(r.devPath(ns))
}

func level0(dev nvme.Device) *TCGInfo {
	b, err := dev.SecurityReceive(0x01, 0x0001, 2048)
	if err != nil {
		return &TCGInfo{Error: err.Error()}
	}
	l, err := tcg.ParseLevel0(b)
	if err != nil {
		return &TCGInfo{Error: err.Error()}
	}
	return &TCGInfo{Level0: l}
}
