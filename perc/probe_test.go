// SPDX-License-Identifier: Apache-2.0

package perc

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func ctrlCount(n string) string {
	return `{"Controllers":[{"Command Status":{"CLI Version":"007.2612.0000.0000 June 13, 2023","Status Code":0,"Status":"Success","Description":"None"},"Response Data":{"Controller Count":` + n + `}}]}`
}

// multiCLI is a host with perccli64 in $PATH and storcli64 in its install
// directory, each answering from its own table.
type multiCLI struct {
	out   map[string]map[string]string // tool name -> args -> output
	calls []string
}

func (m *multiCLI) lister() *Lister {
	paths := map[string]string{"perccli64": "/usr/sbin/perccli64", "/opt/MegaRAID/storcli/storcli64": "/opt/MegaRAID/storcli/storcli64"}
	return &Lister{
		LookPath: func(n string) (string, error) {
			if p, ok := paths[n]; ok {
				return p, nil
			}
			return "", errors.New("not found")
		},
		Exec: func(_ context.Context, path string, args ...string) ([]byte, error) {
			k := filepath.Base(path) + " " + strings.Join(args, " ")
			m.calls = append(m.calls, k)
			return []byte(m.out[filepath.Base(path)][strings.Join(args, " ")]), nil
		},
	}
}

// perccli64 sees no Broadcom-branded controller; storcli64 does and is used.
func TestToolPrefersCLIThatSeesController(t *testing.T) {
	m := &multiCLI{out: map[string]map[string]string{
		"perccli64": {"show ctrlcount J": ctrlCount("0")},
		"storcli64": {"show ctrlcount J": ctrlCount("1")},
	}}
	l := m.lister()
	ctx := context.Background()
	for range 2 {
		if p, n := l.tool(ctx); p != "/opt/MegaRAID/storcli/storcli64" || n != "storcli64" {
			t.Fatalf("%q %q", p, n)
		}
	}
	if strings.Join(m.calls, ";") != "perccli64 show ctrlcount J;storcli64 show ctrlcount J" {
		t.Fatalf("probes %v", m.calls)
	}
}

// With one CLI installed nothing is probed.
func TestToolSingleCLINotProbed(t *testing.T) {
	f := &fakeCLI{out: map[string]string{}}
	if _, n := f.lister().tool(context.Background()); n != "perccli64" || len(f.calls) != 0 {
		t.Fatalf("%q %v", n, f.calls)
	}
}

// perccli64 alone on a server whose controller it does not manage: the
// error says so, instead of an empty "%!w(<nil>)".
func TestControllersNoControllerSeen(t *testing.T) {
	f := &fakeCLI{out: map[string]string{
		"/call/eall/sall show all J": ctrlCount("0"),
		"/call/sall show all J":      ctrlCount("0"),
		"/call/eall/sall show J":     ctrlCount("0"),
		"/call/sall show J":          ctrlCount("0"),
		"show ctrlcount J":           ctrlCount("0"),
	}}
	_, err := f.lister().Controllers(context.Background())
	want := "perccli64: list physical drives: /call/eall/sall show all: no drives listed (Success: None)\n" +
		"/call/sall show all: no drives listed (Success: None); no controller seen by perccli64 (show ctrlcount: 0). " + NoControllerHint
	if err == nil || err.Error() != want || strings.Contains(err.Error(), "%!") {
		t.Fatalf("%v", err)
	}
	if _, err := f.lister().List(context.Background()); err == nil || !strings.Contains(err.Error(), "returned no drives; no controller seen by perccli64") {
		t.Fatalf("List: %v", err)
	}
}

// When storcli64 sees no controller either, the hint points at the kernel.
func TestControllersNoCLISeesController(t *testing.T) {
	m := &multiCLI{out: map[string]map[string]string{
		"perccli64": {"show ctrlcount J": ctrlCount("0"), "/call/eall/sall show all J": ctrlCount("0")},
		"storcli64": {"show ctrlcount J": ctrlCount("0")},
	}}
	_, err := m.lister().Controllers(context.Background())
	if err == nil || !strings.Contains(err.Error(), "no controller seen by perccli64, storcli64 (show ctrlcount: 0). check the kernel log") {
		t.Fatalf("%v", err)
	}
}

// An explicit path is not probed for the choice, and the hint names only it.
func TestNoControllerHintExplicitPath(t *testing.T) {
	l := &Lister{Path: "/srv/storcli64", Exec: func(_ context.Context, _ string, args ...string) ([]byte, error) {
		return []byte(ctrlCount("0")), nil
	}}
	if h := l.noControllerHint(context.Background()); !strings.Contains(h, "no controller seen by storcli64 (") {
		t.Fatalf("%q", h)
	}
	// A CLI that cannot count adds nothing.
	l = &Lister{Path: "/srv/storcli64", Exec: func(context.Context, string, ...string) ([]byte, error) { return nil, errors.New("exit status 1") }}
	if h := l.noControllerHint(context.Background()); h != "" {
		t.Fatalf("%q", h)
	}
	none := &Lister{LookPath: func(string) (string, error) { return "", errors.New("no") }}
	if h := none.noControllerHint(context.Background()); h != "" {
		t.Fatalf("%q", h)
	}
}

func TestParseControllerCount(t *testing.T) {
	for in, want := range map[string]int{
		ctrlCount("2"): 2,
		ctrlCount("0"): 0,
		`{"Controllers":[{"Command Status":{"Status":"Failure","Description":"No Controller found"}}]}`:    0,
		`{"Controllers":[{"Command Status":{"Status":"Failure","Description":"Controller 0 not found"}}]}`: 0,
	} {
		if n, err := ParseControllerCount([]byte(in)); err != nil || n != want {
			t.Errorf("%s: %d %v", in, n, err)
		}
	}
	for _, in := range []string{"", "{", `{"Controllers":[]}`, `{"Controllers":[{"Command Status":{"Status":"Failure","Description":"Un-supported command"}}]}`} {
		if _, err := ParseControllerCount([]byte(in)); err == nil {
			t.Errorf("%q accepted", in)
		}
	}
}

// A failing probe is not taken for "no controller"; the first CLI is kept.
func TestToolProbeErrors(t *testing.T) {
	m := &multiCLI{out: map[string]map[string]string{}}
	l := m.lister()
	if _, n := l.tool(context.Background()); n != "perccli64" {
		t.Fatalf("%q", n)
	}
	if r := l.counts["/usr/sbin/perccli64"]; r.err == nil || !strings.Contains(r.err.Error(), "perccli64 show ctrlcount: parse JSON") {
		t.Fatalf("%+v", r)
	}
	l = &Lister{
		LookPath: func(n string) (string, error) { return "/x/" + filepath.Base(n), nil },
		Exec:     func(context.Context, string, ...string) ([]byte, error) { return nil, errors.New("exit status 1") },
	}
	if _, n := l.tool(context.Background()); n != "perccli64" || !strings.Contains(l.counts["/x/perccli64"].err.Error(), "exit status 1") {
		t.Fatalf("%q %+v", n, l.counts)
	}
}

// A $PATH link to a CLI in its install directory is one CLI.
func TestInstalledDeduplicatesLinks(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "opt", "storcli64")
	link := filepath.Join(dir, "bin", "storcli64")
	for _, d := range []string{filepath.Dir(target), filepath.Dir(link)} {
		if err := os.MkdirAll(d, 0o750); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(target, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	l := &Lister{LookPath: func(n string) (string, error) {
		switch n {
		case "storcli64":
			return link, nil
		case "/opt/MegaRAID/storcli/storcli64":
			return target, nil
		}
		return "", errors.New("no")
	}}
	if got := l.installed(); len(got) != 1 || got[0].path != link {
		t.Fatalf("%+v", got)
	}
}

func TestStatusNote(t *testing.T) {
	for in, want := range map[string]string{
		`{"Controllers":[]}`: " (no controller in the output)",
		`{"Controllers":[{"Command Status":{"Status":"Failure","Description":"Controller 0 not found"}}]}`: " (Failure: Controller 0 not found)",
		`{"Controllers":[{}]}`: "",
		"not json":             "",
	} {
		if got := statusNote([]byte(in)); got != want {
			t.Errorf("%s: %q", in, got)
		}
	}
}
