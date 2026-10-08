package auditlog

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLocalAuditRoundTripClearAndInvalidJSON(t *testing.T) {
	dir := t.TempDir()
	message := "Fixture reason with \"quotes\", newline\n한국어"
	if err := LogSecurityEvent(dir, SecurityEvent{IssueID: "gh-1", SessionID: "fixture-session", Reason: message, Scope: "device-local", Outcome: "confirmed"}); err != nil {
		t.Fatal(err)
	}
	events, err := ReadSecurityEvents(dir)
	if err != nil || len(events) != 1 || events[0].Reason != message || events[0].Timestamp.IsZero() {
		t.Fatalf("%+v %v", events, err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".todos/issues.db")); !os.IsNotExist(err) {
		t.Fatal("audit opened issue database")
	}
	if err := ClearSecurityEvents(dir); err != nil {
		t.Fatal(err)
	}
	events, err = ReadSecurityEvents(dir)
	if err != nil || len(events) != 0 {
		t.Fatalf("%+v %v", events, err)
	}
	if err := os.WriteFile(filepath.Join(dir, SecurityEventsFile), []byte("{}\ninvalid\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadSecurityEvents(dir); err == nil || !strings.Contains(err.Error(), "line 2") {
		t.Fatalf("invalid audit data ignored: %v", err)
	}
}

func TestLocalAuditFileWriteFailure(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, SecurityEventsFile), 0700); err != nil {
		t.Fatal(err)
	}
	if err := LogSecurityEvent(dir, SecurityEvent{IssueID: "gh-1", SessionID: "fixture", Reason: "Fixture"}); err == nil {
		t.Fatal("audit file failure hidden")
	}
}
