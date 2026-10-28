package mcpclient

import (
	"context"
	"net/http"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/opensvc/ai-agent/internal/agent"
	"github.com/opensvc/ai-agent/internal/auth"
)

// skillsServer serves skills/list as an MCP server with the skills
// extension does, the extension declared when declare is set.
func skillsServer(t *testing.T, declare bool, result *skillsListResult) string {
	t.Helper()
	capabilities := &mcp.ServerCapabilities{Resources: &mcp.ResourceCapabilities{}}
	if declare {
		capabilities.Extensions = map[string]any{skillsExtension: map[string]any{"directoryRead": false}}
	}
	server := mcp.NewServer(&mcp.Implementation{Name: "test-mcp", Version: "v0.1.0"}, &mcp.ServerOptions{Capabilities: capabilities})
	if err := mcp.AddReceivingCustomMethod(server, skillsListMethod, func(context.Context, *mcp.ServerSession, *skillsListParams) (*skillsListResult, error) {
		return result, nil
	}); err != nil {
		t.Fatal(err)
	}
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true})
	return serveUnix(t, handler)
}

func listedSkills() *skillsListResult {
	frontmatter := map[string]any{"name": "node-maintenance", "metadata": map[string]any{"opensvc/tools": "drain_node"}}
	return &skillsListResult{Skills: []skillsListEntry{
		{URI: "skill://node-maintenance/SKILL.md", Frontmatter: frontmatter, Resources: []any{map[string]any{"uri": "skill://node-maintenance/SKILL.md", "digest": "sha256:ab", "size": float64(12)}}},
		// A skill generated on demand has no manifest: it is not loaded.
		{URI: "skill://generated/SKILL.md", Frontmatter: frontmatter, Resources: "dynamic"},
	}}
}

// The client lists the skills of a server declaring the extension, through
// skills/list, with their frontmatter as listed, its strings strings, and
// their manifest; a skill without manifest is left out; a server without
// the extension lists none, and is not asked.
func TestListSkillsReadsTheExtension(t *testing.T) {
	ctx := auth.WithBearerToken(t.Context(), "delegated-test-token")
	for name, tc := range map[string]struct {
		declare bool
		want    int
	}{"declared": {true, 1}, "not declared": {false, 0}} {
		t.Run(name, func(t *testing.T) {
			client, err := New(skillsServer(t, tc.declare, listedSkills()))
			if err != nil {
				t.Fatal(err)
			}
			session, err := client.Connect(ctx)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = session.Close() })
			skills, err := session.ListSkills(ctx)
			if err != nil || len(skills) != tc.want {
				t.Fatalf("skills = %+v, %v", skills, err)
			}
			if tc.want == 0 {
				return
			}
			want := agent.SkillFile{URI: "skill://node-maintenance/SKILL.md", Digest: "sha256:ab", Size: 12}
			tools, _ := skills[0].Frontmatter["metadata"].(map[string]any)["opensvc/tools"].(string)
			if skills[0].URI != "skill://node-maintenance/SKILL.md" || len(skills[0].Files) != 1 || skills[0].Files[0] != want || tools != "drain_node" {
				t.Fatalf("skill = %+v", skills[0])
			}
		})
	}
}
