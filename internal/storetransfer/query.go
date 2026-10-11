package storetransfer

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/marcus/td/internal/query"
)

type QueryRewrite struct {
	Query       string   `json:"query"`
	Unmapped    []string `json:"unmapped"`
	Unsupported []string `json:"unsupported"`
}

func (r QueryRewrite) Blocked() bool { return len(r.Unmapped) > 0 || len(r.Unsupported) > 0 }

// RewriteQuery substitutes structured exact task references only. Missing
// mappings and identity-pattern/order selectors are reported, never guessed.
// A blocked rewrite is preview data, not permission to execute a partial plan.
func RewriteQuery(text string, mapping map[string]string) (*QueryRewrite, error) {
	result := &QueryRewrite{Query: text, Unmapped: []string{}, Unsupported: []string{}}
	if strings.TrimSpace(text) == "" {
		return result, nil
	}
	parsed, err := query.Parse(text)
	if err != nil {
		return nil, err
	}
	if problems := parsed.Validate(); len(problems) > 0 {
		return nil, fmt.Errorf("invalid source query: %v", problems)
	}
	unmapped := map[string]bool{}
	rewrite := func(value any) any {
		id, ok := value.(string)
		if !ok {
			return value
		}
		if target, exists := mapping[id]; exists && target != "" {
			return target
		}
		unmapped[id] = true
		return value
	}
	var visit func(query.Node)
	visit = func(node query.Node) {
		switch n := node.(type) {
		case *query.BinaryExpr:
			visit(n.Left)
			visit(n.Right)
		case *query.UnaryExpr:
			visit(n.Expr)
		case *query.FieldExpr:
			if identityField(n.Field) {
				if n.Operator == query.OpEq || n.Operator == query.OpNeq {
					n.Value = rewrite(n.Value)
				} else {
					result.Unsupported = append(result.Unsupported, n.Field+" "+n.Operator)
				}
			}
		case *query.FunctionCall:
			if n.Name == "blocks" || n.Name == "blocked_by" || n.Name == "child_of" || n.Name == "descendant_of" {
				for i, value := range n.Args {
					n.Args[i] = rewrite(value)
				}
			}
			if (n.Name == "any" || n.Name == "all" || n.Name == "none") && len(n.Args) > 1 {
				if field, ok := n.Args[0].(string); ok && identityField(field) {
					for i := 1; i < len(n.Args); i++ {
						n.Args[i] = rewrite(n.Args[i])
					}
				}
			}
		}
	}
	visit(parsed.Root)
	for id := range unmapped {
		result.Unmapped = append(result.Unmapped, id)
	}
	sort.Strings(result.Unmapped)
	result.Query, err = renderQuery(parsed)
	if err != nil {
		return nil, err
	}
	// Enforce valid serialization before returning preview text. AST.String is
	// a diagnostic formatter and loses quoting; do not use it for execution.
	check, err := query.Parse(result.Query)
	if err != nil {
		return nil, fmt.Errorf("serialize mapped query: %w", err)
	}
	if problems := check.Validate(); len(problems) > 0 {
		return nil, fmt.Errorf("mapped query validation: %v", problems)
	}
	return result, nil
}

func identityField(field string) bool {
	switch field {
	case "id", "parent", "epic", "dep.blocks", "dep.depends_on":
		return true
	}
	return false
}

func quoteQuery(value string) string {
	// Match TDQ's supported escapes. strconv.Quote introduces \r/\x/\u
	// escapes which TDQ does not decode and would change string contents.
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`, "\t", `\t`).Replace(value) + `"`
}

func renderQuery(parsed *query.Query) (string, error) {
	renderValue := func(value any) (string, error) {
		switch v := value.(type) {
		case string:
			return quoteQuery(v), nil
		case int:
			return strconv.Itoa(v), nil
		case *query.DateValue:
			return v.Raw, nil
		case *query.SpecialValue:
			return v.String(), nil
		default:
			return "", fmt.Errorf("unsupported query operand %T", value)
		}
	}
	var render func(query.Node) (string, error)
	render = func(node query.Node) (string, error) {
		switch n := node.(type) {
		case *query.BinaryExpr:
			left, err := render(n.Left)
			if err != nil {
				return "", err
			}
			right, err := render(n.Right)
			if err != nil {
				return "", err
			}
			return "(" + left + " " + n.Op + " " + right + ")", nil
		case *query.UnaryExpr:
			child, err := render(n.Expr)
			if err != nil {
				return "", err
			}
			return "(" + n.Op + " " + child + ")", nil
		case *query.FieldExpr:
			value, err := renderValue(n.Value)
			if err != nil {
				return "", err
			}
			return n.Field + " " + n.Operator + " " + value, nil
		case *query.FunctionCall:
			args := make([]string, len(n.Args))
			for i, value := range n.Args {
				var err error
				args[i], err = renderValue(value)
				if err != nil {
					return "", err
				}
			}
			return n.Name + "(" + strings.Join(args, ", ") + ")", nil
		case *query.TextSearch:
			return quoteQuery(n.Text), nil
		case nil:
			return "", nil
		default:
			return "", fmt.Errorf("unsupported query node %T", node)
		}
	}
	text, err := render(parsed.Root)
	if err != nil {
		return "", err
	}
	if parsed.Sort != nil {
		field := parsed.Sort.Field
		aliases := []string{}
		for alias, column := range query.SortFieldToColumn {
			if column == field {
				aliases = append(aliases, alias)
			}
		}
		sort.Strings(aliases)
		if len(aliases) > 0 {
			field = aliases[0]
		}
		if parsed.Sort.Descending {
			field = "-" + field
		}
		text = strings.TrimSpace(text + " sort:" + field)
	}
	return text, nil
}
