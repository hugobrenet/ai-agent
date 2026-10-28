package mcpclient

import (
	"context"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/opensvc/ai-agent/internal/agent"
)

const (
	// skillsExtension is the MCP extension serving Agent Skills, SEP-2640.
	// The go-sdk does not call it yet: skills/list is a custom method of the
	// client until it does.
	skillsExtension  = "io.modelcontextprotocol/skills"
	skillsListMethod = "skills/list"
	// maxMCPSkills bounds the skills read from a server, and
	// maxMCPSkillFiles the files of each.
	maxMCPSkills     = 32
	maxMCPSkillFiles = 512
)

type skillsListParams struct {
	mcp.ParamsBase
	Cursor string `json:"cursor,omitempty"`
}

type skillsListResult struct {
	mcp.ResultBase
	Skills []skillsListEntry `json:"skills"`
}

type skillsListEntry struct {
	URI         string         `json:"uri"`
	Frontmatter map[string]any `json:"frontmatter"`
	// Resources is the manifest of the files; a skill whose content is
	// generated gives "dynamic" instead, which the agent does not load.
	Resources any `json:"resources"`
}

// Session serves the skills of its server to the agent.
var _ agent.SkillSession = (*Session)(nil)

func registerSkillMethods(client *mcp.Client) error {
	return mcp.AddSendingCustomMethod[*skillsListParams, *skillsListResult](client, skillsListMethod)
}

// ListSkills returns the skills of the MCP server, as its skills/list gives
// them, at most maxMCPSkills: none when the server does not declare the
// skills extension. The first page is read: the servers of the agent list
// their skills in one. A skill without a manifest of its files is left out.
func (s *Session) ListSkills(ctx context.Context) ([]agent.Skill, error) {
	result := s.session.InitializeResult()
	if result == nil || result.Capabilities == nil || result.Capabilities.Resources == nil {
		return nil, nil
	}
	if _, declared := result.Capabilities.Extensions[skillsExtension]; !declared {
		return nil, nil
	}
	listed, err := mcp.CallCustomMethod[*skillsListParams, *skillsListResult](ctx, s.session, skillsListMethod, &skillsListParams{})
	if err != nil {
		return nil, fmt.Errorf("list MCP skills: %w", err)
	}
	var skills []agent.Skill
	for _, entry := range listed.Skills {
		if len(skills) == maxMCPSkills {
			break
		}
		files, ok := skillFiles(entry.Resources)
		if !ok || entry.URI == "" || entry.Frontmatter == nil {
			continue
		}
		skills = append(skills, agent.Skill{URI: entry.URI, Frontmatter: entry.Frontmatter, Files: files})
	}
	return skills, nil
}

// skillFiles reads a manifest: a list of files, each with its URI, digest
// and size; anything else, such as "dynamic", is no manifest.
func skillFiles(resources any) ([]agent.SkillFile, bool) {
	items, ok := resources.([]any)
	if !ok || len(items) == 0 || len(items) > maxMCPSkillFiles {
		return nil, false
	}
	files := make([]agent.SkillFile, 0, len(items))
	for _, item := range items {
		fields, ok := item.(map[string]any)
		if !ok {
			return nil, false
		}
		uri, _ := fields["uri"].(string)
		digest, _ := fields["digest"].(string)
		size, _ := fields["size"].(float64)
		if uri == "" || digest == "" || size < 0 || size != float64(int64(size)) {
			return nil, false
		}
		files = append(files, agent.SkillFile{URI: uri, Digest: digest, Size: int64(size)})
	}
	return files, true
}
