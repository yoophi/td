package ghstore

import (
	"context"
	"encoding/json"
	"github.com/marcus/td/internal/models"
	"strings"
	"testing"
	"time"
)

func TestImportAdoptionPlanningRetainsDestinationAndPinsBinding(t *testing.T) {
	c, _, writes, _ := importBatchFixture(t)
	source := importTestItem("td-source")
	source.SourceNamespace = "original"
	source.AdoptTargetID = "gh-1"
	before, err := c.Get(context.Background(), "gh-1")
	if err != nil {
		t.Fatal(err)
	}
	plan, err := c.PlanImport(context.Background(), []ImportItem{source}, false)
	if err != nil || len(plan.Rows) != 1 || plan.Rows[0].Action != "adopt" || plan.Rows[0].TargetID != "gh-1" || plan.Rows[0].basis == "" || *writes != 0 {
		t.Fatal(plan, err)
	}
	after, err := c.Get(context.Background(), "gh-1")
	if err != nil || after.Title != before.Title {
		t.Fatal("planning changed destination", after, err)
	}
	digest, err := source.Digest()
	if err != nil {
		t.Fatal(err)
	}
	source.AdoptTargetID = "gh-2"
	otherDigest, err := source.Digest()
	if err != nil || digest == otherDigest {
		t.Fatal("source digest omitted destination decision", err)
	}
}

func TestImportAdoptionRejectsWrongKindsDuplicatesAndForceBeforeWrites(t *testing.T) {
	c, _, writes, _ := importBatchFixture(t)
	first, second := importTestItem("td-first"), importTestItem("td-second")
	first.SourceNamespace, second.SourceNamespace = "original", "original"
	first.AdoptTargetID, second.AdoptTargetID = "gh-1", "gh-1"
	if _, err := c.PlanImport(context.Background(), []ImportItem{first, second}, false); err == nil {
		t.Fatal("duplicate adoption accepted")
	}
	if _, err := c.PlanImport(context.Background(), []ImportItem{first}, true); err == nil {
		t.Fatal("force changed adoption semantics")
	}
	for _, id := range []string{"gh-999", "gh-01", "bd-gh-1", "gh-0"} {
		first.AdoptTargetID = id
		if _, err := c.PlanImport(context.Background(), []ImportItem{first}, false); err == nil {
			t.Fatal("invalid/absent task accepted", id)
		}
	}
	if *writes != 0 {
		t.Fatal("rejected planning wrote to GitHub")
	}
}

func TestImportAdoptionCanonicalInputAndExecutionGuard(t *testing.T) {
	item := importTestItem("td-source")
	item.SourceNamespace = "original"
	item.AdoptTargetID = "gh-1"
	data, err := json.Marshal([]ImportItem{item})
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := ParseImportJSON(data)
	if err != nil || decoded[0].AdoptTargetID != "gh-1" {
		t.Fatal(decoded, err)
	}
	var client *Client // No client proves execution is rejected before API access.
	if _, err := client.Import(context.Background(), decoded, false); err == nil {
		t.Fatal("adoption executed without its native journal contract")
	}
}

func TestImportAdoptionPrivateExecutionPreservesTargetAndMapsNewParent(t *testing.T) {
	c, issues, writes, comments := importBatchFixture(t)
	before, err := json.Marshal(issues[1])
	if err != nil {
		t.Fatal(err)
	}
	adopted, child := importTestItem("td-adopted"), importTestItem("td-new")
	adopted.SourceNamespace, child.SourceNamespace = "original", "original"
	adopted.AdoptTargetID = "gh-1"
	adopted.Issue.Title = "must remain source provenance"
	adopted.Issue.ParentID = child.Issue.ID // Source-owned relationship is not applied.
	child.Issue.ParentID = ""               // Avoid a source cycle; reference adoption as dependency.
	child.Dependencies = []models.IssueDependency{{DependsOnID: adopted.Issue.ID, RelationType: "depends_on"}}
	result, err := c.executeImport(context.Background(), []ImportItem{adopted, child}, false)
	if err != nil || result.Created != 1 || result.Applied != 1 || result.Completed != 1 || result.Skipped != 1 || *writes != 2 {
		t.Fatal(result, err, *writes)
	}
	after, err := json.Marshal(issues[1])
	if err != nil || string(before) != string(after) {
		t.Fatal("adopted task changed", err)
	}
	for _, comment := range *comments {
		if strings.HasSuffix(comment.IssueURL, "/1") {
			t.Fatal("adopted task received source history/checkpoints")
		}
	}
	newTask, err := c.Get(context.Background(), "gh-4")
	if err != nil || len(newTask.Details.Dependencies) != 1 || newTask.Details.Dependencies[0] != "gh-1" {
		t.Fatal(newTask, err)
	}
}

func TestImportAdoptionRejectsRetainedEditAfterAllocation(t *testing.T) {
	c, issues, writes, _ := importBatchFixture(t)
	adopted, child := importTestItem("td-adopted"), importTestItem("td-new")
	adopted.SourceNamespace, child.SourceNamespace = "original", "original"
	adopted.AdoptTargetID = "gh-1"
	child.Issue.ParentID = adopted.Issue.ID
	base := c.run
	c.run = func(ctx context.Context, dir string, payload []byte, args ...string) ([]byte, error) {
		data, err := base(ctx, dir, payload, args...)
		if err == nil && args[4] == "POST" && strings.HasSuffix(args[5], "/issues") {
			issues[1]["title"] = "external retained edit"
		}
		return data, err
	}
	result, err := c.executeImport(context.Background(), []ImportItem{adopted, child}, false)
	if err == nil || result.Created != 1 || result.Applied != 0 || result.Completed != 0 || *writes != 1 {
		t.Fatal("changed adoption basis was replaced", result, err, *writes)
	}
}

func TestImportAdoptionPreparedCommandPinsNativeBeforeAndRequiresV35(t *testing.T) {
	c, nativeRows, writes, _ := importBatchFixture(t)
	clock := time.Date(2026, 10, 11, 0, 0, 0, 0, time.UTC)
	for _, issue := range nativeRows {
		issue["created_at"], issue["updated_at"] = clock, clock
		issue["user"] = map[string]string{"login": "original-author"}
	}
	adopted, created := importTestItem("td-adopted"), importTestItem("td-new")
	adopted.SourceNamespace, created.SourceNamespace = "original", "original"
	adopted.AdoptTargetID = "gh-1"
	created.Issue.ParentID = adopted.Issue.ID
	items := []ImportItem{adopted, created}
	plan, err := c.PlanImport(context.Background(), items, false)
	if err != nil {
		t.Fatal(err)
	}
	command, err := prepareImportCommand(items, false, plan, map[string][]nativeStateEvent{"gh-1": {}})
	if err != nil {
		t.Fatal(err)
	}
	if command.Members[0].Action != "adopt" || command.Members[0].Before == nil || command.Members[0].Before.Issue.Number != 1 || command.Members[1].TargetID != "" {
		t.Fatal("adoption invented/replaced original destination", command)
	}
	action := ActionJournal{OperationID: "td-fixed-command", SessionID: "ses_actor", CarrierID: "gh-999", Action: "import", Phase: "prepared", ImportCommand: command}
	body, err := renderActionJournal(action)
	if err != nil || !strings.Contains(body, actionImportAdoptionStart) {
		t.Fatal(body, err)
	}
	native := journalCommentFixture(t, action)
	native.IssueURL = "https://api.github.com/repos/owner/repo/issues/999"
	if _, err := decodeActionJournal(native, "gh-999"); err != nil {
		t.Fatal(err)
	}
	native.Body = strings.Replace(native.Body, actionImportAdoptionStart, actionImportCommandStart, 1)
	if _, err := decodeActionJournal(native, "gh-999"); err == nil {
		t.Fatal("v33 accepted destination adoption")
	}
	root, parts, _ := importArchiveNativeFixture(t, action)
	collection, err := collectJournalParts(parts)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := decodeInlineJournalFromCollection(root, "gh-999", collection)
	if err != nil || decoded.ImportCommand.Members[0].Action != "adopt" {
		t.Fatal("archive transport lost adoption contract", err)
	}
	// The old transport must not ambiguously claim the new decision contract.
	logical := root.Body
	if index := strings.Index(root.Body, actionCompressedJournalStart); index >= 0 {
		logical, err = decodeJournalEnvelope(root.Body[index:])
		if err != nil {
			t.Fatal(err)
		}
	}
	if !strings.Contains(logical, actionImportAdoptionArchiveStart) {
		t.Fatal("wrong archive contract version")
	}

	if *writes != 0 {
		t.Fatal("contract preparation wrote to GitHub")
	}
}
