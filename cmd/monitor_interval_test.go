package cmd

import (
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
)

func TestMonitorIntervalDefaultAndMinimum(t *testing.T) {
	if got, err := monitorCmd.Flags().GetDuration("interval"); err != nil || got != time.Minute {
		t.Fatalf("default interval = %s, error = %v", got, err)
	}
	for _, value := range []string{"29s", "0s", "-1s"} {
		command := &cobra.Command{}
		command.Flags().Duration("interval", time.Minute, "")
		if err := command.Flags().Set("interval", value); err != nil {
			t.Fatal(err)
		}
		// Invalid intervals must fail before opening the store or starting a TUI.
		if err := monitorCmd.RunE(command, nil); err == nil || !strings.Contains(err.Error(), "at least 30s") {
			t.Fatalf("%s: expected explicit interval error, got %v", value, err)
		}
	}
}
