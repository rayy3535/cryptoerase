// SPDX-License-Identifier: Apache-2.0

// Package perc lists the physical drives behind a Dell PERC / Broadcom
// MegaRAID controller using perccli64, perccli, storcli64 or storcli. The
// operating system only sees virtual disks there, so this is the evidence of
// which drives a virtual disk hides and whether they are self-encrypting.
package perc

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"sort"
	"strings"
	"time"
)

// Tools are tried in this order.
var Tools = []string{"perccli64", "perccli", "storcli64", "storcli"}

// Drive is one physical drive from the controller CLI.
type Drive struct {
	Controller *int   `json:"controller,omitempty"`
	Slot       string `json:"slot"`
	DID        *int   `json:"did,omitempty"`
	State      string `json:"state,omitempty"`
	DG         any    `json:"dg,omitempty"`
	Interface  string `json:"intf,omitempty"`
	Media      string `json:"media,omitempty"`
	SED        string `json:"sed,omitempty"`
	Model      string `json:"model,omitempty"`
	Serial     string `json:"serial,omitempty"`
	WWN        string `json:"wwn,omitempty"`
	Tool       string `json:"tool"`
}

// Lister runs a controller CLI. LookPath and Exec are overridable for tests.
type Lister struct {
	LookPath func(string) (string, error)
	Exec     func(ctx context.Context, path string, args ...string) ([]byte, error)
	Timeout  time.Duration
}

// List returns the physical drives, or nil with no error when no CLI is
// installed.
func (l *Lister) List(ctx context.Context) ([]Drive, error) {
	path, name := l.tool()
	if path == "" {
		return nil, nil
	}
	var lastErr error
	for _, args := range [][]string{{"/call/eall/sall", "show", "J"}, {"/call/sall", "show", "J"}} {
		out, err := l.exec(ctx, path, args...)
		if err != nil && len(out) == 0 {
			lastErr = err
			continue
		}
		drives, perr := Parse(out, name)
		if perr == nil && len(drives) > 0 {
			return drives, nil
		}
		lastErr = perr
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("%s returned no drives", name)
	}
	return nil, lastErr
}

func runCommand(ctx context.Context, path string, args ...string) ([]byte, error) {
	var out bytes.Buffer
	// G204: path comes from exec.LookPath of a fixed tool name; no shell.
	cmd := exec.CommandContext(ctx, path, args...) //nolint:gosec // G204, see above
	cmd.Stdout = &out
	cmd.WaitDelay = 5 * time.Second
	err := cmd.Run()
	return out.Bytes(), err
}

// Parse extracts drives from storcli-style JSON ("show J"): every object that
// carries an "EID:Slt" (or "Slt") key under each controller's response.
func Parse(out []byte, tool string) ([]Drive, error) {
	var doc struct {
		Controllers []struct {
			CommandStatus struct {
				Controller *int `json:"Controller"`
			} `json:"Command Status"`
			ResponseData any `json:"Response Data"`
		} `json:"Controllers"`
	}
	if err := json.Unmarshal(out, &doc); err != nil {
		return nil, fmt.Errorf("%s: parse JSON: %w", tool, err)
	}
	var drives []Drive
	seen := map[string]bool{}
	for _, c := range doc.Controllers {
		walk(c.ResponseData, func(m map[string]any) {
			slot, ok := str(m["EID:Slt"])
			if !ok {
				if slot, ok = str(m["Slt"]); !ok {
					return
				}
			}
			key := fmt.Sprintf("%v/%s", c.CommandStatus.Controller, slot)
			if c.CommandStatus.Controller != nil {
				key = fmt.Sprintf("%d/%s", *c.CommandStatus.Controller, slot)
			}
			if seen[key] {
				return
			}
			seen[key] = true
			d := Drive{Controller: c.CommandStatus.Controller, Slot: slot, Tool: tool}
			if v, ok := m["DID"].(float64); ok {
				i := int(v)
				d.DID = &i
			}
			d.State, _ = str(m["State"])
			d.DG = m["DG"]
			d.Interface, _ = str(m["Intf"])
			d.Media, _ = str(m["Med"])
			d.SED, _ = str(m["SED"])
			model, _ := str(m["Model"])
			d.Model = strings.TrimSpace(model)
			drives = append(drives, d)
		})
	}
	sort.SliceStable(drives, func(i, j int) bool {
		ci, cj := -1, -1
		if drives[i].Controller != nil {
			ci = *drives[i].Controller
		}
		if drives[j].Controller != nil {
			cj = *drives[j].Controller
		}
		if ci != cj {
			return ci < cj
		}
		return drives[i].Slot < drives[j].Slot
	})
	return drives, nil
}

func str(v any) (string, bool) {
	s, ok := v.(string)
	return s, ok
}

func walk(v any, fn func(map[string]any)) {
	switch x := v.(type) {
	case map[string]any:
		if _, ok := x["EID:Slt"]; ok {
			fn(x)
			return
		}
		if _, ok := x["Slt"]; ok {
			fn(x)
			return
		}
		for _, c := range x {
			walk(c, fn)
		}
	case []any:
		for _, c := range x {
			walk(c, fn)
		}
	}
}
