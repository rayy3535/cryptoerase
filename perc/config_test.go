// SPDX-License-Identifier: Apache-2.0

package perc

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// The synthetic_*.json fixtures follow the storcli/perccli JSON layout; they
// are not captured from hardware.

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestParseConfig(t *testing.T) {
	ctrls, err := ParseConfig(readFixture(t, "synthetic_pd_showall.json"), readFixture(t, "synthetic_vall_showall.json"), "perccli64")
	if err != nil {
		t.Fatal(err)
	}
	if len(ctrls) != 1 || ctrls[0].Index != 0 || ctrls[0].Tool != "perccli64" {
		t.Fatalf("%+v", ctrls)
	}
	c := ctrls[0]
	if len(c.Drives) != 3 {
		t.Fatalf("drives %+v", c.Drives)
	}
	d0 := c.Drives[0]
	if d0.Slot != "64:0" || d0.Serial != "EXAMPLESATA0001" || d0.WWN != "5002538E00000001" || d0.State != "Onln" || d0.DriveGroup() != 0 ||
		!d0.CryptoErase || d0.Sanitize != "CryptoErase, BlockErase" {
		t.Errorf("drive 0 %+v", d0)
	}
	if d1 := c.Drives[1]; d1.Serial != "EXAMPLESATA0002" || d1.CryptoErase || d1.Sanitize != "" {
		t.Errorf("drive 1 %+v", d1)
	}
	if hs := c.Drives[2]; hs.State != "DHS" || hs.DriveGroup() != -1 || hs.Foreign() {
		t.Errorf("hot spare %+v", hs)
	}
	want := VirtualDisk{Controller: 0, VD: 0, DG: 0, RAID: "RAID1", State: "Optl", Size: "893.750 GB", Name: "data",
		OSDevice: "/dev/sda", NAA: "6f4ee0804f0feb002e000000000000ab", Drives: []string{"64:0", "64:1"}}
	if len(c.VDs) != 1 || !reflect.DeepEqual(c.VDs[0], want) {
		t.Errorf("vd %+v", c.VDs)
	}
}

func TestParseVirtualDisksNone(t *testing.T) {
	out := `{"Controllers":[{"Command Status":{"Controller":0,"Status":"Failure","Description":"No VD's have been configured."}}]}`
	vds, err := ParseVirtualDisks([]byte(out))
	if err != nil || len(vds) != 0 {
		t.Fatalf("%v %+v", err, vds)
	}
	bad := `{"Controllers":[{"Command Status":{"Controller":0,"Status":"Failure","Description":"Controller not found"}}]}`
	if _, err := ParseVirtualDisks([]byte(bad)); err == nil {
		t.Fatal("failure accepted")
	}
	if _, err := ParseVirtualDisks([]byte("{")); err == nil {
		t.Fatal("bad JSON accepted")
	}
}

func TestDriveHelpers(t *testing.T) {
	if Path(0, "64:3") != "/c0/e64/s3" || Path(1, "7") != "/c1/s7" {
		t.Fatal("Path")
	}
	for dg, want := range map[any]int{float64(2): 2, "3": 3, "-": -1, nil: -1, "F": -1} {
		if got := (Drive{DG: dg}).DriveGroup(); got != want {
			t.Errorf("DG %v: %d", dg, got)
		}
	}
	if !(Drive{DG: "F"}).Foreign() || (Drive{DG: float64(0)}).Foreign() {
		t.Error("Foreign")
	}
}

// fakeCLI answers controller CLI invocations from a table and records them.
type fakeCLI struct {
	out   map[string]string
	calls []string
	err   error
}

func (f *fakeCLI) lister() *Lister {
	return &Lister{
		LookPath: func(n string) (string, error) {
			if n == "perccli64" {
				return "/opt/perccli64", nil
			}
			return "", errors.New("no")
		},
		Exec: func(_ context.Context, _ string, args ...string) ([]byte, error) {
			k := strings.Join(args, " ")
			f.calls = append(f.calls, k)
			return []byte(f.out[k]), f.err
		},
	}
}

func TestControllers(t *testing.T) {
	f := &fakeCLI{out: map[string]string{
		"/call/eall/sall show all J": string(readFixture(t, "synthetic_pd_showall.json")),
		"/call/vall show all J":      string(readFixture(t, "synthetic_vall_showall.json")),
	}}
	ctrls, err := f.lister().Controllers(context.Background())
	if err != nil || len(ctrls) != 1 || len(ctrls[0].VDs) != 1 || len(ctrls[0].Drives) != 3 {
		t.Fatalf("%v %+v", err, ctrls)
	}

	// No CLI installed: nothing to report, no error.
	none := &Lister{LookPath: func(string) (string, error) { return "", errors.New("no") }}
	if c, err := none.Controllers(context.Background()); c != nil || err != nil {
		t.Fatalf("%v %v", c, err)
	}

	// Drive listing fails in both forms.
	broken := &fakeCLI{out: map[string]string{}, err: errors.New("exit status 1")}
	if _, err := broken.lister().Controllers(context.Background()); err == nil || !strings.Contains(err.Error(), "list physical drives") {
		t.Fatalf("%v", err)
	}
}

func TestConfigCommands(t *testing.T) {
	ok := `{"Controllers":[{"Command Status":{"Controller":0,"Status":"Success","Description":"None"}}]}`
	f := &fakeCLI{out: map[string]string{
		"/c0/v0 delete force J":             ok,
		"/c0/e64/s2 delete hotsparedrive J": ok,
		"/c0/e64/s0 set jbod J":             ok,
		"/c0/e64/s1 set jbod J":             `{"Controllers":[{"Command Status":{"Controller":0,"Status":"Failure","Description":"Set PD JBOD Failed.","Detailed Status":[{"Drive":"/c0/e64/s1","Status":"Failure","ErrMsg":"operation not allowed"}]}}]}`,
		"/c0/s3 set jbod J":                 "not json",
	}}
	l := f.lister()
	ctx := context.Background()
	if cmd, err := l.DeleteVD(ctx, 0, 0); err != nil || cmd != "perccli64 /c0/v0 delete force" {
		t.Errorf("%q %v", cmd, err)
	}
	if cmd, err := l.DeleteHotSpare(ctx, 0, "64:2"); err != nil || cmd != "perccli64 /c0/e64/s2 delete hotsparedrive" {
		t.Errorf("%q %v", cmd, err)
	}
	if _, err := l.SetJBOD(ctx, 0, "64:0"); err != nil {
		t.Error(err)
	}
	_, err := l.SetJBOD(ctx, 0, "64:1")
	var ce *CommandError
	if !errors.As(err, &ce) || ce.Status != "Failure" || !strings.Contains(err.Error(), "operation not allowed") {
		t.Errorf("failure: %v", err)
	}
	if _, err := l.SetJBOD(ctx, 0, "3"); !errors.As(err, &ce) || !strings.Contains(err.Error(), "unreadable output") {
		t.Errorf("garbage: %v", err)
	}
	none := &Lister{LookPath: func(string) (string, error) { return "", errors.New("no") }}
	if _, err := none.DeleteVD(ctx, 0, 0); err == nil {
		t.Error("no CLI accepted")
	}
}

func TestToolLookup(t *testing.T) {
	// An explicit path is used as given.
	if p, n := (&Lister{Path: "/srv/bin/storcli64"}).tool(context.Background()); p != "/srv/bin/storcli64" || n != "storcli64" {
		t.Fatalf("explicit: %q %q", p, n)
	}
	// $PATH first, in Tools order.
	inPath := &Lister{LookPath: func(n string) (string, error) {
		if n == "storcli64" {
			return "/usr/sbin/storcli64", nil
		}
		return "", errors.New("not found")
	}}
	if p, n := inPath.tool(context.Background()); p != "/usr/sbin/storcli64" || n != "storcli64" {
		t.Fatalf("PATH: %q %q", p, n)
	}
	// Then the package install directories.
	var tried []string
	opt := &Lister{LookPath: func(n string) (string, error) {
		tried = append(tried, n)
		if n == "/opt/MegaRAID/storcli/storcli64" {
			return n, nil
		}
		return "", errors.New("not found")
	}}
	if p, n := opt.tool(context.Background()); p != "/opt/MegaRAID/storcli/storcli64" || n != "storcli64" {
		t.Fatalf("install dir: %q %q (tried %v)", p, n, tried)
	}
	if tried[4] != "/opt/MegaRAID/perccli/perccli64" {
		t.Fatalf("search order %v", tried)
	}
}

// A wrong explicit path is an error when run, not "no CLI installed".
func TestExplicitPathMissing(t *testing.T) {
	l := &Lister{Path: filepath.Join(t.TempDir(), "perccli64")}
	if _, err := l.Controllers(context.Background()); err == nil || !strings.Contains(err.Error(), "perccli64") {
		t.Fatalf("%v", err)
	}
}

func TestCheck(t *testing.T) {
	ctx := context.Background()
	notFound := func(string) (string, error) { return "", errors.New("not found") }
	if err := (&Lister{LookPath: notFound}).Check(ctx); err == nil ||
		!strings.Contains(err.Error(), "none of perccli64, perccli, storcli64, storcli in $PATH or /opt/MegaRAID/perccli, /opt/MegaRAID/storcli") {
		t.Fatalf("none: %v", err)
	}
	if err := (&Lister{Path: "/srv/perccli64", LookPath: notFound}).Check(ctx); err == nil || !strings.Contains(err.Error(), "(/srv/perccli64)") {
		t.Fatalf("explicit missing: %v", err)
	}
	found := func(n string) (string, error) { return n, nil }
	if err := (&Lister{Path: "/srv/perccli64", LookPath: found}).Check(ctx); err != nil {
		t.Fatalf("explicit: %v", err)
	}
	if err := (&Lister{LookPath: found}).Check(ctx); err != nil {
		t.Fatalf("in PATH: %v", err)
	}
	if err := (&Lister{Path: filepath.Join(t.TempDir(), "perccli64")}).Check(ctx); err == nil {
		t.Fatal("real lookup of a missing file passed")
	}
}
