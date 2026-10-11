package storetransfer

import (
	"reflect"
	"strings"
	"testing"

	"github.com/marcus/td/internal/query"
)

func TestRewriteQueryExactReferencesPreserveOtherValuesAndSort(t *testing.T) {
	source := `NOT (parent = "td-parent" OR any(id, "td-child", "td-parent")) AND title ~ "td-child with spaces" AND descendant_of("td-parent") sort:-created`
	result, err := RewriteQuery(source, map[string]string{"td-parent": "gh-12", "td-child": "gh-13"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Blocked() || !strings.Contains(result.Query, `parent = "gh-12"`) || !strings.Contains(result.Query, `"gh-13"`) || !strings.Contains(result.Query, `title ~ "td-child with spaces"`) || !strings.Contains(result.Query, `descendant_of("gh-12")`) {
		t.Fatalf("wrong rewrite: %+v", result)
	}
	a, err := query.Parse(source)
	if err != nil {
		t.Fatal(err)
	}
	b, err := query.Parse(result.Query)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(a.Sort, b.Sort) {
		t.Fatal("sort semantics changed")
	}
}

func TestRewriteQueryUnmappedAndPatternsCannotExecute(t *testing.T) {
	result, err := RewriteQuery(`id = "future" OR id ~ "td-" OR blocked_by("future")`, map[string]string{})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Blocked() || !reflect.DeepEqual(result.Unmapped, []string{"future"}) || !reflect.DeepEqual(result.Unsupported, []string{"id ~"}) || !strings.Contains(result.Query, `id ~ "td-"`) {
		t.Fatalf("silently dropped or guessed selectors: %+v", result)
	}
}

func TestRewriteQueryQuotedStringsCannotInjectOrLoseEscapes(t *testing.T) {
	for _, value := range []string{"quoted \" value", "backslash\\value", "line\nnext\tcolumn", "carriage\rreturn", "한글\x01raw", `gh-1" OR status=closed OR id="gh-2`} {
		t.Run(value, func(t *testing.T) {
			result, err := RewriteQuery(`id = "source"`, map[string]string{"source": value})
			if err != nil {
				t.Fatal(err)
			}
			parsed, err := query.Parse(result.Query)
			if err != nil {
				t.Fatal(err)
			}
			field, ok := parsed.Root.(*query.FieldExpr)
			if !ok || field.Field != "id" || field.Value != value {
				t.Fatalf("injection or changed string: %+v", parsed.Root)
			}
		})
	}
	for _, text := range []string{`title ~ "quoted \" value"`, `"text with spaces"`, `due >= -7d AND minor = true sort:due`, `parent = EMPTY`, `sort:-updated`} {
		before, err := query.Parse(text)
		if err != nil {
			t.Fatal(err)
		}
		if p := before.Validate(); len(p) > 0 {
			t.Fatal(p)
		}
		result, err := RewriteQuery(text, nil)
		if err != nil {
			t.Fatal(err)
		}
		after, err := query.Parse(result.Query)
		if err != nil {
			t.Fatal(err)
		}
		if p := after.Validate(); len(p) > 0 {
			t.Fatal(p)
		}
		if !reflect.DeepEqual(before.Root, after.Root) || !reflect.DeepEqual(before.Sort, after.Sort) {
			t.Fatalf("unrelated semantics changed: %s -> %s", text, result.Query)
		}
	}
}
