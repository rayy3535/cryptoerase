// SPDX-License-Identifier: Apache-2.0

package perc

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Controller-side cryptographic erase of one drive.
//
// A PERC/MegaRAID controller crypto-erases ISE and SED drives itself
// ("start erase crypto"). The drive must be unconfigured good, so a non-RAID
// (JBOD) drive is set good first and back to JBOD afterwards. The command's
// own status only says the erase was started, and "show erase" reports "Not
// in progress" both before and after an erase that takes no time, so the
// result is read from the controller's event log: "Erase completed on PD
// 26(e0x44/s0)", or "Erase failed ...".

// SetGood makes a drive unconfigured good ("set good force"). A non-RAID
// drive disappears from the operating system.
func (l *Lister) SetGood(ctx context.Context, c int, slot string) (string, error) {
	return l.do(ctx, Path(c, slot), "set", "good", "force")
}

// StartCryptoErase starts the controller's cryptographic erase of an
// unconfigured good drive ("start erase crypto").
func (l *Lister) StartCryptoErase(ctx context.Context, c int, slot string) (string, error) {
	return l.do(ctx, Path(c, slot), "start", "erase", "crypto")
}

// EraseProgress is the answer to "show erase".
type EraseProgress struct {
	// Status is the CLI's text, e.g. "Not in progress" or "In progress".
	Status string `json:"status"`
	// Percent is the progress, or -1 when the CLI shows none ("-").
	Percent int `json:"percent"`
}

// InProgress reports whether an erase is running.
func (p EraseProgress) InProgress() bool {
	s := strings.ToLower(p.Status)
	return strings.Contains(s, "progress") && !strings.Contains(s, "not")
}

// EraseStatus runs "show erase" for one drive.
func (l *Lister) EraseStatus(ctx context.Context, c int, slot string) (EraseProgress, error) {
	_, doc, err := l.doJSON(ctx, Path(c, slot), "show", "erase")
	if err != nil {
		return EraseProgress{}, err
	}
	return parseEraseStatus(doc)
}

func parseEraseStatus(doc *jsonDoc) (EraseProgress, error) {
	for _, c := range doc.Controllers {
		rows, _ := c.ResponseData.([]any)
		for _, r := range rows {
			row, ok := r.(map[string]any)
			if !ok {
				continue
			}
			st, ok := str(row["Status"])
			if !ok {
				continue
			}
			p := EraseProgress{Status: strings.TrimSpace(st), Percent: -1}
			if v, ok := row["Progress%"].(float64); ok {
				p.Percent = int(v)
			} else if s, ok := str(row["Progress%"]); ok {
				if n, err := strconv.Atoi(strings.TrimSpace(s)); err == nil {
					p.Percent = n
				}
			}
			return p, nil
		}
	}
	return EraseProgress{}, errors.New("show erase: no drive status in output")
}

// Event is one entry of the controller event log.
type Event struct {
	Seq         uint32 `json:"seq"`
	Time        string `json:"time,omitempty"`
	Code        string `json:"code,omitempty"`
	Description string `json:"description"`
	// DeviceID is the drive the event concerns ("Device ID" under "Event
	// Data"), when it names one.
	DeviceID *int `json:"device_id,omitempty"`
}

// Events returns the latest n entries of controller c's event log
// ("/cC show events type=latest=N"), newest first as the CLI prints them.
func (l *Lister) Events(ctx context.Context, c, n int) ([]Event, error) {
	path, name := l.tool()
	if path == "" {
		return nil, errors.New("no PERC/MegaRAID CLI installed")
	}
	args := []string{fmt.Sprintf("/c%d", c), "show", "events", fmt.Sprintf("type=latest=%d", n)}
	out, err := l.exec(ctx, path, args...)
	evs, perr := ParseEvents(string(out))
	if perr != nil || (err != nil && len(evs) == 0) {
		return nil, fmt.Errorf("%s %s: %w", name, strings.Join(args, " "), errors.Join(err, perr))
	}
	return evs, nil
}

var (
	reSeq       = regexp.MustCompile(`^seqNum:\s*0x([0-9a-fA-F]+)`)
	reCLIStatus = regexp.MustCompile(`^Status\s*=\s*(.+)$`)
)

// ParseEvents decodes the text of "show events" (the CLI prints it as text
// even when asked for JSON): records that start with "seqNum: 0x..." and
// carry "Time:", "Code:", "Event Description:" and, under "Event Data:",
// "Device ID:" lines. The CLI's "Status = ..." block comes after the
// events; a status other than Success is an error.
func ParseEvents(out string) ([]Event, error) {
	var evs []Event
	var cur *Event
	sc := bufio.NewScanner(strings.NewReader(out))
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if g := reCLIStatus.FindStringSubmatch(line); g != nil {
			if st := strings.TrimSpace(g[1]); !strings.EqualFold(st, "Success") {
				return nil, fmt.Errorf("status %s", st)
			}
			cur = nil // the CLI's own report follows
			continue
		}
		if g := reSeq.FindStringSubmatch(line); g != nil {
			v, err := strconv.ParseUint(g[1], 16, 32)
			if err != nil {
				return nil, fmt.Errorf("bad seqNum %q", g[1])
			}
			evs = append(evs, Event{Seq: uint32(v)})
			cur = &evs[len(evs)-1]
			continue
		}
		if cur == nil {
			continue
		}
		if k, v, ok := strings.Cut(line, ":"); ok {
			v = strings.TrimSpace(v)
			switch strings.TrimSpace(k) {
			case "Time":
				cur.Time = v
			case "Code":
				cur.Code = v
			case "Event Description":
				cur.Description = v
			case "Device ID":
				if n, err := strconv.Atoi(v); err == nil {
					cur.DeviceID = &n
				}
			}
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if len(evs) == 0 {
		return nil, errors.New("no events in output")
	}
	return evs, nil
}

var reEraseEvent = regexp.MustCompile(`^Erase (completed|failed|aborted)\b.*\bon PD ([0-9a-fA-F]+)\(`)

// EraseOutcome looks for the result of an erase of drive did among the
// events newer than seq: "completed", "failed" or "aborted" with the event,
// or "" when there is none yet. The drive is taken from the event's Device
// ID, else from the description, where PD numbers are hexadecimal ("PD 26"
// is device ID 38).
func EraseOutcome(evs []Event, seq uint32, did int) (string, *Event) {
	var best *Event
	var outcome string
	for i := range evs {
		e := &evs[i]
		if e.Seq <= seq {
			continue
		}
		g := reEraseEvent.FindStringSubmatch(e.Description)
		if g == nil {
			continue
		}
		if e.DeviceID != nil {
			if *e.DeviceID != did {
				continue
			}
		} else if pd, err := strconv.ParseUint(g[2], 16, 32); err != nil || int(pd) != did {
			continue
		}
		if best == nil || e.Seq > best.Seq {
			best, outcome = e, g[1]
		}
	}
	return outcome, best
}

// LatestSeq returns the highest sequence number among evs.
func LatestSeq(evs []Event) uint32 {
	var m uint32
	for _, e := range evs {
		m = max(m, e.Seq)
	}
	return m
}
