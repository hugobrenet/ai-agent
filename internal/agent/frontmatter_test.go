package agent

import (
	"encoding/json"
	"strings"
	"testing"
)

// The frontmatter of a skill is read in the form the Agent Skills
// specification gives it: strings, plain or quoted, and one level of map.
func TestParseFrontmatterReadsTheSpecificationForm(t *testing.T) {
	fields, err := parseFrontmatter(strings.Join([]string{
		"name: node-maintenance",
		"description: Use when the user plans to reboot, patch or repair a node.",
		"license: 'Apache-2.0, it''s quoted'",
		`compatibility: "Requires om3"`,
		"metadata:",
		"  opensvc/tools: get_node_status drain_node",
		"  author: opensvc",
	}, "\n"))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"name":          "node-maintenance",
		"description":   "Use when the user plans to reboot, patch or repair a node.",
		"license":       "Apache-2.0, it's quoted",
		"compatibility": "Requires om3",
		"metadata":      map[string]any{"opensvc/tools": "get_node_status drain_node", "author": "opensvc"},
	}
	if !sameFrontmatter(fields, want) {
		t.Fatalf("fields = %v", fields)
	}
}

// Any other YAML is refused: the agent does not guess what a skill meant.
func TestParseFrontmatterRefusesOtherYAML(t *testing.T) {
	for name, header := range map[string]string{
		"list":          "name: a\ntools:\n  - x",
		"empty block":   "name: a\ndescription: |\nlicense: b",
		"blank first":   "name: a\ndescription: |\n\n  text",
		"folded deeper": "name: a\ndescription: >\n  text\n    deeper",
		"indicator":     "name: a\ndescription: |2\n  text",
		"tab":           "name:\ta",
		"comment":       "name: a # the name",
		"two levels":    "metadata:\n  a:\n    b: c",
		"deep indent":   "metadata:\n    a: b",
		"orphan indent": "  a: b",
		"twice":         "name: a\nname: b",
		"escape":        `name: "a\"b"`,
		"flow map":      "metadata: {a: b}",
		"anchor":        "name: &a x",
		"no space":      "name:a",
		"empty map":     "metadata:\nname: a",
		"colon value":   "description: a: b",
	} {
		if _, err := parseFrontmatter(header); err == nil {
			t.Errorf("%s: %q was read", name, header)
		}
	}
}

// The frontmatter read must be the one listed, field by field: a field
// missing, added or changed, or a value of another type, differs.
func TestSameFrontmatterComparesFieldByField(t *testing.T) {
	read := map[string]any{"name": "a", "metadata": map[string]any{"opensvc/tools": "x y"}}
	for name, listed := range map[string]map[string]any{
		"changed":   {"name": "b", "metadata": map[string]any{"opensvc/tools": "x y"}},
		"missing":   {"name": "a"},
		"added":     {"name": "a", "license": "MIT", "metadata": map[string]any{"opensvc/tools": "x y"}},
		"tools":     {"name": "a", "metadata": map[string]any{"opensvc/tools": "x"}},
		"not text":  {"name": "a", "metadata": map[string]any{"opensvc/tools": []any{"x", "y"}}},
		"not a map": {"name": "a", "metadata": "x y"},
	} {
		if sameFrontmatter(read, listed) {
			t.Errorf("%s: %v taken for %v", name, listed, read)
		}
	}
	if !sameFrontmatter(read, map[string]any{"name": "a", "metadata": map[string]any{"opensvc/tools": "x y"}}) {
		t.Fatal("the same frontmatter differs")
	}
}

// A literal or folded block reads as the server reads it: the expected values
// are those of go.yaml.in/yaml/v2, the library of om3-mcp, for each header.
func TestParseFrontmatterReadsBlocksAsTheServer(t *testing.T) {
	for _, tc := range []struct{ header, want string }{
		{"description: |\n  Line one.\n  Line two.\n", `{"description":"Line one.\nLine two.\n"}`},
		{"description: |-\n  Line one.\n\n  Line three.\n", `{"description":"Line one.\n\nLine three."}`},
		{"description: |+\n  Kept.\n\n", `{"description":"Kept.\n\n"}`},
		{"description: >\n  Use when the user plans to reboot,\n  patch or repair a node.\n\n  Second paragraph.\n", `{"description":"Use when the user plans to reboot, patch or repair a node.\nSecond paragraph.\n"}`},
		{"description: >-\n  One\n  two\n\n\n  three\n", `{"description":"One two\n\nthree"}`},
		{"description: |\n  Indented:\n    deeper line\n  back\n", `{"description":"Indented:\n  deeper line\nback\n"}`},
		{"metadata:\n  summary: >\n    folded under\n    a map\n  opensvc/tools: a b\nname: x\n", `{"metadata":{"opensvc/tools":"a b","summary":"folded under a map\n"},"name":"x"}`},
	} {
		fields, err := parseFrontmatter(tc.header)
		if err != nil {
			t.Errorf("%q: %v", tc.header, err)
			continue
		}
		var want map[string]any
		if err := json.Unmarshal([]byte(tc.want), &want); err != nil {
			t.Fatal(err)
		}
		if !sameFrontmatter(fields, want) {
			t.Errorf("%q = %q, want %s", tc.header, fields, tc.want)
		}
	}
}
