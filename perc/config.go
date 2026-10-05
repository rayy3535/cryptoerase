// SPDX-License-Identifier: Apache-2.0

package perc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// VirtualDisk is one RAID virtual disk.
type VirtualDisk struct {
	Controller int    `json:"controller"`
	VD         int    `json:"vd"`
	DG         int    `json:"dg"`
	RAID       string `json:"raid,omitempty"`
	State      string `json:"state,omitempty"`
	Size       string `json:"size,omitempty"`
	Name       string `json:"name,omitempty"`
	// OSDevice is the block device the CLI reports for the VD ("/dev/sda"),
	// if any; NAA is its SCSI NAA identifier, matched against
	// /sys/block/*/device/wwid when OSDevice is missing.
	OSDevice string `json:"os_device,omitempty"`
	NAA      string `json:"naa,omitempty"`
	// Drives are the member slots, "EID:Slt" or "Slt".
	Drives []string `json:"drives,omitempty"`
}

// Controller is the configuration of one RAID controller.
type Controller struct {
	Index  int           `json:"controller"`
	Tool   string        `json:"tool"`
	VDs    []VirtualDisk `json:"virtual_disks,omitempty"`
	Drives []Drive       `json:"drives,omitempty"`
	// JBOD is the controller's JBOD mode ("/cN show jbod"): "ON", "OFF", or
	// empty when the controller does not report it. With JBOD off, drives
	// cannot be set to JBOD (non-RAID); Broadcom-branded and OEM controllers
	// ship with it off.
	JBOD string `json:"jbod,omitempty"`
}

// Path returns the CLI object path of a drive slot on controller c:
// "64:0" -> /c0/e64/s0, "3" -> /c0/s3.
func Path(c int, slot string) string {
	if e, s, ok := strings.Cut(slot, ":"); ok {
		return fmt.Sprintf("/c%d/e%s/s%s", c, e, s)
	}
	return fmt.Sprintf("/c%d/s%s", c, slot)
}

// Foreign reports whether the drive carries a foreign configuration.
func (d Drive) Foreign() bool {
	s, ok := d.DG.(string)
	return ok && strings.EqualFold(strings.TrimSpace(s), "F")
}

// dgNumber returns the drive group, or -1 when the drive is in none.
func (d Drive) dgNumber() int {
	if f, ok := d.DG.(float64); ok {
		return int(f)
	}
	if s, ok := d.DG.(string); ok {
		if n, err := strconv.Atoi(strings.TrimSpace(s)); err == nil {
			return n
		}
	}
	return -1
}

// DriveGroup returns the drive group number, or -1 when the drive is in none.
func (d Drive) DriveGroup() int { return d.dgNumber() }

func (l *Lister) exec(ctx context.Context, path string, args ...string) ([]byte, error) {
	run := l.Exec
	if run == nil {
		run = runCommand
	}
	timeout := l.Timeout
	if timeout == 0 {
		timeout = 60 * time.Second
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return run(cctx, path, args...)
}

// Controllers reads every controller's virtual disks and physical drives
// (with serial numbers and WWNs). It returns nil with no error when no CLI is
// installed.
func (l *Lister) Controllers(ctx context.Context) ([]Controller, error) {
	path, name := l.tool(ctx)
	if path == "" {
		return nil, nil
	}
	var pdOut []byte
	var pdErrs []error
	for _, args := range [][]string{{"/call/eall/sall", "show", "all", "J"}, {"/call/sall", "show", "all", "J"}} {
		out, err := l.exec(ctx, path, args...)
		drives, perr := Parse(out, name)
		if (err == nil || len(out) > 0) && perr == nil && len(drives) > 0 {
			pdOut = out
			break
		}
		switch {
		case err != nil || perr != nil:
			pdErrs = append(pdErrs, errors.Join(err, perr))
		default:
			pdErrs = append(pdErrs, fmt.Errorf("%s: no drives listed%s", strings.Join(args[:len(args)-1], " "), statusNote(out)))
		}
	}
	if pdOut == nil {
		return nil, fmt.Errorf("%s: list physical drives: %w%s", name, errors.Join(pdErrs...), l.noControllerHint(ctx))
	}
	vdOut, err := l.exec(ctx, path, "/call/vall", "show", "all", "J")
	if err != nil && len(vdOut) == 0 {
		return nil, fmt.Errorf("%s: list virtual disks: %w", name, err)
	}
	ctrls, err := ParseConfig(pdOut, vdOut, name)
	if err != nil {
		return nil, err
	}
	for i := range ctrls {
		// Not every controller reports it; unknown is left empty.
		ctrls[i].JBOD, _ = l.JBODMode(ctx, ctrls[i].Index)
	}
	return ctrls, nil
}

// JBODMode returns controller c's JBOD mode, "ON" or "OFF" ("/cC show
// jbod").
func (l *Lister) JBODMode(ctx context.Context, c int) (string, error) {
	_, doc, err := l.doJSON(ctx, fmt.Sprintf("/c%d", c), "show", "jbod")
	if err != nil {
		return "", err
	}
	return ParseJBODMode(doc)
}

// ParseJBODMode finds {"Ctrl_Prop": "JBOD", "Value": "OFF"} in the response
// of "show jbod".
func ParseJBODMode(doc *jsonDoc) (string, error) {
	var mode string
	for _, c := range doc.Controllers {
		walkMaps(c.ResponseData, func(m map[string]any) {
			if p, _ := str(m["Ctrl_Prop"]); strings.EqualFold(strings.TrimSpace(p), "JBOD") {
				v, _ := str(m["Value"])
				mode = strings.ToUpper(strings.TrimSpace(v))
			}
		})
	}
	if mode != "ON" && mode != "OFF" {
		return "", fmt.Errorf("no JBOD mode in the output (%q)", mode)
	}
	return mode, nil
}

// walkMaps calls fn for every object in v, at any depth.
func walkMaps(v any, fn func(map[string]any)) {
	switch x := v.(type) {
	case map[string]any:
		fn(x)
		for _, c := range x {
			walkMaps(c, fn)
		}
	case []any:
		for _, c := range x {
			walkMaps(c, fn)
		}
	}
}

// EnableJBOD turns on controller c's JBOD mode ("/cC set jbod=on"), which
// SetJBOD needs.
func (l *Lister) EnableJBOD(ctx context.Context, c int) (string, error) {
	return l.do(ctx, fmt.Sprintf("/c%d", c), "set", "jbod=on")
}

// ParseConfig combines "/call/eall/sall show all J" and
// "/call/vall show all J" output into per-controller configurations.
func ParseConfig(pdOut, vdOut []byte, tool string) ([]Controller, error) {
	drives, err := Parse(pdOut, tool)
	if err != nil {
		return nil, err
	}
	details, err := parseDetails(pdOut)
	if err != nil {
		return nil, err
	}
	vds, err := ParseVirtualDisks(vdOut)
	if err != nil {
		return nil, err
	}
	byCtrl := map[int]*Controller{}
	get := func(i int) *Controller {
		if byCtrl[i] == nil {
			byCtrl[i] = &Controller{Index: i, Tool: tool}
		}
		return byCtrl[i]
	}
	for _, d := range drives {
		if d.Controller == nil {
			return nil, fmt.Errorf("%s: drive %s has no controller number", tool, d.Slot)
		}
		if det, ok := details[Path(*d.Controller, d.Slot)]; ok {
			d.Serial, d.WWN, d.CryptoErase, d.Sanitize = det.serial, det.wwn, det.cryptoErase, det.sanitize
		}
		c := get(*d.Controller)
		c.Drives = append(c.Drives, d)
	}
	for _, v := range vds {
		c := get(v.Controller)
		c.VDs = append(c.VDs, v)
	}
	var out []Controller
	for _, c := range byCtrl {
		out = append(out, *c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Index < out[j].Index })
	return out, nil
}

type driveDetail struct {
	serial, wwn, sanitize string
	cryptoErase           bool
}

var reDriveDetail = regexp.MustCompile(`^Drive (/c\d+(?:/e\d+)?/s\d+) (Device attributes|Policies/Settings)$`)

// parseDetails extracts "SN" and "WWN" (from "Drive /cX/eY/sZ Device
// attributes") and "Cryptographic Erase Capable" and "Sanitize Support"
// (from "Drive /cX/eY/sZ Policies/Settings") out of "show all J" output,
// keyed by drive path. The objects are found at any depth.
func parseDetails(out []byte) (map[string]driveDetail, error) {
	var doc jsonDoc
	if err := json.Unmarshal(out, &doc); err != nil {
		return nil, err
	}
	res := map[string]driveDetail{}
	for _, c := range doc.Controllers {
		var walkDet func(v any)
		walkDet = func(v any) {
			m, ok := v.(map[string]any)
			if !ok {
				return
			}
			for k, sub := range m {
				g := reDriveDetail.FindStringSubmatch(k)
				attrs, ok := sub.(map[string]any)
				if g == nil || !ok {
					walkDet(sub)
					continue
				}
				det := res[g[1]]
				if g[2] == "Device attributes" {
					sn, _ := str(attrs["SN"])
					wwn, _ := str(attrs["WWN"])
					det.serial, det.wwn = strings.TrimSpace(sn), strings.TrimSpace(wwn)
				} else {
					ce, _ := str(attrs["Cryptographic Erase Capable"])
					det.cryptoErase = strings.EqualFold(strings.TrimSpace(ce), "Yes")
					san, _ := str(attrs["Sanitize Support"])
					det.sanitize = strings.TrimSpace(san)
				}
				res[g[1]] = det
			}
		}
		walkDet(c.ResponseData)
	}
	return res, nil
}

type jsonDoc struct {
	Controllers []struct {
		CommandStatus struct {
			Controller  *int   `json:"Controller"`
			Status      string `json:"Status"`
			Description string `json:"Description"`
			Detailed    any    `json:"Detailed Status"`
		} `json:"Command Status"`
		ResponseData any `json:"Response Data"`
	} `json:"Controllers"`
}

var (
	reVDKey    = regexp.MustCompile(`^/c(\d+)/v(\d+)$`)
	rePDsForVD = regexp.MustCompile(`^PDs for VD (\d+)$`)
	reVDProps  = regexp.MustCompile(`^VD(\d+) Properties$`)
)

// ParseVirtualDisks parses "/call/vall show all J". A controller without
// virtual disks answers with a failure status ("No VDs have been
// configured"), which is not an error here.
func ParseVirtualDisks(out []byte) ([]VirtualDisk, error) {
	var doc jsonDoc
	if err := json.Unmarshal(out, &doc); err != nil {
		return nil, fmt.Errorf("virtual disks: parse JSON: %w", err)
	}
	var vds []VirtualDisk
	for _, c := range doc.Controllers {
		st := c.CommandStatus
		if !strings.EqualFold(st.Status, "Success") {
			if strings.Contains(strings.ToLower(st.Description), "no vd") {
				continue
			}
			return nil, fmt.Errorf("virtual disks: %s: %s", st.Status, st.Description)
		}
		m, ok := c.ResponseData.(map[string]any)
		if !ok {
			continue
		}
		byVD := map[int]*VirtualDisk{}
		get := func(ctrl, n int) *VirtualDisk {
			if byVD[n] == nil {
				byVD[n] = &VirtualDisk{Controller: ctrl, VD: n, DG: -1}
			}
			return byVD[n]
		}
		ctrlOf := func() int {
			if st.Controller != nil {
				return *st.Controller
			}
			return 0
		}
		for k, v := range m {
			switch {
			case reVDKey.MatchString(k):
				g := reVDKey.FindStringSubmatch(k)
				ctrl, _ := strconv.Atoi(g[1])
				n, _ := strconv.Atoi(g[2])
				vd := get(ctrl, n)
				rows, _ := v.([]any)
				if len(rows) == 0 {
					continue
				}
				row, _ := rows[0].(map[string]any)
				if dgvd, ok := str(row["DG/VD"]); ok {
					if dg, _, ok := strings.Cut(dgvd, "/"); ok {
						vd.DG, _ = strconv.Atoi(dg)
					}
				}
				vd.RAID, _ = str(row["TYPE"])
				vd.State, _ = str(row["State"])
				vd.Size, _ = str(row["Size"])
				vd.Name, _ = str(row["Name"])
			case rePDsForVD.MatchString(k):
				n, _ := strconv.Atoi(rePDsForVD.FindStringSubmatch(k)[1])
				vd := get(ctrlOf(), n)
				rows, _ := v.([]any)
				for _, r := range rows {
					row, _ := r.(map[string]any)
					if s, ok := str(row["EID:Slt"]); ok {
						vd.Drives = append(vd.Drives, s)
					} else if s, ok := str(row["Slt"]); ok {
						vd.Drives = append(vd.Drives, s)
					} else if f, ok := row["Slt"].(float64); ok {
						vd.Drives = append(vd.Drives, strconv.Itoa(int(f)))
					}
				}
			case reVDProps.MatchString(k):
				n, _ := strconv.Atoi(reVDProps.FindStringSubmatch(k)[1])
				vd := get(ctrlOf(), n)
				props, _ := v.(map[string]any)
				os, _ := str(props["OS Drive Name"])
				vd.OSDevice = strings.TrimSpace(os)
				naa, _ := str(props["SCSI NAA Id"])
				vd.NAA = strings.ToLower(strings.TrimSpace(naa))
			}
		}
		for _, vd := range byVD {
			sort.Strings(vd.Drives)
			vds = append(vds, *vd)
		}
	}
	sort.Slice(vds, func(i, j int) bool {
		if vds[i].Controller != vds[j].Controller {
			return vds[i].Controller < vds[j].Controller
		}
		return vds[i].VD < vds[j].VD
	})
	return vds, nil
}

// CommandError is a controller CLI command that did not report success.
type CommandError struct {
	Command string
	Status  string
	Detail  string
}

func (e *CommandError) Error() string {
	return fmt.Sprintf("%s: %s %s", e.Command, e.Status, e.Detail)
}

// do runs one configuration command (JSON output) and checks its status.
func (l *Lister) do(ctx context.Context, args ...string) (string, error) {
	cmd, _, err := l.doJSON(ctx, args...)
	return cmd, err
}

// doJSON runs one command with JSON output, checks every controller's
// status and returns the decoded document.
func (l *Lister) doJSON(ctx context.Context, args ...string) (string, *jsonDoc, error) {
	path, name := l.tool(ctx)
	if path == "" {
		return "", nil, errors.New("no PERC/MegaRAID CLI installed")
	}
	cmd := name + " " + strings.Join(args, " ")
	out, err := l.exec(ctx, path, append(args, "J")...)
	var doc jsonDoc
	if jerr := json.Unmarshal(out, &doc); jerr != nil || len(doc.Controllers) == 0 {
		if err == nil {
			err = fmt.Errorf("unreadable output: %q", strings.TrimSpace(string(out)))
		}
		return cmd, nil, &CommandError{Command: cmd, Status: "error", Detail: err.Error()}
	}
	for _, c := range doc.Controllers {
		if !strings.EqualFold(c.CommandStatus.Status, "Success") {
			detail := c.CommandStatus.Description
			if c.CommandStatus.Detailed != nil {
				b, _ := json.Marshal(c.CommandStatus.Detailed)
				detail += " " + string(b)
			}
			return cmd, nil, &CommandError{Command: cmd, Status: c.CommandStatus.Status, Detail: strings.TrimSpace(detail)}
		}
	}
	return cmd, &doc, nil
}

// DeleteVD deletes virtual disk vd on controller c ("/cC/vVD delete force").
func (l *Lister) DeleteVD(ctx context.Context, c, vd int) (string, error) {
	return l.do(ctx, fmt.Sprintf("/c%d/v%d", c, vd), "delete", "force")
}

// DeleteHotSpare removes the hot-spare role from a drive.
func (l *Lister) DeleteHotSpare(ctx context.Context, c int, slot string) (string, error) {
	return l.do(ctx, Path(c, slot), "delete", "hotsparedrive")
}

// SetJBOD makes an unconfigured drive a non-RAID (JBOD) drive, so the
// operating system sees it directly.
func (l *Lister) SetJBOD(ctx context.Context, c int, slot string) (string, error) {
	return l.do(ctx, Path(c, slot), "set", "jbod")
}
