package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRegularLowStartupFlagRefusesBeforeStore(t *testing.T) {
	for _, readOnly := range []bool{false, true} {
		t.Run(map[bool]string{false: "no_opt_in", true: "read_only"}[readOnly], func(t *testing.T) {
			optIn := ""
			if readOnly {
				optIn = "1"
			}
			t.Setenv("WHATSMEOW_REGULAR_LOW_WEB_COMPAT", optIn)
			dir := filepath.Join(t.TempDir(), "not-created")
			cmd := newSyncCmd(&rootFlags{readOnly: readOnly, storeDir: dir})
			cmd.SetArgs([]string{"--regular-low-full-sync"})
			err := cmd.Execute()
			want := "requires WHATSMEOW_REGULAR_LOW_WEB_COMPAT=1"
			if readOnly {
				want = "read-only"
			}
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Fatal("startup guard", err)
			}
			if _, err := os.Stat(dir); !os.IsNotExist(err) {
				t.Fatal("refusal opened store", err)
			}
		})
	}
	flag := newSyncCmd(&rootFlags{}).Flags().Lookup("regular-low-full-sync")
	if flag == nil || flag.DefValue != "false" {
		t.Fatal("anchor must be explicit")
	}
}
