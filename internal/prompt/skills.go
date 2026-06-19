package prompt

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	// skillFileName is the conventional instruction file for one skill.
	skillFileName = "SKILL.md"

	// maxSkillMetadataBytes caps the amount read while parsing metadata.
	maxSkillMetadataBytes = 16 * 1024
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
		filepath.Join(dir, ".harness", "skills"),
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

// readSkillMetadata reads compact frontmatter for one SKILL.md file.
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
	if name == "" {
		name = fallbackName
	}
	description := strings.TrimSpace(metadata["description"])
	if description == "" {
		return Skill{}, false, nil
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
	for _, line := range lines[1:] {
		if strings.TrimSpace(line) == "---" {
			return metadata, true
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		key = strings.TrimSpace(strings.ToLower(key))
		value = strings.TrimSpace(value)
		metadata[key] = strings.Trim(value, `"'`)
	}

	return nil, false
}
