package ghstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/marcus/td/internal/models"
	"strconv"
	"strings"
	"testing"
	"time"
)

func undoChainFixture(t *testing.T) (*ActionJournalRecord, *Record, []ActionJournalRecord) {
	t.Helper()
	state := func(title string, tick int) *ActionIssueState {
		s := journalStateFixture(t, title)
		s.UpdatedAt = s.UpdatedAt.Add(time.Duration(tick) * time.Second)
		s.Revision, _ = s.checksum()
		return &s
	}
	base := ActionJournalRecord{ActionJournal: ActionJournal{OperationID: "op_a", SessionID: "ses_actor", IssueID: "gh-1", CarrierID: "gh-9", Action: "update", Phase: "applied", Before: state("Original", 0), After: state("A", 1)}, ID: "ghc-101", Author: "github-author"}
	b := base
	b.ID, b.OperationID, b.Phase, b.UndoOperationID = "ghc-102", "op_b", "undone", "undo_b"
	b.Before, b.After, b.Restored = base.After, state("B", 2), state("A", 3)
	c := b
	c.ID, c.OperationID, c.UndoOperationID = "ghc-103", "op_c", "undo_c"
	c.Before, c.After, c.Restored = b.Restored, state("C", 4), state("A", 5)
	current := &Record{nativeBody: c.Restored.Body, nativeState: c.Restored.State}
	current.ID, current.Number, current.Title, current.Labels, current.UpdatedAt = "gh-1", 1, c.Restored.Title, c.Restored.Labels, c.Restored.UpdatedAt
	return &base, current, []ActionJournalRecord{base, b, c}
}

func TestActionUndoReceiptChainRequiresExactUnambiguousProof(t *testing.T) {
	action, current, history := undoChainFixture(t)
	plan, proof, err := planUndoThroughReceipts(action, current, "ses_actor", "github-author", history)
	if err != nil || len(proof) != 2 || plan.Payload["title"] != "Original" {
		t.Fatal(plan, proof, err)
	}
	if action.After.UpdatedAt.Equal(current.UpdatedAt) {
		t.Fatal("original snapshot was rewritten")
	}
	for _, mode := range []string{"external-clock", "external-title", "missing", "duplicate", "wrong-author", "wrong-session", "wrong-carrier", "prepared", "wrong-restoration", "selected-edited", "cycle"} {
		t.Run(mode, func(t *testing.T) {
			a, r, h := undoChainFixture(t)
			switch mode {
			case "external-clock":
				r.UpdatedAt = r.UpdatedAt.Add(time.Second)
			case "external-title":
				r.Title = "Native edit"
			case "missing":
				h = h[:2]
			case "duplicate":
				d := h[1]
				d.ID, d.OperationID = "ghc-104", "op_d"
				h = append(h, d)
			case "wrong-author":
				h[1].Author = "other"
			case "wrong-session":
				h[1].SessionID = "ses_other"
			case "wrong-carrier":
				h[1].CarrierID = "gh-8"
			case "prepared":
				h[1].Phase = "undo_prepared"
				h[1].Restored = nil
			case "wrong-restoration":
				s := journalStateFixture(t, "Wrong")
				h[1].Restored = &s
			case "selected-edited":
				h[0].Author = "other"
			case "cycle":
				h[1].Restored = h[1].Before
			}
			_, _, err := planUndoThroughReceipts(a, r, "ses_actor", "github-author", h)
			var conflict *ConflictError
			if !errors.As(err, &conflict) {
				t.Fatal("unproven chain accepted", err)
			}
		})
	}
	if err := verifyUndoWitnesses(proof, history); err != nil {
		t.Fatal(err)
	}
	history[1].Author = "changed"
	if err := verifyUndoWitnesses(proof, history); err == nil {
		t.Fatal("edited witness accepted")
	}
}

func TestActionUndoReceiptChainExecutesAndRechecksBeforeTargetWrite(t *testing.T) {
	for _, mode := range []string{"success", "external-clock", "witness-edited", "ambiguous-after-intent", "history-denied", "comment-success", "comment-present", "comment-restored-after-intent"} {
		t.Run(mode, func(t *testing.T) {
			a, current, history := undoChainFixture(t)
			if strings.HasPrefix(mode, "comment-") {
				b := &history[1]
				b.Action = "comment_create"
				after := *b.Before
				after.UpdatedAt = b.After.UpdatedAt
				after.Revision, _ = after.checksum()
				b.After = &after
				body, err := renderActivity(activityData{Kind: "comment", OperationID: b.OperationID, SessionID: b.SessionID, Message: "Owned comment"})
				if err != nil {
					t.Fatal(err)
				}
				b.CommentInput = &ActionCommentInput{Body: body}
				b.CommentAfter = &ActionCommentState{ID: "ghc-201", IssueID: b.IssueID, Body: body, Author: b.Author, CreatedAt: after.UpdatedAt, UpdatedAt: after.UpdatedAt}
				b.CommentAfter.Revision, _ = b.CommentAfter.checksum()
			}
			comments := map[int]apiComment{}
			for _, record := range history {
				body, err := renderActionJournal(record.ActionJournal)
				if err != nil {
					t.Fatal(err)
				}
				n, _ := CommentNumber(record.ID)
				c := apiComment{ID: n, Body: body, IssueURL: "https://api.github.com/repos/owner/repo/issues/9", CreatedAt: current.UpdatedAt, UpdatedAt: current.UpdatedAt}
				c.User.Login = record.Author
				comments[int(n)] = c
			}
			restoreNativeComment := func() {
				b := history[1].CommentAfter
				comment := apiComment{ID: 201, IssueURL: "https://api.github.com/repos/owner/repo/issues/1", Body: b.Body, CreatedAt: b.CreatedAt, UpdatedAt: b.UpdatedAt}
				comment.User.Login = b.Author
				comments[201] = comment
			}
			if mode == "comment-present" {
				restoreNativeComment()
			}
			body, err := encodeBody("Journal", metadata{EntityKind: "journal", Type: models.TypeTask, Priority: models.PriorityP2})
			if err != nil {
				t.Fatal(err)
			}
			carrier := apiIssue{Number: 9, Title: "Journal", Body: body, State: "open", UpdatedAt: current.UpdatedAt}
			target := apiIssue{Number: 1, Title: current.Title, Body: current.nativeBody, State: current.nativeState, UpdatedAt: current.UpdatedAt}
			for _, label := range current.Labels {
				target.Labels = append(target.Labels, struct {
					Name string `json:"name"`
				}{label})
			}
			if mode == "external-clock" {
				target.UpdatedAt = target.UpdatedAt.Add(time.Second)
			}
			writes, intents, collections := 0, 0, 0
			client := &Client{repo: "owner/repo", run: func(_ context.Context, _ string, payload []byte, args ...string) ([]byte, error) {
				method, path := args[4], strings.TrimPrefix(args[5], "repos/owner/repo")
				encode := func(v any) ([]byte, error) { return json.Marshal(v) }
				switch {
				case path == "user":
					return encode(map[string]string{"login": "github-author"})
				case path == "/issues?state=all&per_page=100":
					return encode([][]apiIssue{{target, carrier}})
				case path == "/issues/comments?per_page=100":
					collections++
					if mode == "history-denied" {
						return nil, errors.New("HTTP 403 history denied")
					}
					page := []apiComment{}
					for _, c := range comments {
						page = append(page, c)
					}
					return encode([][]apiComment{page})
				case strings.HasPrefix(path, "/issues/comments/"):
					n, _ := strconv.Atoi(strings.TrimPrefix(path, "/issues/comments/"))
					c, ok := comments[n]
					if !ok {
						return nil, fmt.Errorf("unknown comment")
					}
					if method == "PATCH" {
						var p struct{ Body string }
						if err := json.Unmarshal(payload, &p); err != nil {
							return nil, err
						}
						c.Body, c.UpdatedAt = p.Body, c.UpdatedAt.Add(time.Second)
						comments[n] = c
						intents++
						if intents == 1 && mode == "comment-restored-after-intent" {
							restoreNativeComment()
						}
						if intents == 1 && mode == "witness-edited" {
							w := comments[102]
							w.User.Login = "other"
							comments[102] = w
						}
						if intents == 1 && mode == "ambiguous-after-intent" {
							w := comments[102]
							w.ID = 104
							duplicate := history[1].ActionJournal
							duplicate.OperationID = "op_d"
							var err error
							w.Body, err = renderActionJournal(duplicate)
							if err != nil {
								return nil, err
							}
							comments[104] = w
						}
					}
					return encode(c)
				case path == "/issues/9":
					return encode(carrier)
				case path == "/issues/1":
					if method == "PATCH" {
						writes++
						var p struct {
							Title, Body, State string
							Labels             []string
						}
						if err := json.Unmarshal(payload, &p); err != nil {
							return nil, err
						}
						target.Title, target.Body, target.State = p.Title, p.Body, p.State
						target.UpdatedAt = target.UpdatedAt.Add(time.Second)
						target.Labels = nil
						for _, l := range p.Labels {
							target.Labels = append(target.Labels, struct {
								Name string `json:"name"`
							}{l})
						}
					}
					return encode(target)
				default:
					return nil, fmt.Errorf("unexpected %s %s", method, path)
				}
			}}
			result, err := client.UndoAction(context.Background(), a.ID, "ses_actor")
			if mode != "success" && mode != "comment-success" {
				if err == nil || result != nil || writes != 0 {
					t.Fatal("unsafe target write", result, err, writes)
				}
				if mode == "witness-edited" || mode == "ambiguous-after-intent" {
					var conflict *ConflictError
					if !errors.As(err, &conflict) || intents != 1 || collections != 2 {
						t.Fatal("receipt recheck gate not reached", err, intents, collections)
					}
				}
				if mode == "external-clock" && intents != 0 {
					t.Fatal("unproven edit wrote intent", intents)
				}
				if mode == "history-denied" && !strings.Contains(err.Error(), "HTTP 403 history denied") {
					t.Fatal("original API error lost", err)
				}
				return
			}
			if err != nil || result.Issue.Title != "Original" || writes != 1 || collections != 2 {
				t.Fatal(result, err, writes, collections)
			}
			result, err = client.UndoAction(context.Background(), a.ID, "ses_actor")
			if err != nil || !result.AlreadyUndone || writes != 1 {
				t.Fatal("replay wrote target", result, err, writes)
			}
		})
	}
}
