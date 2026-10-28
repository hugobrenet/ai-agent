package agent

import (
	"fmt"
	"regexp"
	"strings"
)

// frontmatterKey is a key of a skill frontmatter, such as name or
// opensvc/tools.
var frontmatterKey = regexp.MustCompile(`^[A-Za-z0-9_./-]+$`)

// splitFrontmatter cuts a SKILL.md into its YAML frontmatter and its body.
// The line ends are read as YAML reads them: a CRLF ends a line.
func splitFrontmatter(text string) (string, string, error) {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	rest, ok := strings.CutPrefix(text, "---\n")
	if !ok {
		return "", "", fmt.Errorf("the skill does not begin with a frontmatter")
	}
	header, body, ok := strings.Cut(rest, "\n---\n")
	if !ok {
		return "", "", fmt.Errorf("the frontmatter of the skill is not closed")
	}
	return header, body, nil
}

// parseFrontmatter reads the frontmatter of an Agent Skill, in the form the
// specification gives it: keys whose value is a string, plain, quoted or a
// literal (|) or folded (>) block, the common form of a long description,
// and keys holding, one level down, a map of such strings, such as
// metadata. Any other YAML, a list, an anchor, a comment, a tab, is refused:
// the agent does not guess what a skill meant.
func parseFrontmatter(header string) (map[string]any, error) {
	fields := map[string]any{}
	var nested map[string]any
	// A final line break ends the last line, it opens no empty one.
	lines := strings.Split(strings.TrimSuffix(header, "\n"), "\n")
	for number := 0; number < len(lines); number++ {
		line := lines[number]
		if strings.TrimSpace(line) == "" {
			continue
		}
		if strings.ContainsRune(line, '\t') {
			return nil, fmt.Errorf("frontmatter line %d: tabs are not read", number+1)
		}
		indented := strings.HasPrefix(line, "  ")
		if indented && nested == nil {
			return nil, fmt.Errorf("frontmatter line %d: an indented line belongs to no map", number+1)
		}
		indent := 0
		if indented {
			indent = 2
			if strings.HasPrefix(line, "   ") {
				return nil, fmt.Errorf("frontmatter line %d: only one level of map is read", number+1)
			}
		}
		key, value, err := frontmatterLine(strings.TrimSpace(line))
		if err != nil {
			return nil, fmt.Errorf("frontmatter line %d: %w", number+1, err)
		}
		if value != nil && (*value == "|" || *value == ">" || *value == "|-" || *value == ">-" || *value == "|+" || *value == ">+") {
			block, next, err := frontmatterBlock(lines, number+1, indent, *value)
			if err != nil {
				return nil, fmt.Errorf("frontmatter key %s: %w", key, err)
			}
			value, number = &block, next-1
		}
		if indented {
			if value == nil {
				return nil, fmt.Errorf("frontmatter line %d: only one level of map is read", number+1)
			}
			if _, taken := nested[key]; taken {
				return nil, fmt.Errorf("frontmatter line %d: %s is set twice", number+1, key)
			}
			nested[key] = *value
			continue
		}
		if _, taken := fields[key]; taken {
			return nil, fmt.Errorf("frontmatter line %d: %s is set twice", number+1, key)
		}
		if value == nil {
			nested = map[string]any{}
			fields[key] = nested
			continue
		}
		nested = nil
		fields[key] = *value
	}
	for key, value := range fields {
		if items, ok := value.(map[string]any); ok && len(items) == 0 {
			return nil, fmt.Errorf("frontmatter key %s holds nothing", key)
		}
	}
	return fields, nil
}

// frontmatterBlock reads the block scalar of a key indented by keyIndent,
// from lines[start], as YAML does: its lines are those indented deeper than
// the key, the indentation of the first one removed from all; a literal
// block (|) keeps the line breaks, a folded one (>) joins its lines with a
// space, an empty line standing for a break; the final breaks are one,
// none with "-" or all with "+". A block with no content, starting with an
// empty line, or folding a line more indented, is refused. It returns the
// string and the index of the first line after the block.
func frontmatterBlock(lines []string, start, keyIndent int, indicator string) (string, int, error) {
	end := start
	blockIndent := -1
	var content []string
	for ; end < len(lines); end++ {
		line := lines[end]
		if strings.TrimSpace(line) == "" {
			if blockIndent < 0 {
				return "", 0, fmt.Errorf("a block starting with an empty line is not read")
			}
			content = append(content, "")
			continue
		}
		indent := len(line) - len(strings.TrimLeft(line, " "))
		if indent <= keyIndent || strings.ContainsRune(line, '\t') {
			break
		}
		if blockIndent < 0 {
			blockIndent = indent
		}
		if indent < blockIndent {
			return "", 0, fmt.Errorf("a block line is less indented than its first line")
		}
		if indent > blockIndent && indicator[0] == '>' {
			return "", 0, fmt.Errorf("a more indented line in a folded block is not read")
		}
		content = append(content, line[blockIndent:])
	}
	if blockIndent < 0 {
		return "", 0, fmt.Errorf("the block holds nothing")
	}
	trailing := 0
	for len(content) > 0 && content[len(content)-1] == "" {
		content, trailing = content[:len(content)-1], trailing+1
	}
	var text string
	if indicator[0] == '|' {
		text = strings.Join(content, "\n")
	} else {
		// Two lines join with a space; each empty line is one break.
		var folded strings.Builder
		for index, line := range content {
			switch {
			case index == 0:
				folded.WriteString(line)
			case line == "":
				folded.WriteString("\n")
			case content[index-1] == "":
				folded.WriteString(line)
			default:
				folded.WriteString(" " + line)
			}
		}
		text = folded.String()
	}
	switch {
	case strings.HasSuffix(indicator, "-"):
	case strings.HasSuffix(indicator, "+"):
		text += strings.Repeat("\n", trailing+1)
	default:
		text += "\n"
	}
	return text, end, nil
}

// frontmatterLine reads one "key: value" line; a nil value opens a map.
func frontmatterLine(line string) (string, *string, error) {
	key, value, ok := strings.Cut(line, ":")
	if !ok || !frontmatterKey.MatchString(key) {
		return "", nil, fmt.Errorf("not a key: value line")
	}
	if value == "" {
		return key, nil, nil
	}
	if value[0] != ' ' {
		return "", nil, fmt.Errorf("%s: a space follows the colon", key)
	}
	value = strings.TrimSpace(value)
	switch value {
	case "|", ">", "|-", ">-", "|+", ">+":
		// A block scalar, read by parseFrontmatter.
		return key, &value, nil
	}
	switch {
	case len(value) >= 2 && value[0] == '"' && value[len(value)-1] == '"':
		inner := value[1 : len(value)-1]
		if strings.ContainsAny(inner, `"\`) {
			return "", nil, fmt.Errorf("%s: escapes in a double-quoted value are not read", key)
		}
		return key, &inner, nil
	case len(value) >= 2 && value[0] == '\'' && value[len(value)-1] == '\'':
		inner := strings.ReplaceAll(value[1:len(value)-1], "''", "'")
		return key, &inner, nil
	case strings.ContainsAny(value[:1], "-?:,[]{}#&*!|>'\"%@`"), strings.Contains(value, " #"), strings.Contains(value, ": "):
		return "", nil, fmt.Errorf("%s: this YAML value is not read", key)
	}
	return key, &value, nil
}

// sameFrontmatter tells whether the frontmatter read from a SKILL.md is the
// one its server listed, field by field.
func sameFrontmatter(read map[string]any, listed map[string]any) bool {
	if len(read) != len(listed) {
		return false
	}
	for key, value := range read {
		switch value := value.(type) {
		case string:
			if listedValue, ok := listed[key].(string); !ok || listedValue != value {
				return false
			}
		case map[string]any:
			listedMap, ok := listed[key].(map[string]any)
			if !ok || !sameFrontmatter(value, listedMap) {
				return false
			}
		default:
			return false
		}
	}
	return true
}
