package ghstore

import (
	"bufio"
	"crypto/sha256"
	"fmt"
	"github.com/marcus/td/internal/models"
	"strconv"
	"strings"
)

// ParseImportMarkdown reads the historical summary format, including gh-N
// headings. Markdown is lossy; JSON is required for history/provenance restore.
func ParseImportMarkdown(data string) ([]ImportItem, error) {
	data = strings.ReplaceAll(data, "\r\n", "\n")
	rows := []ImportItem{}
	var current *ImportItem
	var body []string
	description := false
	finish := func() {
		if current != nil {
			current.Issue.Description = strings.TrimSpace(strings.Join(body, "\n"))
			rows = append(rows, *current)
		}
	}
	scanner := bufio.NewScanner(strings.NewReader(data))
	scanner.Buffer(make([]byte, 4096), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "## ") {
			finish()
			header := strings.TrimSpace(strings.TrimPrefix(line, "## "))
			id := ""
			title := header
			if before, after, ok := strings.Cut(header, ": "); ok && importIDPattern.MatchString(before) {
				id, title = before, after
			}
			if id == "" {
				id = fmt.Sprintf("td-%x", sha256.Sum256([]byte(fmt.Sprintf("%d\n%s\n%s", len(rows), header, data))))
			}
			current = &ImportItem{Issue: models.Issue{ID: id, Title: title, Type: models.TypeTask, Priority: models.PriorityP2, Status: models.StatusOpen}}
			body = nil
			description = false
			continue
		}
		if current == nil {
			continue
		}
		if !description && strings.HasPrefix(line, "- ") {
			key, value, ok := strings.Cut(strings.TrimPrefix(line, "- "), ": ")
			if ok {
				switch key {
				case "Status":
					current.Issue.Status = models.Status(value)
					continue
				case "Type":
					current.Issue.Type = models.Type(value)
					continue
				case "Priority":
					current.Issue.Priority = models.Priority(value)
					continue
				case "Points":
					n, err := strconv.Atoi(value)
					if err != nil {
						return nil, fmt.Errorf("invalid Markdown points: %w", err)
					}
					current.Issue.Points = n
					continue
				case "Labels":
					current.Issue.Labels = strings.Split(value, ",")
					for i := range current.Issue.Labels {
						current.Issue.Labels[i] = strings.TrimSpace(current.Issue.Labels[i])
					}
					continue
				}
			}
		}
		if strings.TrimSpace(line) != "" {
			description = true
		}
		body = append(body, line)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	finish()
	if len(rows) == 0 && strings.TrimSpace(data) != "" && strings.TrimSpace(data) != "# Issues Export" {
		return nil, fmt.Errorf("markdown import has no issue headings")
	}
	if err := ValidateImportItems(rows); err != nil {
		return nil, err
	}
	return rows, nil
}
