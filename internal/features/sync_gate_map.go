package features

// GateMapEntry records a concrete sync surface that must be gated.
type GateMapEntry struct {
	Feature string
	Surface string
	Notes   string
}

// SyncGateMap is the authoritative map of sync-related surfaces to feature flags.
// Paths refer to the sync implementation currently on the "sunc" branch.
var SyncGateMap = []GateMapEntry{
	{
		Feature: SyncCLI.Name,
		Surface: "cmd/auth.go",
		Notes:   "Gates SQLite td-sync authentication handlers; GitHub auth login/logout/status remain registered and use gh credentials independently of the flag",
	},
	{
		Feature: SyncCLI.Name,
		Surface: "cmd/sync.go",
		Notes:   "Gates SQLite sync transfers. GitHub sync refresh/status/tail/conflict diagnostics also run on the always-on parent without SQLite replay. Status and enable/disable remain reachable; GitHub toggles report their SQLite-only meaning without changing global settings.",
	},
	{
		Feature: SyncCLI.Name,
		Surface: "cmd/sync_conflicts.go",
		Notes:   "Gates SQLite td sync conflicts; GitHub device-local recorded CLI conflicts remain available on the always-on parent",
	},
	{
		Feature: SyncCLI.Name,
		Surface: "cmd/sync_tail.go",
		Notes:   "Gates SQLite td sync tail; GitHub observed activity tail remains available on the always-on parent",
	},
	{
		Feature: SyncCLI.Name,
		Surface: "cmd/sync_init.go",
		Notes:   "Gates the SQLite guided sync setup wizard; GitHub validation of existing store configuration remains available on the always-on parent",
	},
	{
		Feature: SyncCLI.Name,
		Surface: "cmd/project.go",
		Notes:   "Gates SQLite td-sync project handlers; GitHub project selection and diagnostics remain registered independently, and unfinished GitHub management operations fail before SQLite/td-sync access",
	},
	{
		Feature: SyncCLI.Name,
		Surface: "cmd/config.go",
		Notes:   "Gates sync.* settings only. The config parent and project store settings are always available.",
	},
	{
		Feature: SyncCLI.Name,
		Surface: "cmd/doctor.go",
		Notes:   "NO LONGER GATED: td doctor is a read-only diagnostic registered ungated so a stranded project can always be diagnosed (td-78b482). Entry retained for traceability; the SyncCLI gate no longer applies to it.",
	},
	{
		Feature: SyncAutosync.Name,
		Surface: "cmd/root.go#PersistentPreRun",
		Notes:   "Explicit override for startup push/pull hook (default: per-project sync config decides; td-a4c721)",
	},
	{
		Feature: SyncAutosync.Name,
		Surface: "cmd/root.go#PersistentPostRun",
		Notes:   "Explicit override for post-mutation autosync hook (default: per-project sync config decides; td-a4c721)",
	},
	{
		Feature: SyncAutosync.Name,
		Surface: "cmd/monitor.go",
		Notes:   "Explicit override for monitor periodic autosync loop (default: per-project sync config decides; td-a4c721)",
	},
	{
		Feature: SyncMonitorPrompt.Name,
		Surface: "pkg/monitor/commands.go#checkSyncPrompt",
		Notes:   "Gates first-run monitor sync prompt trigger",
	},
	{
		Feature: SyncMonitorPrompt.Name,
		Surface: "pkg/monitor/sync_prompt.go",
		Notes:   "Gates monitor sync prompt modal flows",
	},
	{
		Feature: SyncNotes.Name,
		Surface: "cmd/sync.go#syncEntityValidator",
		Notes:   "Gates notes entity handling during manual sync push/pull",
	},
	{
		Feature: SyncNotes.Name,
		Surface: "cmd/autosync.go",
		Notes:   "Gates notes entity handling during autosync push/pull",
	},
}
