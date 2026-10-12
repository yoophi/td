package ghstore

import "fmt"

// Adoption planning is a distinct no-overwrite decision. Execution remains
// blocked before API access until native command ownership, completion and
// inverse contracts all pin the original binding and retained observation.
func requireExecutableImportContract(items []ImportItem) error {
	for _, item := range items {
		if item.AdoptTargetID != "" {
			return fmt.Errorf("task adoption execution requires its versioned command/receipt contract; no API call or write attempted")
		}
	}
	return nil
}
