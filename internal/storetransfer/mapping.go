package storetransfer

import "fmt"

type Assignment struct {
	Kind      string `json:"kind"`
	SourceID  string `json:"source_id"`
	SourceKey string `json:"source_key"`
	Action    string `json:"action"`
	TargetID  string `json:"target_id,omitempty"`
}

// IdentityPlan describes mappings against validated observed input only. It
// does not prove destination access, receipt provenance, or write authority.
// New target IDs remain unresolved until confirmed creation receipts exist.
type IdentityPlan struct {
	SourceSHA256 string       `json:"source_sha256"`
	TargetSHA256 string       `json:"target_sha256"`
	Assignments  []Assignment `json:"assignments"`
}

func PlanIdentities(source, target *Source, bindings map[string]string) (*IdentityPlan, error) {
	if source == nil || target == nil {
		return nil, fmt.Errorf("source and target observations are required")
	}
	s, err := source.Envelope.decode()
	if err != nil {
		return nil, err
	}
	t, err := target.Envelope.decode()
	if err != nil {
		return nil, err
	}
	if s.Envelope.Store == t.Envelope.Store && s.Envelope.Namespace == t.Envelope.Namespace {
		return nil, fmt.Errorf("source and destination identity scopes are identical")
	}
	sp, err := s.validateReferences()
	if err != nil {
		return nil, err
	}
	tp, err := t.validateReferences()
	if err != nil {
		return nil, err
	}
	sourceKeys := map[string]bool{}
	for _, identity := range sp.Identities {
		sourceKeys[identity.Key] = true
	}
	for key, id := range bindings {
		if !sourceKeys[key] || id == "" {
			return nil, fmt.Errorf("unknown source key or empty target binding %q", key)
		}
	}
	targetKinds := map[string]string{}
	for _, identity := range tp.Identities {
		targetKinds[identity.ID] = identity.Kind
	}
	// Both backends provide All Issues, even before a native GitHub carrier
	// exists. Its logical identity is stable; do not invent a carrier number.
	if kind, exists := targetKinds["bd-all-issues"]; exists && (kind != "board" || !builtinBoard(t, "bd-all-issues")) {
		return nil, fmt.Errorf("destination reserved builtin ID is occupied by a non-builtin entity")
	}
	targetKinds["bd-all-issues"] = "board"
	used := map[string]string{}
	plan := &IdentityPlan{SourceSHA256: s.Envelope.SHA256, TargetSHA256: t.Envelope.SHA256, Assignments: []Assignment{}}
	for _, identity := range sp.Identities {
		assignment := Assignment{Kind: identity.Kind, SourceID: identity.ID, SourceKey: identity.Key, Action: "create"}
		if archivedSQLiteBoard(s, t.Envelope.Store, identity.ID) {
			if _, exists := bindings[identity.Key]; exists {
				return nil, fmt.Errorf("deleted GitHub board %q cannot adopt a live SQLite board", identity.ID)
			}
			assignment.Action = "archive"
			plan.Assignments = append(plan.Assignments, assignment)
			continue
		}
		if id, exists := bindings[identity.Key]; exists {
			assignment.Action = "adopt"
			assignment.TargetID = id
		}
		if builtinBoard(s, identity.ID) {
			if assignment.TargetID != "" && assignment.TargetID != "bd-all-issues" {
				return nil, fmt.Errorf("builtin board cannot map to %q", assignment.TargetID)
			}
			assignment.Action = "builtin"
			assignment.TargetID = "bd-all-issues"
		} else if assignment.TargetID == "bd-all-issues" {
			return nil, fmt.Errorf("custom source entity cannot replace builtin board")
		}
		if assignment.TargetID != "" {
			if targetKinds[assignment.TargetID] != assignment.Kind {
				return nil, fmt.Errorf("target %q missing or has a different entity kind", assignment.TargetID)
			}
			if prior, exists := used[assignment.TargetID]; exists {
				return nil, fmt.Errorf("target %q is bound by both %q and %q", assignment.TargetID, prior, assignment.SourceKey)
			}
			used[assignment.TargetID] = assignment.SourceKey
		}
		plan.Assignments = append(plan.Assignments, assignment)
	}
	return plan, nil
}

func archivedSQLiteBoard(source *Source, targetStore, id string) bool {
	if targetStore == "sqlite" && source.GitHub != nil {
		for _, b := range source.GitHub.Boards {
			if b.ID == id {
				return b.Details.DeletedAt != nil
			}
		}
	}
	return false
}

func builtinBoard(source *Source, id string) bool {
	if source.SQLite != nil {
		for _, row := range source.SQLite.Boards {
			if row.Board.ID == id {
				return row.Board.IsBuiltin
			}
		}
	}
	if source.GitHub != nil {
		for _, row := range source.GitHub.Boards {
			if row.ID == id {
				return row.IsBuiltin
			}
		}
	}
	return false
}

// KnownTaskMappings intentionally omits pending creations. Query rewrites
// containing those references must wait for checked receipt assignments.
func (p *IdentityPlan) KnownTaskMappings() map[string]string {
	result := map[string]string{}
	if p != nil {
		for _, assignment := range p.Assignments {
			if assignment.Kind == "task" && assignment.TargetID != "" {
				result[assignment.SourceID] = assignment.TargetID
			}
		}
	}
	return result
}
