package ghstore

import (
	"encoding/json"
	"fmt"
)

// Freeze the complete portable envelope before any remote observation. The
// command must use these same source values for allocation, replanning, graph
// projection and provenance, including after a partial result. Do not route
// through the legacy import decoder: it normalizes empty handoffs and would
// change the digest of an already parsed input.
func freezeImportItems(items []ImportItem) ([]ImportItem, error) {
	data, err := json.Marshal(items)
	if err != nil {
		return nil, fmt.Errorf("encode import command input: %w", err)
	}
	type value ImportItem
	var values []value
	if err := json.Unmarshal(data, &values); err != nil {
		return nil, fmt.Errorf("freeze import command input: %w", err)
	}
	frozen := make([]ImportItem, len(values))
	for i := range values {
		frozen[i] = ImportItem(values[i])
	}
	if err := ValidateImportItems(frozen); err != nil {
		return nil, err
	}
	return frozen, nil
}
