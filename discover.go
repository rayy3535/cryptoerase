// SPDX-License-Identifier: Apache-2.0

package cryptoerase

import (
	"bufio"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

type nvmeCtrl struct {
	name       string   // nvme0
	namespaces []string // nvme0n1 ...
}

type inventory struct {
	nvme  []nvmeCtrl
	scsi  []string
	other []string
}

var (
	reCtrl   = regexp.MustCompile(`^nvme\d+$`)
	reNSHead = regexp.MustCompile(`^nvme\d+n\d+$`)
	reNSPath = regexp.MustCompile(`^nvme(\d+)c\d+n(\d+)$`) // native multipath path device
)

func ignoredBlock(name string) bool {
	for _, p := range []string{"loop", "ram", "zram", "dm-", "md", "sr", "nbd", "fd"} {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return strings.HasPrefix(name, "mmcblk") && (strings.Contains(name, "boot") || strings.Contains(name, "rpmb"))
}

// discover enumerates NVMe controllers with their namespaces, SCSI-class
// disks and any other block devices. Namespaces are mapped to controllers
// through /sys/class/nvme/<ctrl>/: entries there are either namespace block
// devices (nvme0n1) or, with native multipath, path devices (nvme0c1n1) whose
// head is nvme0n1.
func (r *runner) discover() (*inventory, error) {
	sysfs := r.opts.SysfsRoot
	byCtrl := map[string]map[string]bool{}
	claimed := map[string]bool{}
	ctrls, _ := os.ReadDir(filepath.Join(sysfs, "class", "nvme"))
	for _, c := range ctrls {
		if !reCtrl.MatchString(c.Name()) {
			continue
		}
		set := map[string]bool{}
		byCtrl[c.Name()] = set
		entries, _ := os.ReadDir(filepath.Join(sysfs, "class", "nvme", c.Name()))
		for _, e := range entries {
			n := e.Name()
			if reNSHead.MatchString(n) {
				set[n], claimed[n] = true, true
			} else if m := reNSPath.FindStringSubmatch(n); m != nil {
				head := "nvme" + m[1] + "n" + m[2]
				set[head], claimed[head] = true, true
			}
		}
	}
	blocks, err := os.ReadDir(filepath.Join(sysfs, "block"))
	if err != nil {
		return nil, err
	}
	inv := &inventory{}
	for _, b := range blocks {
		n := b.Name()
		switch {
		case reNSHead.MatchString(n):
			if !claimed[n] { // fall back to the controller with the same instance
				ctrl := n[:strings.LastIndex(n, "n")]
				if byCtrl[ctrl] == nil {
					byCtrl[ctrl] = map[string]bool{}
				}
				byCtrl[ctrl][n] = true
			}
		case reNSPath.MatchString(n):
		case strings.HasPrefix(n, "sd"):
			inv.scsi = append(inv.scsi, n)
		case ignoredBlock(n):
		default:
			inv.other = append(inv.other, n)
		}
	}
	for c, set := range byCtrl {
		nc := nvmeCtrl{name: c}
		for ns := range set {
			nc.namespaces = append(nc.namespaces, ns)
		}
		sortNatural(nc.namespaces)
		inv.nvme = append(inv.nvme, nc)
	}
	sort.Slice(inv.nvme, func(i, j int) bool { return CompareVersions(inv.nvme[i].name, inv.nvme[j].name) < 0 })
	sortNatural(inv.scsi)
	sortNatural(inv.other)
	return inv, nil
}

func sortNatural(s []string) {
	sort.Slice(s, func(i, j int) bool { return CompareVersions(s[i], s[j]) < 0 })
}

// computeInUse returns the whole-disk names under every mounted filesystem
// and active swap, following partitions and dm/md slaves.
func (r *runner) computeInUse() map[string]bool {
	set := map[string]bool{}
	add := func(src string) {
		if !strings.HasPrefix(src, "/dev/") {
			return
		}
		name := filepath.Base(src)
		if resolved, err := filepath.EvalSymlinks(filepath.Join(r.opts.DevRoot, strings.TrimPrefix(src, "/dev/"))); err == nil {
			name = filepath.Base(resolved)
		}
		for _, b := range r.resolveBase(name, 0) {
			set[b] = true
		}
	}
	for _, f := range []struct {
		file string
		skip int
	}{{"mounts", 0}, {"swaps", 1}} {
		fh, err := os.Open(filepath.Join(r.opts.ProcRoot, f.file))
		if err != nil {
			continue
		}
		sc := bufio.NewScanner(fh)
		for i := 0; sc.Scan(); i++ {
			if i < f.skip {
				continue
			}
			if fields := strings.Fields(sc.Text()); len(fields) > 0 {
				add(fields[0])
			}
		}
		fh.Close()
	}
	// Mount sources are not always usable /dev paths: the kernel shows the
	// root filesystem as /dev/root, and device-mapper nodes can be renamed.
	// mountinfo carries each mount's device number, which sysfs resolves.
	if fh, err := os.Open(filepath.Join(r.opts.ProcRoot, "self", "mountinfo")); err == nil {
		sc := bufio.NewScanner(fh)
		for sc.Scan() {
			f := strings.Fields(sc.Text())
			if len(f) < 3 || strings.HasPrefix(f[2], "0:") { // 0:N = no block device (tmpfs, overlay ...)
				continue
			}
			if resolved, err := filepath.EvalSymlinks(filepath.Join(r.opts.SysfsRoot, "dev", "block", f[2])); err == nil {
				for _, b := range r.resolveBase(filepath.Base(resolved), 0) {
					set[b] = true
				}
			}
		}
		fh.Close()
	}
	return set
}

func (r *runner) resolveBase(name string, depth int) []string {
	p := filepath.Join(r.opts.SysfsRoot, "class", "block", name)
	if depth < 16 {
		if slaves, _ := os.ReadDir(filepath.Join(p, "slaves")); len(slaves) > 0 {
			var out []string
			for _, s := range slaves {
				out = append(out, r.resolveBase(s.Name(), depth+1)...)
			}
			return out
		}
	}
	if _, err := os.Stat(filepath.Join(p, "partition")); err == nil {
		if resolved, err := filepath.EvalSymlinks(p); err == nil {
			return []string{filepath.Base(filepath.Dir(resolved))}
		}
	}
	return []string{name}
}

func readTrim(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// scsiDriver returns the SCSI host driver (ahci, mpt3sas, megaraid_sas ...).
func (r *runner) scsiDriver(name string) string {
	if host := r.scsiHost(name); host != "" {
		if d := readTrim(filepath.Join(r.opts.SysfsRoot, "class", "scsi_host", host, "proc_name")); d != "" {
			return d
		}
	}
	return "unknown"
}

// scsiHost returns the SCSI host ("host0") a disk is attached to, or "".
func (r *runner) scsiHost(name string) string {
	resolved, err := filepath.EvalSymlinks(filepath.Join(r.opts.SysfsRoot, "block", name, "device"))
	if err != nil {
		return ""
	}
	for part := range strings.SplitSeq(resolved, string(filepath.Separator)) {
		if strings.HasPrefix(part, "host") && len(part) > 4 && strings.Trim(part[4:], "0123456789") == "" {
			return part
		}
	}
	return ""
}

func (r *runner) host() Host {
	dmi := filepath.Join(r.opts.SysfsRoot, "class", "dmi", "id")
	h := Host{
		Manufacturer: readTrim(filepath.Join(dmi, "sys_vendor")),
		Product:      readTrim(filepath.Join(dmi, "product_name")),
		Serial:       readTrim(filepath.Join(dmi, "product_serial")),
		Kernel:       readTrim(filepath.Join(r.opts.ProcRoot, "sys", "kernel", "osrelease")),
	}
	h.Hostname, _ = os.Hostname()
	return h
}
