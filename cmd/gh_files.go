package cmd

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/marcus/td/internal/config"
	"github.com/marcus/td/internal/ghcontext"
	"github.com/marcus/td/internal/ghstore"
	"github.com/marcus/td/internal/models"
	"github.com/marcus/td/internal/output"
	"github.com/spf13/cobra"
)

func init() {
	for _, original := range []*cobra.Command{linkCmd, unlinkCmd, filesCmd} {
		local := original.RunE
		original.RunE = func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(getBaseDir())
			if err != nil {
				return err
			}
			kind, err := config.Store(cfg)
			if err != nil {
				return err
			}
			if kind == config.StoreSQLite {
				return local(cmd, args)
			}
			cmd.SilenceUsage = true
			cmd.SetOut(cmd.OutOrStdout())
			if len(args) == 0 {
				return fmt.Errorf("issue ID required")
			}
			if _, err := canonicalGitHubID(args[0]); err != nil {
				return err
			}
			dependency, _ := cmd.Flags().GetString("depends-on")
			if dependency != "" {
				if len(args) != 1 || cmd.Flags().Changed("role") || cmd.Flags().Changed("recursive") {
					return fmt.Errorf("--depends-on cannot be combined with file patterns, --role or --recursive")
				}
				client, err := ghstore.Open(cmd.Context(), getBaseDir(), cfg.GitHub)
				if err != nil {
					return err
				}
				return changeGitHubDependencies(depCmd, cmd, []string{args[0], dependency}, cfg, client)
			}
			client, err := ghstore.Open(cmd.Context(), getBaseDir(), cfg.GitHub)
			if err != nil {
				return err
			}
			dir, err := gitHubContextDirectory()
			if err != nil {
				return err
			}
			scope, err := ghcontext.Resolve(cmd.Context(), dir, cfg.GitHub.Repo)
			if err != nil {
				return err
			}
			observed, err := client.Get(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			if original == filesCmd {
				return listGitHubFiles(cmd, observed, scope.Worktree)
			}
			var files []models.IssueFile
			if observed.Details != nil {
				files = append(files, observed.Details.Files...)
			}
			changed := []string{}
			if original == linkCmd {
				if len(args) < 2 {
					return fmt.Errorf("need file patterns or --depends-on flag")
				}
				role, _ := cmd.Flags().GetString("role")
				if role == "" {
					role = string(models.FileRoleImplementation)
				}
				if !slices.Contains([]string{"implementation", "test", "reference", "config"}, role) {
					return fmt.Errorf("invalid file role %q", role)
				}
				recursive, _ := cmd.Flags().GetBool("recursive")
				matches, err := matchGitHubFiles(scope.Worktree, dir, args[1:], recursive)
				if err != nil {
					return err
				}
				now := time.Now().UTC()
				for _, file := range matches {
					sha, err := computeFileSHA(filepath.Join(scope.Worktree, filepath.FromSlash(file)))
					if err != nil {
						return fmt.Errorf("hash %s: %w; no file changes attempted", file, err)
					}
					index := slices.IndexFunc(files, func(f models.IssueFile) bool { return f.FilePath == file })
					if index >= 0 && files[index].Role == models.FileRole(role) && files[index].LinkedSHA == sha {
						continue
					}
					row := models.IssueFile{ID: "file-" + rand.Text(), IssueID: observed.ID, FilePath: file, Role: models.FileRole(role), LinkedSHA: sha, LinkedAt: now}
					if index >= 0 {
						row.ID = files[index].ID
						files[index] = row
					} else {
						files = append(files, row)
					}
					changed = append(changed, file)
				}
			} else {
				if len(args) != 2 {
					return fmt.Errorf("unlink requires an issue ID and pattern")
				}
				pattern := filepath.ToSlash(args[1])
				if filepath.IsAbs(args[1]) {
					pattern, err = repositoryFilePath(scope.Worktree, args[1], false)
					if err != nil {
						return err
					}
				}
				if _, err = filepath.Match(pattern, ""); err != nil {
					return fmt.Errorf("invalid unlink pattern: %w", err)
				}
				retained := []models.IssueFile{}
				for _, file := range files {
					match, _ := filepath.Match(pattern, file.FilePath)
					if match || pattern == file.FilePath {
						changed = append(changed, file.FilePath)
					} else {
						retained = append(retained, file)
					}
				}
				files = retained
			}
			if len(changed) > 0 {
				state, err := scope.Update(cmd.Context(), nil)
				if err != nil {
					return err
				}
				result, noop, err := client.ReplaceLinkedFilesObserved(cmd.Context(), observed, files)
				if err != nil {
					return err
				}
				if !noop {
					action := "Linked"
					if original == unlinkCmd {
						action = "Unlinked"
					}
					_, err = client.AppendActivity(cmd.Context(), result.ID, models.Activity{Kind: "log", LogType: models.LogTypeProgress, SessionID: state.Session.ID, Message: action + " files: " + strings.Join(changed, ", ")})
					if err != nil {
						return fmt.Errorf("%s file changes for %s are saved (%d files: %s), but the activity comment failed; earlier changes remain, inspect files before retrying: %w", action, result.ID, len(changed), strings.Join(changed, ", "), err)
					}
				}
			}
			action := "linked"
			if original == unlinkCmd {
				action = "unlinked"
			}
			if jsonMode(cmd) {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]any{"action": action, "issue": observed.ID, "count": len(changed), "files": changed})
			}
			cmd.Printf("%s %d files for %s\n", strings.ToUpper(action), len(changed), observed.ID)
			return nil
		}
	}
}

func repositoryFilePath(root, filePath string, resolve bool) (string, error) {
	if !filepath.IsAbs(filePath) {
		filePath = filepath.Join(root, filePath)
	}
	filePath = filepath.Clean(filePath)
	within := func(base, candidate string) (string, bool) {
		rel, err := filepath.Rel(base, candidate)
		return rel, err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
	}
	lexical, lexicalInside := within(root, filePath)
	if !resolve && lexicalInside {
		return filepath.ToSlash(lexical), nil
	}
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", err
	}
	var resolved string
	if resolve {
		resolved, err = filepath.EvalSymlinks(filePath)
	} else {
		// Unlink permits deleted files; resolve only their existing parent.
		var parent string
		parent, err = filepath.EvalSymlinks(filepath.Dir(filePath))
		resolved = filepath.Join(parent, filepath.Base(filePath))
	}
	if err != nil {
		return "", err
	}
	realRelative, inside := within(realRoot, resolved)
	if !inside {
		return "", fmt.Errorf("file is outside repository: %s", filePath)
	}
	if lexicalInside {
		return filepath.ToSlash(lexical), nil
	}
	// macOS commonly spells the same temporary directory as /var and
	// /private/var. Store its real relative path, not the absolute alias.
	return filepath.ToSlash(realRelative), nil
}

func matchGitHubFiles(root, dir string, patterns []string, recursive bool) ([]string, error) {
	seen := map[string]bool{}
	add := func(path string) error {
		relative, err := repositoryFilePath(root, path, true)
		if err != nil {
			return err
		}
		info, err := os.Stat(path)
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("not a regular file: %s", path)
		}
		seen[relative] = true
		return nil
	}
	for _, pattern := range patterns {
		if !filepath.IsAbs(pattern) {
			pattern = filepath.Join(dir, pattern)
		}
		matches, err := filepath.Glob(pattern)
		if err != nil {
			return nil, fmt.Errorf("invalid file pattern: %w", err)
		}
		if len(matches) == 0 {
			return nil, fmt.Errorf("no files matching %s; no file changes attempted", pattern)
		}
		for _, match := range matches {
			if _, err := repositoryFilePath(root, match, true); err != nil {
				return nil, err
			}
			info, err := os.Stat(match)
			if err != nil {
				return nil, err
			}
			if !info.IsDir() {
				if err = add(match); err != nil {
					return nil, err
				}
				continue
			}
			if recursive {
				err = filepath.WalkDir(match, func(path string, entry os.DirEntry, err error) error {
					if err != nil {
						return err
					}
					if entry.IsDir() {
						if entry.Name() == ".git" || entry.Name() == ".todos" {
							return filepath.SkipDir
						}
						return nil
					}
					return add(path)
				})
			} else {
				entries, readErr := os.ReadDir(match)
				if readErr != nil {
					return nil, readErr
				}
				for _, entry := range entries {
					if !entry.IsDir() {
						if err = add(filepath.Join(match, entry.Name())); err != nil {
							break
						}
					}
				}
			}
			if err != nil {
				return nil, err
			}
		}
	}
	result := make([]string, 0, len(seen))
	for path := range seen {
		result = append(result, path)
	}
	sort.Strings(result)
	if len(result) == 0 {
		return nil, fmt.Errorf("no regular files found; no file changes attempted")
	}
	return result, nil
}

type gitHubFileStatus struct {
	models.IssueFile
	Status     string `json:"status"`
	CurrentSHA string `json:"current_sha,omitempty"`
	Unlinked   bool   `json:"unlinked,omitempty"`
}

func listGitHubFiles(cmd *cobra.Command, record *ghstore.Record, root string) error {
	rows := []gitHubFileStatus{}
	linked := map[string]bool{}
	changedOnly, _ := cmd.Flags().GetBool("changed")
	untracked, _ := cmd.Flags().GetBool("untracked")
	if record.Details != nil {
		for _, file := range record.Details.Files {
			relative, err := repositoryFilePath(root, filepath.FromSlash(file.FilePath), false)
			if err != nil {
				return err
			}
			if relative != file.FilePath {
				return fmt.Errorf("noncanonical linked file path %q", file.FilePath)
			}
			linked[file.FilePath] = true
			row := gitHubFileStatus{IssueFile: file, Status: "unchanged"}
			full := filepath.Join(root, filepath.FromSlash(file.FilePath))
			_, err = os.Stat(full)
			if os.IsNotExist(err) {
				row.Status = "deleted"
			} else if err != nil {
				return fmt.Errorf("inspect %s: %w", file.FilePath, err)
			} else {
				if _, err = repositoryFilePath(root, full, true); err != nil {
					return err
				}
				row.CurrentSHA, err = computeFileSHA(full)
				if err != nil {
					return err
				}
				if file.LinkedSHA == "" {
					row.Status = "new"
				} else if file.LinkedSHA != row.CurrentSHA {
					row.Status = "modified"
				}
			}
			if !changedOnly || row.Status != "unchanged" {
				rows = append(rows, row)
			}
		}
	}
	if untracked {
		data, err := exec.CommandContext(cmd.Context(), "git", "-C", root, "status", "--porcelain=v1", "-z", "--untracked-files=all").Output()
		if err != nil {
			return fmt.Errorf("read unlinked Git changes: %w", err)
		}
		entries := strings.Split(string(data), "\x00")
		for i := 0; i < len(entries); i++ {
			entry := entries[i]
			if entry == "" {
				continue
			}
			if len(entry) < 4 {
				return fmt.Errorf("invalid git status entry")
			}
			code, path := entry[:2], filepath.ToSlash(entry[3:])
			if strings.ContainsAny(code, "RC") {
				i++
			}
			if linked[path] {
				continue
			}
			status := "modified"
			if code == "??" {
				status = "new"
			} else if strings.Contains(code, "D") {
				status = "deleted"
			}
			rows = append(rows, gitHubFileStatus{IssueFile: models.IssueFile{IssueID: record.ID, FilePath: path}, Status: status, Unlinked: true})
		}
	}
	if jsonMode(cmd) {
		return json.NewEncoder(cmd.OutOrStdout()).Encode(rows)
	}
	cmd.Printf("%s: %s\n", record.ID, output.SanitizeIssueText(record.Title))
	if record.Details != nil {
		for i := len(record.Details.Transitions) - 1; i >= 0; i-- {
			entry := record.Details.Transitions[i]
			if entry.Action == "start" && entry.Snapshot != nil {
				cmd.Printf("Started: %s (%s)\n", output.ShortSHA(entry.Snapshot.CommitSHA), output.FormatTimeAgo(entry.Snapshot.Timestamp))
				break
			}
		}
	}
	for _, row := range rows {
		role := string(row.Role)
		if row.Unlinked {
			role = "unlinked"
		}
		cmd.Printf("%s [%s] (%s)\n", output.SanitizeIssueText(row.FilePath), row.Status, role)
	}
	if len(rows) == 0 {
		cmd.Println("No matching files")
	}
	return nil
}
