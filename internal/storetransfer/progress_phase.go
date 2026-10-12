package storetransfer

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"maps"
	"reflect"
	"regexp"
)

// PhaseReceipt is durable controller evidence recorded after native checks.
// It never authorizes skipping those checks on resume, nor upgrades an
// uncertain transport response into a received one.
type PhaseReceipt struct {
	Name         string            `json:"name"`
	NativeRootID string            `json:"native_root_id,omitempty"`
	Mappings     map[string]string `json:"mappings"`
	EntityProofs map[string]string `json:"entity_proofs,omitempty"`
}

var phaseNames = []string{"source_archive", "tasks", "entities", "completion_archive"}
var phaseRootID = regexp.MustCompile(`^ghc-[1-9][0-9]*$`)

func validatePhaseReceipts(phases []PhaseReceipt, c *Checkpoint) error {
	if len(phases) == 0 {
		return nil
	}
	if len(phases) > len(phaseNames) {
		return fmt.Errorf("invalid transfer phase count")
	}
	source, err := c.Source.decode()
	if err != nil {
		return err
	}
	preview, err := source.Preview()
	if err != nil {
		return err
	}
	kinds, operations := map[string]string{}, map[string]bool{}
	for _, identity := range preview.Identities {
		kinds[identity.Key] = identity.Kind
		if identity.Kind == "note" || identity.Kind == "board" {
			hash := sha256.Sum256([]byte(identity.Kind + "\x00" + identity.Key))
			operations[c.OperationID+"-"+hex.EncodeToString(hash[:])] = true
		}
	}
	for i, phase := range phases {
		if phase.Name != phaseNames[i] || phase.Mappings == nil {
			return fmt.Errorf("invalid transfer phase order/mappings")
		}
		archive := phase.Name == "source_archive" || phase.Name == "completion_archive"
		if archive && !phaseRootID.MatchString(phase.NativeRootID) || !archive && phase.NativeRootID != "" {
			return fmt.Errorf("invalid native phase root reference")
		}
		for key, id := range phase.Mappings {
			kind := kinds[key]
			validID := kind == "task" && phaseTaskID.MatchString(id) || kind == "note" && phaseNoteID.MatchString(id) || kind == "board" && (phaseBoardID.MatchString(id) || id == "bd-all-issues")
			if !validID || phase.Name == "tasks" && kind != "task" {
				return fmt.Errorf("invalid transfer phase mapping")
			}
		}
		if phase.Name == "source_archive" && !reflect.DeepEqual(phase.Mappings, c.Bindings) || (phase.Name == "entities" || phase.Name == "completion_archive") && len(phase.Mappings) != len(kinds) {
			return fmt.Errorf("transfer phase differs from original source/bindings")
		}
		for operation, proof := range phase.EntityProofs {
			if phase.Name != "entities" || !operations[operation] || proof != "verified" && proof != "observed" {
				return fmt.Errorf("invalid transfer entity proof")
			}
		}
	}
	return nil
}

var phaseTaskID = regexp.MustCompile(`^gh-[1-9][0-9]*$`)
var phaseNoteID = regexp.MustCompile(`^nt-gh-[1-9][0-9]*$`)
var phaseBoardID = regexp.MustCompile(`^bd-gh-[1-9][0-9]*$`)

func (r *progressRecorder) recordPhase(phase PhaseReceipt) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	phase.Mappings = maps.Clone(phase.Mappings)
	phase.EntityProofs = maps.Clone(phase.EntityProofs)
	if len(phase.EntityProofs) == 0 {
		phase.EntityProofs = nil
	}
	for _, previous := range r.current.Phases {
		if previous.Name == phase.Name {
			if !reflect.DeepEqual(previous, phase) {
				return fmt.Errorf("fresh native result differs from original %s phase; no phase rewritten", phase.Name)
			}
			return nil
		}
	}
	next, err := r.next()
	if err != nil {
		return err
	}
	next.Phases = append(next.Phases, phase)
	if err := r.save(next); err != nil {
		return fmt.Errorf("native %s checks completed but local phase receipt is unverified; earlier remote writes may remain; no automatic retry: %w", phase.Name, err)
	}
	return nil
}
