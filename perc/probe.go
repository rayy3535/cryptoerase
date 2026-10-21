// SPDX-License-Identifier: Apache-2.0

package perc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

// NoControllerHint explains why an installed CLI may see no controller.
const NoControllerHint = "perccli64 manages Dell PERC controllers only; Broadcom-, LSI- and OEM-branded MegaRAID controllers need storcli64"

// cli is one installed controller CLI.
type cli struct{ path, name string }

// probeResult is what "show ctrlcount" returned for one CLI.
type probeResult struct {
	count int
	err   error
}

// installed returns the controller CLIs found, in Tools order: in $PATH,
// then in InstallDirs. A CLI found twice (a link in $PATH to an install
// directory) is listed once.
func (l *Lister) installed() []cli {
	look := l.LookPath
	if look == nil {
		look = exec.LookPath
	}
	var out []cli
	seen := map[string]bool{}
	add := func(p, n string) {
		key := p
		if r, err := filepath.EvalSymlinks(p); err == nil {
			key = r
		}
		if !seen[key] {
			seen[key] = true
			out = append(out, cli{p, n})
		}
	}
	for _, t := range Tools {
		if p, err := look(t); err == nil {
			add(p, t)
		}
	}
	for _, dir := range InstallDirs {
		for _, t := range Tools {
			if p, err := look(filepath.Join(dir, t)); err == nil {
				add(p, t)
			}
		}
	}
	return out
}

// tool returns the CLI to run and its name; empty when none is installed.
//
// An explicit Path is used as given: if it is wrong, running it fails with an
// error rather than looking like no CLI is installed. Otherwise, with several
// CLIs installed, it is the first that sees a controller ("show ctrlcount"):
// perccli64 finds no Broadcom-branded or OEM controller, where storcli64
// does. With one CLI installed, or none that sees a controller, it is the
// first one. The choice is made once.
func (l *Lister) tool(ctx context.Context) (path, name string) {
	if l.Path != "" {
		return l.Path, filepath.Base(l.Path)
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.chosen == nil {
		l.chosen = &cli{}
		cands := l.installed()
		if len(cands) > 0 {
			*l.chosen = cands[0]
		}
		if len(cands) > 1 {
			for _, c := range cands {
				if r := l.countLocked(ctx, c); r.err == nil && r.count > 0 {
					*l.chosen = c
					break
				}
			}
		}
	}
	return l.chosen.path, l.chosen.name
}

// countLocked runs "show ctrlcount" with CLI c, once per CLI. l.mu is held.
func (l *Lister) countLocked(ctx context.Context, c cli) probeResult {
	if r, ok := l.counts[c.path]; ok {
		return r
	}
	out, err := l.exec(ctx, c.path, "show", "ctrlcount", "J")
	var r probeResult
	n, perr := ParseControllerCount(out)
	switch {
	case perr == nil:
		r.count = n // a count in the output counts, whatever the exit status
	case err != nil:
		r.err = fmt.Errorf("%s show ctrlcount: %w", c.name, err)
	default:
		r.err = fmt.Errorf("%s show ctrlcount: %w", c.name, perr)
	}
	if l.counts == nil {
		l.counts = map[string]probeResult{}
	}
	l.counts[c.path] = r
	return r
}

var reNoController = regexp.MustCompile(`(?i)no controller|controller \S* ?not found`)

// ParseControllerCount reads the output of "show ctrlcount J":
// "Controller Count" under "Response Data". A failure status that says no
// controller was found counts as zero.
func ParseControllerCount(out []byte) (int, error) {
	var doc jsonDoc
	if err := json.Unmarshal(out, &doc); err != nil {
		return 0, fmt.Errorf("parse JSON: %w", err)
	}
	for _, c := range doc.Controllers {
		if m, ok := c.ResponseData.(map[string]any); ok {
			if n, ok := m["Controller Count"].(float64); ok {
				return int(n), nil
			}
		}
		if reNoController.MatchString(c.CommandStatus.Description) {
			return 0, nil
		}
	}
	return 0, errors.New("no controller count in the output")
}

// ErrNoController is wrapped in the errors of List and Controllers when no
// installed CLI sees a controller: each one answered "show ctrlcount" with 0.
var ErrNoController = errors.New("no controller found")

// noControllerError marks err with ErrNoController, keeping its message.
type noControllerError struct{ err error }

func (e *noControllerError) Error() string   { return e.err.Error() }
func (e *noControllerError) Unwrap() []error { return []error{e.err, ErrNoController} }

// withNoController appends an explanation to err when the CLI in use sees no
// controller, and marks it with ErrNoController when no installed CLI does.
func (l *Lister) withNoController(ctx context.Context, err error) error {
	hint, none := l.noController(ctx)
	if hint == "" {
		return err
	}
	err = fmt.Errorf("%w%s", err, hint)
	if none {
		return &noControllerError{err}
	}
	return err
}

// noController returns an explanation to append to an error when the CLI in
// use sees no controller, else "", and whether every installed CLI (or the
// explicit Path) answered "show ctrlcount" with 0.
func (l *Lister) noController(ctx context.Context) (hint string, none bool) {
	path, name := l.tool(ctx)
	if path == "" {
		return "", false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if r := l.countLocked(ctx, cli{path, name}); r.err != nil || r.count > 0 {
		return "", false
	}
	names := []string{name}
	storcli := strings.HasPrefix(name, "storcli")
	none = true
	if l.Path == "" {
		for _, c := range l.installed() {
			if c.path == path {
				continue
			}
			r := l.countLocked(ctx, c)
			if r.err != nil || r.count != 0 {
				none = false
				continue
			}
			// The same CLI may be installed twice (a copy in $PATH and one
			// in its install directory); it is named once.
			if !slices.Contains(names, c.name) {
				names = append(names, c.name)
			}
			storcli = storcli || strings.HasPrefix(c.name, "storcli")
		}
	}
	hint = NoControllerHint
	if storcli {
		hint = "check the kernel log (dmesg | grep -i megaraid) for the controller's state"
	}
	return fmt.Sprintf("; no controller seen by %s (show ctrlcount: 0). %s", strings.Join(names, ", "), hint), none
}

// statusNote returns the CLI's own status from its JSON output, as
// " (Status: Description)", or "".
func statusNote(out []byte) string {
	var doc jsonDoc
	if json.Unmarshal(out, &doc) != nil {
		return ""
	}
	if len(doc.Controllers) == 0 {
		return " (no controller in the output)"
	}
	var parts []string
	for _, c := range doc.Controllers {
		st := c.CommandStatus
		if s := strings.TrimSpace(st.Status + ": " + st.Description); s != ":" {
			parts = append(parts, s)
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return " (" + strings.Join(parts, "; ") + ")"
}
