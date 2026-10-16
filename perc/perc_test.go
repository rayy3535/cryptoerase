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

func TestParseFixture(t *testing.T) {
	b, err := os.ReadFile("testdata/perccli_2xsata.json")
	if err != nil {
		t.Fatal(err)
	}
	d, err := Parse(b, "perccli64")
	if err != nil {
		t.Fatal(err)
	}
	if len(d) != 2 || d[0].Slot != "64:0" || d[1].Slot != "64:1" {
		t.Fatalf("drives %+v", d)
	}
	if d[0].Model != "SAMSUNG MZ7LH960HAJR-00005" || d[0].SED != "N" || d[0].Interface != "SATA" || *d[0].Controller != 0 || *d[1].DID != 1 {
		t.Fatalf("fields %+v", d[0])
	}
}

func TestParseDedupesDetailedOutput(t *testing.T) {
	doc := `{"Controllers":[{"Command Status":{"Controller":0},"Response Data":{
	  "Drive /c0/e64/s0":[{"EID:Slt":"64:0","DID":0,"SED":"Y","Model":"A"}],
	  "Drive /c0/e64/s0 - Detailed Information":{"Drive /c0/e64/s0 State":{"Media Error Count":0}},
	  "Drive Information":[{"EID:Slt":"64:0","DID":0,"SED":"Y","Model":"A"}]}}]}`
	d, err := Parse([]byte(doc), "storcli64")
	if err != nil || len(d) != 1 || d[0].SED != "Y" {
		t.Fatalf("%v %+v", err, d)
	}
}

func TestListNoTool(t *testing.T) {
	l := &Lister{LookPath: func(string) (string, error) { return "", errors.New("not found") }}
	d, err := l.List(context.Background())
	if err != nil || d != nil {
		t.Fatalf("%v %v", d, err)
	}
}

func TestListFallsBackToSall(t *testing.T) {
	b, _ := os.ReadFile("testdata/perccli_2xsata.json")
	var calls [][]string
	l := &Lister{
		LookPath: func(n string) (string, error) {
			if n == "storcli64" {
				return "/opt/storcli64", nil
			}
			return "", errors.New("no")
		},
		Exec: func(_ context.Context, path string, args ...string) ([]byte, error) {
			calls = append(calls, args)
			if args[0] == "/call/eall/sall" {
				return []byte(`{"Controllers":[{"Command Status":{"Controller":0,"Status":"Failure"}}]}`), nil
			}
			return b, nil
		},
	}
	d, err := l.List(context.Background())
	if err != nil || len(d) != 2 || d[0].Tool != "storcli64" || len(calls) != 2 {
		t.Fatalf("%v %+v %v", err, d, calls)
	}
}

func TestListRunsTheTool(t *testing.T) {
	b, err := os.ReadFile("testdata/perccli_2xsata.json")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	fixture := filepath.Join(dir, "out.json")
	if err := os.WriteFile(fixture, b, 0o600); err != nil {
		t.Fatal(err)
	}
	tool := filepath.Join(dir, "perccli64")
	script := "#!/bin/sh\n[ \"$1 $2 $3\" = '/call/eall/sall show J' ] || exit 3\ncat " + fixture + "\n"
	if err := os.WriteFile(tool, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	l := &Lister{LookPath: func(n string) (string, error) {
		if n == "perccli64" {
			return tool, nil
		}
		return "", errors.New("no")
	}}
	d, err := l.List(context.Background())
	if err != nil || len(d) != 2 || d[0].Tool != "perccli64" {
		t.Fatalf("%v %+v", err, d)
	}

	// A tool that fails both forms reports the error.
	if err := os.WriteFile(tool, []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := l.List(context.Background()); err == nil {
		t.Fatal("failing tool reported no error")
	}
	// A tool that answers with no drives says so.
	if err := os.WriteFile(tool, []byte("#!/bin/sh\necho '{\"Controllers\":[]}'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := l.List(context.Background()); err == nil || !strings.Contains(err.Error(), "no drives") {
		t.Fatalf("empty tool: %v", err)
	}
}

// PERC 11 lists a non-RAID drive as state Onln, type JBOD (seen: PERC H755).
func TestNonRAIDType(t *testing.T) {
	out := `{"Controllers":[{"Command Status":{"Controller":0,"Status":"Success"},"Response Data":{"Drive Information":[` +
		`{"EID:Slt":"252:0","DID":3,"State":"Onln","DG":"-","Size":"894.252 GB","Intf":"SATA","Med":"SSD","SED":"N","PI":"N","SeSz":"512B","Model":"INTEL SSDSC2KB960GZ","Sp":"U","Type":"JBOD"},` +
		`{"EID:Slt":"252:1","DID":4,"State":"Onln","DG":0,"Intf":"SATA","Med":"SSD","Model":"INTEL SSDSC2KB960GZ","Type":"-"},` +
		`{"EID:Slt":"252:2","DID":5,"State":"JBOD","DG":"-","Intf":"SATA","Med":"SSD","Model":"INTEL SSDSC2KB960GZ"}]}}]}`
	drives, err := Parse([]byte(out), "perccli64")
	if err != nil || len(drives) != 3 {
		t.Fatalf("%v %+v", err, drives)
	}
	for i, want := range []bool{true, false, true} {
		if got := drives[i].NonRAID(); got != want {
			t.Errorf("%s: non-RAID %v", drives[i].Slot, got)
		}
	}
}
