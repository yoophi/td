package backendstore

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/marcus/td/internal/config"
	"github.com/marcus/td/internal/models"
	"github.com/marcus/td/internal/session"
	"github.com/marcus/td/internal/workdir"
)

type SessionCommand struct {
	Name              string `json:"name"`
	Branch            string `json:"branch,omitempty"`
	AgentType         string `json:"agent_type"`
	ContextID         string `json:"context_id"`
	PreviousSessionID string `json:"previous_session_id,omitempty"`
}

type Connection struct {
	Client                                 *Client
	StateDirectory                         string
	ProjectID, SessionID, Worktree, Branch string
}

type sessionIntent struct {
	Version   int            `json:"version"`
	Key       string         `json:"key"`
	Command   SessionCommand `json:"command"`
	SessionID string         `json:"session_id,omitempty"`
}

func (c *Client) RegisterSession(ctx context.Context, project, key string, cmd SessionCommand) (string, error) {
	if !config.ValidBackendProjectID(project) || !operationPattern.MatchString(key) || strings.TrimSpace(cmd.ContextID) == "" {
		return "", fmt.Errorf("invalid backend session registration")
	}
	var out struct {
		Session struct {
			ID string `json:"id"`
		} `json:"session"`
	}
	e := c.request(ctx, "POST", "/v1/projects/"+project+"/sessions", "", key, cmd, &out)
	if e == nil && !validSessionID(out.Session.ID) {
		e = requestFailure("POST", key, fmt.Errorf("backend returned invalid session ID"))
	}
	return out.Session.ID, e
}

// Open separates the shared project association from a device/checkout/branch/
// agent context. contextDir must be the actual caller checkout (before resolving
// shared .todos). The registration intent precedes HTTP and survives lost replies.
func Open(ctx context.Context, projectDir, contextDir string) (*Connection, error) {
	return open(ctx, projectDir, contextDir, "")
}

// OpenWeb keeps the td serve browser identity separate from CLI agent sessions.
// Its registration intent is durable across restarts, just like Open.
func OpenWeb(ctx context.Context, projectDir, contextDir string) (*Connection, error) {
	return open(ctx, projectDir, contextDir, "web")
}

func open(ctx context.Context, projectDir, contextDir, agentType string) (*Connection, error) {
	if e := ctx.Err(); e != nil {
		return nil, e
	}
	cfg, e := config.Load(projectDir)
	if e != nil {
		return nil, e
	}
	kind, e := config.Store(cfg)
	if e != nil {
		return nil, e
	}
	if kind != config.StoreBackend || cfg.Backend == nil || !config.ValidBackendProjectID(cfg.Backend.ProjectID) {
		return nil, fmt.Errorf("valid td-backend project association required; run td init")
	}
	client, e := NewClient(cfg.Backend.URL, os.Getenv("TD_BACKEND_TOKEN"))
	if e != nil {
		return nil, e
	}
	info, e := workdir.WorktreeForPath(contextDir)
	if e != nil {
		return nil, e
	}
	branchBytes, e := exec.CommandContext(ctx, "git", "-C", info.WorktreeRoot, "symbolic-ref", "--quiet", "--short", "HEAD").Output()
	if e != nil {
		branchBytes, _ = exec.CommandContext(ctx, "git", "-C", info.WorktreeRoot, "rev-parse", "--short", "HEAD").Output()
	}
	if e := ctx.Err(); e != nil {
		return nil, e
	}
	branch := strings.TrimSpace(string(branchBytes))
	hostname, e := os.Hostname()
	if e != nil {
		return nil, e
	}
	fp := session.GetAgentFingerprint()
	if agentType == "" {
		agentType = string(fp.Type)
	}
	tokenKey := sha256.Sum256([]byte(client.Token))
	identity, _ := json.Marshal([]any{client.URL, cfg.Backend.ProjectID, fmt.Sprintf("%x", tokenKey), hostname, info.WorktreeRoot, branch, agentType, fp, os.Getenv("TD_CONTEXT_ID"), os.Getenv("TERM_SESSION_ID"), os.Getenv("TMUX_PANE")})
	digest := sha256.Sum256(identity)
	root, e := backendContextRoot()
	if e != nil {
		return nil, e
	}
	path := filepath.Join(root, fmt.Sprintf("%x.json", digest))
	var intent sessionIntent
	e = config.WithLocalStateLock(root, func() error {
		var e error
		intent, e = readSessionIntent(path)
		if os.IsNotExist(e) {
			key, e := operationKey()
			if e != nil {
				return e
			}
			name := "td " + agentType
			if agentType == "web" {
				name = "td-serve-web"
			}
			intent = sessionIntent{Version: 1, Key: key, Command: SessionCommand{Name: name, Branch: branch, AgentType: agentType, ContextID: fmt.Sprintf("backend:%x", digest)}}
			return writeSessionIntent(path, intent)
		}
		return e
	})
	if e != nil {
		return nil, e
	}
	id := intent.SessionID
	if id == "" {
		id, e = client.RegisterSession(ctx, cfg.Backend.ProjectID, intent.Key, intent.Command)
		if e != nil {
			return nil, e
		}
		e = config.WithLocalStateLock(root, func() error {
			latest, e := readSessionIntent(path)
			if e != nil {
				return e
			}
			if latest.Key != intent.Key || latest.Command != intent.Command || (latest.SessionID != "" && latest.SessionID != id) {
				return fmt.Errorf("backend session context changed during registration")
			}
			latest.SessionID = id
			return writeSessionIntent(path, latest)
		})
		if e != nil {
			return nil, e
		}
	}
	if _, e = client.GetProject(ctx, cfg.Backend.ProjectID, id); e != nil {
		return nil, e
	}
	return &Connection{StateDirectory: root, Client: client, ProjectID: cfg.Backend.ProjectID, SessionID: id, Worktree: info.WorktreeRoot, Branch: branch}, nil
}

func readSessionIntent(path string) (sessionIntent, error) {
	var out sessionIntent
	data, e := os.ReadFile(path)
	if e != nil {
		return out, e
	}
	if e = json.Unmarshal(data, &out); e != nil {
		return out, fmt.Errorf("invalid persisted backend session context")
	}
	if out.Version != 1 || !operationPattern.MatchString(out.Key) || out.Command.ContextID == "" || (out.SessionID != "" && !validSessionID(out.SessionID)) {
		return out, fmt.Errorf("invalid persisted backend session context")
	}
	return out, nil
}
func writeSessionIntent(path string, intent sessionIntent) error {
	return writeJSONIntent(path, intent)
}
func backendContextRoot() (string, error) {
	root := os.Getenv("TD_BACKEND_CONTEXT_ROOT")
	if root == "" {
		var e error
		root, e = os.UserConfigDir()
		if e != nil {
			return "", e
		}
		root = filepath.Join(root, "td", "backend-contexts")
	}
	if !filepath.IsAbs(root) {
		return "", fmt.Errorf("TD_BACKEND_CONTEXT_ROOT must be absolute")
	}
	return root, nil
}
func writeJSONIntent(path string, intent any) error {
	dir := filepath.Dir(path)
	if e := os.MkdirAll(dir, 0700); e != nil {
		return e
	}
	data, e := json.MarshalIndent(intent, "", "  ")
	if e != nil {
		return e
	}
	file, e := os.CreateTemp(dir, "session-*.tmp")
	if e != nil {
		return e
	}
	defer os.Remove(file.Name())
	if _, e = file.Write(data); e != nil {
		file.Close()
		return e
	}
	if e = file.Sync(); e != nil {
		file.Close()
		return e
	}
	if e = file.Close(); e != nil {
		return e
	}
	return os.Rename(file.Name(), path)
}

func (c *Connection) Get(ctx context.Context, id string) (Issue, error) {
	return c.Client.GetIssue(ctx, c.ProjectID, c.SessionID, id)
}

func (c *Connection) Heartbeat(ctx context.Context) error {
	path, e := scopedPath(c.ProjectID, c.SessionID)
	if e != nil {
		return e
	}
	key, e := operationKey()
	if e != nil {
		return e
	}
	var out struct {
		SessionID string `json:"session_id"`
	}
	if e = c.Client.request(ctx, "POST", path+"/sessions/"+c.SessionID+"/heartbeat", c.SessionID, key, nil, &out); e != nil {
		return e
	}
	if out.SessionID != c.SessionID {
		return fmt.Errorf("backend heartbeat returned a different session")
	}
	return nil
}
func (c *Connection) List(ctx context.Context, all bool) ([]Issue, error) {
	return c.Client.ListIssues(ctx, c.ProjectID, c.SessionID, all)
}

// Association returns only non-secret connection fields for diagnostics.
func (c *Connection) Association() models.BackendStoreConfig {
	return models.BackendStoreConfig{URL: c.Client.URL, ProjectID: c.ProjectID}
}
