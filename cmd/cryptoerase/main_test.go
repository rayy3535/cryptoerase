// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestVersion(t *testing.T) {
	var out, errb bytes.Buffer
	if code := run([]string{"--version"}, &out, &errb); code != 0 || !strings.HasPrefix(out.String(), "cryptoerase ") {
		t.Fatalf("code %d out %q", code, out.String())
	}
}

func TestRefusesWithoutYes(t *testing.T) {
	var out, errb bytes.Buffer
	if code := run(nil, &out, &errb); code != 1 || !strings.Contains(errb.String(), "refusing to erase without --yes") {
		t.Fatalf("code %d stderr %q", code, errb.String())
	}
}

func TestBadArguments(t *testing.T) {
	var out, errb bytes.Buffer
	if code := run([]string{"--no-such-flag"}, &out, &errb); code != 1 {
		t.Fatalf("code %d", code)
	}
	if code := run([]string{"--inventory", "extra"}, &out, &errb); code != 1 {
		t.Fatalf("code %d", code)
	}
	if code := run([]string{"--inventory", "--fw-policy", "/nonexistent"}, &out, &errb); code != 1 {
		t.Fatalf("code %d", code)
	}
}
