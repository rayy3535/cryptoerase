// SPDX-License-Identifier: Apache-2.0

package cryptoerase

import (
	"bufio"
	"fmt"
	"regexp"
	"strings"
)

// FirmwareRule sets a minimum firmware for drives whose model and firmware
// both match. Versions are compared within the matched firmware family with
// CompareVersions.
type FirmwareRule struct {
	Model     *regexp.Regexp
	Firmware  *regexp.Regexp
	Min       string
	Reference string
}

func (r FirmwareRule) String() string {
	return fmt.Sprintf("%s ; %s ; %s ; %s", r.Model, r.Firmware, r.Min, r.Reference)
}

// DefaultFirmwarePolicyText is the built-in table, one rule per line:
// model regex ; firmware regex ; minimum ; reference.
const DefaultFirmwarePolicyText = `
# Intel / Solidigm DC P4510 and P4610, Dell-branded firmware line
P4510|P4610|SSDPE2KX|SSDPE2KE ; ^VDV1DP ; VDV1DP25 ; Dell DSA-2022-203 (INTEL-SA-00535 / SOLIDIGM-SA-00563)
# Same drives, Intel firmware line
P4510|P4610|SSDPE2KX|SSDPE2KE ; ^VDV10  ; VDV10184 ; INTEL-SA-00535 (fixed VDV10182), SOLIDIGM-SA-00563 (VDV10184)
`

// DefaultFirmwarePolicy returns the built-in rules.
func DefaultFirmwarePolicy() []FirmwareRule {
	r, err := ParseFirmwarePolicy(DefaultFirmwarePolicyText)
	if err != nil {
		panic(err)
	}
	return r
}

// ParseFirmwarePolicy parses rules in the DefaultFirmwarePolicyText format.
func ParseFirmwarePolicy(text string) ([]FirmwareRule, error) {
	var rules []FirmwareRule
	sc := bufio.NewScanner(strings.NewReader(text))
	n := 0
	for sc.Scan() {
		n++
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		f := strings.SplitN(line, ";", 4)
		if len(f) < 3 {
			return nil, fmt.Errorf("firmware policy line %d: want 'model ; firmware ; min ; reference'", n)
		}
		for i := range f {
			f[i] = strings.TrimSpace(f[i])
		}
		m, err := regexp.Compile(f[0])
		if err != nil {
			return nil, fmt.Errorf("firmware policy line %d: model: %w", n, err)
		}
		fw, err := regexp.Compile(f[1])
		if err != nil {
			return nil, fmt.Errorf("firmware policy line %d: firmware: %w", n, err)
		}
		r := FirmwareRule{Model: m, Firmware: fw, Min: f[2]}
		if len(f) == 4 {
			r.Reference = f[3]
		}
		rules = append(rules, r)
	}
	return rules, sc.Err()
}

// CheckFirmware applies the first rule matching model and firmware.
func CheckFirmware(rules []FirmwareRule, model, firmware string) FirmwareCheck {
	for _, r := range rules {
		if !r.Model.MatchString(model) || !r.Firmware.MatchString(firmware) {
			continue
		}
		if CompareVersions(firmware, r.Min) < 0 {
			return FirmwareCheck{Status: "fail", Detail: fmt.Sprintf("firmware %s is below %s (%s); upgrade firmware, then rerun", firmware, r.Min, r.Reference)}
		}
		return FirmwareCheck{Status: "pass", Detail: fmt.Sprintf(">= %s (%s)", r.Min, r.Reference)}
	}
	return FirmwareCheck{Status: "no_rule"}
}

// CompareVersions compares like `sort -V`: runs of digits numerically, other
// runs byte-wise. Returns -1, 0 or 1.
func CompareVersions(a, b string) int {
	ra, rb := splitRuns(a), splitRuns(b)
	for i := 0; i < len(ra) && i < len(rb); i++ {
		x, y := ra[i], rb[i]
		xd, yd := isDigits(x), isDigits(y)
		switch {
		case xd && yd:
			xn, yn := strings.TrimLeft(x, "0"), strings.TrimLeft(y, "0")
			if len(xn) != len(yn) {
				return sign(len(xn) - len(yn))
			}
			if c := strings.Compare(xn, yn); c != 0 {
				return c
			}
		default:
			if c := strings.Compare(x, y); c != 0 {
				return c
			}
		}
	}
	return sign(len(ra) - len(rb))
}

func splitRuns(s string) []string {
	var runs []string
	start := 0
	for i := 1; i <= len(s); i++ {
		if i == len(s) || isDigit(s[i]) != isDigit(s[i-1]) {
			runs = append(runs, s[start:i])
			start = i
		}
	}
	return runs
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

func isDigits(s string) bool { return s != "" && strings.Trim(s, "0123456789") == "" }

func sign(v int) int {
	switch {
	case v < 0:
		return -1
	case v > 0:
		return 1
	}
	return 0
}
