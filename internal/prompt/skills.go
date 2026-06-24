package prompt

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

const (
	// skillFileName is the conventional instruction file for one skill.
	skillFileName = "SKILL.md"

	// maxSkillMetadataBytes caps the amount read while parsing metadata.
	maxSkillMetadataBytes = 16 * 1024

	// maxSkillNameChars is the Agent Skills name length limit.
	maxSkillNameChars = 64

	// maxSkillDescriptionChars is the Agent Skills description limit.
	maxSkillDescriptionChars = 1024
)

// Skill describes one discovered skill package available to the model.
type Skill struct {
	// Name is the stable identifier used in the skill catalog.
	Name string

	// Description explains when the model should load the skill.
	Description string

	// Path is the absolute path to the skill's SKILL.md file.
	Path string
}

// LoadSkills discovers project skills under supported skill directories.
func LoadSkills(cwd string) ([]Skill, error) {
	dirs, err := ancestorDirs(cwd)
	if err != nil {
		return nil, err
	}

	var skills []Skill
	for _, dir := range dirs {
		for _, root := range skillRoots(dir) {
			found, err := loadSkillsFromRoot(root)
			if err != nil {
				return nil, err
			}
			skills = append(skills, found...)
		}
	}

	return skills, nil
}

// skillRoots returns supported skill package roots for one project directory.
func skillRoots(dir string) []string {
	return []string{
		filepath.Join(dir, ".etch", "skills"),
		filepath.Join(dir, ".agents", "skills"),
	}
}

// loadSkillsFromRoot discovers immediate child skill packages under root.
func loadSkillsFromRoot(root string) ([]Skill, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}

		return nil, fmt.Errorf("read skill root %s: %w", root, err)
	}

	var skills []Skill
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		path := filepath.Join(root, entry.Name(), skillFileName)
		skill, ok, err := readSkillMetadata(path, entry.Name())
		if err != nil {
			return nil, err
		}
		if ok {
			skills = append(skills, skill)
		}
	}

	return skills, nil
}

// readSkillMetadata reads and validates compact frontmatter for one SKILL.md.
func readSkillMetadata(path string, fallbackName string) (Skill, bool, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Skill{}, false, nil
		}

		return Skill{}, false, fmt.Errorf("read skill %s: %w", path,
			err)
	}
	if len(content) > maxSkillMetadataBytes {
		content = content[:maxSkillMetadataBytes]
	}

	metadata, ok := parseSkillFrontmatter(string(content))
	if !ok {
		return Skill{}, false, nil
	}

	name := strings.TrimSpace(metadata["name"])
	if err := validateSkillName(name, fallbackName); err != nil {
		return Skill{}, false, fmt.Errorf("invalid skill %s: %w", path,
			err)
	}
	description := strings.TrimSpace(metadata["description"])
	if err := validateSkillDescription(description); err != nil {
		return Skill{}, false, fmt.Errorf("invalid skill %s: %w", path,
			err)
	}

	return Skill{
		Name:        name,
		Description: description,
		Path:        path,
	}, true, nil
}

// parseSkillFrontmatter returns simple key/value metadata from markdown.
func parseSkillFrontmatter(text string) (map[string]string, bool) {
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return nil, false
	}

	metadata := make(map[string]string)
	for i := 1; i < len(lines); i++ {
		line := lines[i]
		if strings.TrimSpace(line) == "---" {
			return metadata, true
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		key = strings.TrimSpace(strings.ToLower(key))
		value = strings.TrimSpace(value)
		if value == "|" || value == ">" {
			block, next := parseSkillBlockValue(
				lines, i+1, value == ">",
			)
			metadata[key] = block
			i = next - 1

			continue
		}
		metadata[key] = strings.Trim(value, `"'`)
	}

	return nil, false
}

// parseSkillBlockValue parses a simple indented YAML block scalar.
func parseSkillBlockValue(lines []string, start int,
	folded bool) (string, int) {

	var block []string
	for i := start; i < len(lines); i++ {
		line := lines[i]
		if strings.TrimSpace(line) == "---" {
			return joinSkillBlock(block, folded), i
		}
		if strings.TrimSpace(line) != "" &&
			!strings.HasPrefix(line, " ") &&
			!strings.HasPrefix(line, "\t") {
			return joinSkillBlock(block, folded), i
		}
		block = append(block, strings.TrimSpace(line))
	}

	return joinSkillBlock(block, folded), len(lines)
}

// joinSkillBlock joins block scalar lines according to the scalar style.
func joinSkillBlock(lines []string, folded bool) string {
	if folded {
		return strings.Join(lines, " ")
	}

	return strings.Join(lines, "\n")
}

// validateSkillName enforces the Agent Skills name field constraints.
func validateSkillName(name string, folder string) error {
	if name == "" {
		return fmt.Errorf("name is required")
	}
	if utf8.RuneCountInString(name) > maxSkillNameChars {
		return fmt.Errorf("name exceeds %d characters",
			maxSkillNameChars)
	}
	if name != folder {
		return fmt.Errorf("name %q must match parent directory %q",
			name, folder)
	}
	if strings.HasPrefix(name, "-") || strings.HasSuffix(name, "-") {
		return fmt.Errorf("name must not start or end with hyphen")
	}
	if strings.Contains(name, "--") {
		return fmt.Errorf("name must not contain consecutive hyphens")
	}
	for _, r := range name {
		if r >= 'a' && r <= 'z' {
			continue
		}
		if r >= '0' && r <= '9' {
			continue
		}
		if r == '-' {
			continue
		}

		return fmt.Errorf("name contains invalid character %q", r)
	}

	return nil
}

// validateSkillDescription enforces the Agent Skills description constraints.
func validateSkillDescription(description string) error {
	if description == "" {
		return fmt.Errorf("description is required")
	}
	if utf8.RuneCountInString(description) > maxSkillDescriptionChars {
		return fmt.Errorf("description exceeds %d characters",
			maxSkillDescriptionChars)
	}

	return nil
}
