// SPDX-License-Identifier: Apache-2.0

// Command library is the README's library example: plan with RAID reset
// (--inventory --raid-reset), then erase (--yes --raid-reset).
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"

	"github.com/rayy3535/cryptoerase"
)

func main() {
	ctx := context.Background()

	// --inventory --raid-reset: find the drives and plan the erase,
	// including the RAID reset. Changes nothing.
	plan, err := cryptoerase.Run(ctx, cryptoerase.Options{
		Mode:      cryptoerase.ModeInventory,
		RAIDReset: true,
	})
	if err != nil {
		log.Fatal(err) // the run could not start, e.g. hdparm or storcli64 missing
	}
	for _, d := range plan.Drives {
		fmt.Println(d.Device, d.Model, d.Result, d.Reason) // PLANNED, UNHANDLED or FAIL
	}
	if plan.Result != "PASS" {
		log.Fatalf("not every drive can be erased: %s", plan.Result)
	}

	// --yes --raid-reset: delete the RAID virtual disks, then erase and
	// verify every drive.
	rep, err := cryptoerase.Run(ctx, cryptoerase.Options{
		Mode:      cryptoerase.ModeErase,
		Confirm:   true, // --yes
		RAIDReset: true,
		JobID:     "RECLAIM-1234",
	})
	if err != nil {
		log.Fatal(err)
	}
	evidence, err := json.MarshalIndent(rep, "", "  ")
	if err != nil {
		log.Fatal(err)
	}
	if err := os.WriteFile("erase-report.json", evidence, 0o600); err != nil {
		log.Fatal(err)
	}
	os.Exit(rep.ExitCode()) // 0 PASS, 1 FAIL, 2 INCOMPLETE (some drives UNHANDLED)
}
