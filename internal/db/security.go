package db

import "github.com/marcus/td/internal/auditlog"

// Compatibility wrappers: audit files are device-local and never require an issue database.
type SecurityEvent = auditlog.SecurityEvent

func LogSecurityEvent(baseDir string, event SecurityEvent) error {
	return auditlog.LogSecurityEvent(baseDir, event)
}
func ReadSecurityEvents(baseDir string) ([]SecurityEvent, error) {
	return auditlog.ReadSecurityEvents(baseDir)
}
func ClearSecurityEvents(baseDir string) error { return auditlog.ClearSecurityEvents(baseDir) }
