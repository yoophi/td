package ghstore

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"

	"github.com/marcus/td/internal/models"
)

func validatePreservedHistoryActivity(activity models.Activity) error {
	doc := activity.PreservedHistory
	if doc == nil {
		return nil
	}
	if !activity.Native || activity.Kind != "comment" || activity.OperationID != "" || activity.SessionID != "" {
		return fmt.Errorf("preserved history must remain a passive native comment")
	}
	identity := importReceipt{Version: 1, SourceKey: doc.SourceKey, SourceDigest: doc.SourceDigest, TargetID: "gh-1", TargetBasis: doc.ContentDigest}
	if err := identity.validate(); err != nil {
		return err
	}
	if (doc.Kind != "source" && doc.Kind != "destination") || doc.PartsExpected < 1 || doc.PartsPresent < 1 || doc.PartsPresent > doc.PartsExpected || doc.Complete != (doc.PartsPresent == doc.PartsExpected) {
		return fmt.Errorf("invalid preserved history part counts")
	}
	if !doc.Complete {
		if len(doc.Data) != 0 {
			return fmt.Errorf("incomplete preserved history cannot claim decoded data")
		}
		return nil
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, doc.Data); err != nil {
		return fmt.Errorf("invalid preserved history JSON: %w", err)
	}
	data := compact.Bytes()
	if fmt.Sprintf("%x", sha256.Sum256(data)) != doc.ContentDigest || (len(data)-1)/importProvenanceChunkBytes+1 != doc.PartsExpected {
		return fmt.Errorf("preserved history digest/part count differs")
	}
	if doc.Kind == "source" && doc.SourceDigest != doc.ContentDigest {
		return fmt.Errorf("preserved source history digest differs")
	}
	return nil
}
