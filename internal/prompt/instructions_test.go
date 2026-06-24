package prompt

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSystemTextLoadsAncestorInstructions verifies that project instructions
// are loaded from parent directories before child directories.
func TestSystemTextLoadsAncestorInstructions(t *testing.T) {
	root := t.TempDir()
	child := filepath.Join(root, "child")
	if err := os.Mkdir(child, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(root, "AGENTS.md"), "root rules\n")
	writeFile(t, filepath.Join(child, "AGENTS.md"), "child rules\n")

	text, err := SystemText(child)
	if err != nil {
		t.Fatal(err)
	}
	rootIndex := strings.Index(text, "root rules")
	childIndex := strings.Index(text, "child rules")
	if rootIndex < 0 || childIndex < 0 {
		t.Fatalf("missing instructions: %q", text)
	}
	if rootIndex > childIndex {
		t.Fatalf("instructions out of order: %q", text)
	}
}

// TestSystemTextLoadsSystemFilesBeforeInstructions verifies project system
// prompt extensions are pinned ahead of AGENTS.md instructions.
func TestSystemTextLoadsSystemFilesBeforeInstructions(t *testing.T) {
	root := t.TempDir()
	child := filepath.Join(root, "child")
	if err := os.Mkdir(child, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(root, "SYSTEM.md"), "root system\n")
	writeFile(t, filepath.Join(child, "SYSTEM.md"), "child system\n")
	writeFile(t, filepath.Join(root, "AGENTS.md"), "root rules\n")
	writeFile(t, filepath.Join(child, "AGENTS.md"), "child rules\n")

	project, err := LoadProjectContext(child)
	if err != nil {
		t.Fatal(err)
	}
	if len(project.SystemFiles) != 2 {
		t.Fatalf("expected two system files, got %d",
			len(project.SystemFiles))
	}
	rootSystem := strings.Index(project.SystemText, "root system")
	childSystem := strings.Index(project.SystemText, "child system")
	rootRules := strings.Index(project.SystemText, "root rules")
	childRules := strings.Index(project.SystemText, "child rules")
	if rootSystem < 0 || childSystem < 0 || rootRules < 0 ||
		childRules < 0 {

		t.Fatalf("missing context layer: %q", project.SystemText)
	}
	if rootSystem >= childSystem || childSystem >= rootRules ||
		rootRules >= childRules {

		t.Fatalf("context layers out of order: %q", project.SystemText)
	}
}

// TestSystemTextIncludesConfigPromptBeforeProjectFiles verifies config prompt
// text extends the base prompt before SYSTEM.md and AGENTS.md layers.
func TestSystemTextIncludesConfigPromptBeforeProjectFiles(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "SYSTEM.md"), "system identity\n")
	writeFile(t, filepath.Join(dir, "AGENTS.md"), "repo rules\n")

	project, err := LoadProjectContextWithOptions(
		dir, ProjectContextOptions{
			SystemPrompt: "prefer go_inspect",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	configIndex := strings.Index(project.SystemText, "prefer go_inspect")
	systemIndex := strings.Index(project.SystemText, "system identity")
	rulesIndex := strings.Index(project.SystemText, "repo rules")
	if configIndex < 0 || systemIndex < 0 || rulesIndex < 0 {
		t.Fatalf("missing prompt layer: %q", project.SystemText)
	}
	if configIndex >= systemIndex || systemIndex >= rulesIndex {
		t.Fatalf("prompt layers out of order: %q", project.SystemText)
	}
}

// TestSystemTextLoadsConfigPromptFile verifies config prompt files resolve
// relative to the config file location.
func TestSystemTextLoadsConfigPromptFile(t *testing.T) {
	root := t.TempDir()
	configDir := filepath.Join(root, ".etch")
	writeFile(t, filepath.Join(configDir, "agent-policy.md"), "use tools\n")

	project, err := LoadProjectContextWithOptions(
		root, ProjectContextOptions{
			ConfigPath: filepath.Join(
				configDir, "config.toml",
			),
			SystemPromptFile: "agent-policy.md",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(project.SystemText, "use tools") {
		t.Fatalf("missing config prompt file: %q", project.SystemText)
	}
}

// TestLoadInstructionFilesTruncatesLargeFiles verifies that large instruction
// files cannot dominate the first prompt context.
func TestLoadInstructionFilesTruncatesLargeFiles(t *testing.T) {
	dir := t.TempDir()
	writeFile(
		t, filepath.Join(dir, "AGENTS.md"),
		strings.Repeat("x", MaxInstructionFileBytes+10),
	)

	files, err := LoadInstructionFiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 {
		t.Fatalf("expected one file, got %d", len(files))
	}
	if !strings.Contains(files[0].Text, "[truncated 10 bytes]") {
		t.Fatalf("missing truncation marker: %q", files[0].Text)
	}
}

// TestLoadSystemFilesTruncatesLargeFiles verifies SYSTEM.md uses the same
// bounded loading policy as AGENTS.md.
func TestLoadSystemFilesTruncatesLargeFiles(t *testing.T) {
	dir := t.TempDir()
	writeFile(
		t, filepath.Join(dir, "SYSTEM.md"),
		strings.Repeat("y", MaxInstructionFileBytes+7),
	)

	files, err := LoadSystemFiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 {
		t.Fatalf("expected one file, got %d", len(files))
	}
	if !strings.Contains(files[0].Text, "[truncated 7 bytes]") {
		t.Fatalf("missing truncation marker: %q", files[0].Text)
	}
}

// TestSystemTextOrdersSkillsAfterPinnedFiles verifies the skill catalog stays
// behind project system and instruction files.
func TestSystemTextOrdersSkillsAfterPinnedFiles(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "SYSTEM.md"), "system identity\n")
	writeFile(t, filepath.Join(dir, "AGENTS.md"), "repo rules\n")
	writeFile(
		t, filepath.Join(
			dir, ".etch", "skills", "go-style", "SKILL.md",
		),
		"---\nname: go-style\ndescription: Use for Go edits.\n---\n",
	)

	text, err := SystemText(dir)
	if err != nil {
		t.Fatal(err)
	}
	systemIndex := strings.Index(text, "system identity")
	rulesIndex := strings.Index(text, "repo rules")
	skillsIndex := strings.Index(text, "Available skills:")
	if systemIndex < 0 || rulesIndex < 0 || skillsIndex < 0 {
		t.Fatalf("missing context layer: %q", text)
	}
	if systemIndex >= rulesIndex || rulesIndex >= skillsIndex {
		t.Fatalf("context layers out of order: %q", text)
	}
}

// TestSystemTextIncludesSkillCatalog verifies skill metadata is pinned while
// full skill bodies stay out of the default prompt context.
func TestSystemTextIncludesSkillCatalog(t *testing.T) {
	dir := t.TempDir()
	skillDir := filepath.Join(dir, ".etch", "skills", "go-style")
	writeFile(
		t, filepath.Join(skillDir, "SKILL.md"),
		"---\nname: go-style\ndescription: Use for Go "+
			"edits.\n---\n\n# Secret Body\n\nDetailed workflow "+
			"stays unloaded.\n",
	)

	text, err := SystemText(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "Available skills:") {
		t.Fatalf("missing skill catalog: %q", text)
	}
	if !strings.Contains(text, "go-style: Use for Go edits.") {
		t.Fatalf("missing skill metadata: %q", text)
	}
	if strings.Contains(text, "Detailed workflow stays unloaded.") {
		t.Fatalf("skill body leaked into system text: %q", text)
	}
}

// TestLoadSkillsDiscoversProjectSkillRoots verifies both supported project
// skill directories contribute SKILL.md metadata.
func TestLoadSkillsDiscoversProjectSkillRoots(t *testing.T) {
	root := t.TempDir()
	child := filepath.Join(root, "child")
	if err := os.Mkdir(child, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(
		t, filepath.Join(
			root, ".etch", "skills", "root-skill", "SKILL.md",
		),
		"---\nname: root-skill\ndescription: Use at root.\n---\n",
	)
	writeFile(
		t,
		filepath.Join(child, ".agents", "skills", "child", "SKILL.md"),
		"---\nname: child\ndescription: Use at child.\n---\n",
	)

	skills, err := LoadSkills(child)
	if err != nil {
		t.Fatal(err)
	}
	if len(skills) != 2 {
		t.Fatalf("expected two skills, got %d: %#v", len(skills),
			skills)
	}
	if skills[0].Name != "root-skill" || skills[1].Name != "child" {
		t.Fatalf("unexpected skill order or names: %#v", skills)
	}
}

// TestLoadSkillsRejectsInvalidSkillMetadata verifies discovery reports
// standard violations instead of quietly loading malformed skills.
func TestLoadSkillsRejectsInvalidSkillMetadata(t *testing.T) {
	dir := t.TempDir()
	writeFile(
		t, filepath.Join(
			dir, ".etch", "skills", "Bad_Name", "SKILL.md",
		),
		"---\nname: Bad_Name\ndescription: Use badly.\n---\n",
	)

	_, err := LoadSkills(dir)
	if err == nil {
		t.Fatal("expected invalid skill metadata error")
	}
	if !strings.Contains(err.Error(), "invalid character") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestLoadSkillsParsesBlockDescription verifies the tiny stdlib parser accepts
// common YAML block descriptions used by Agent Skills examples.
func TestLoadSkillsParsesBlockDescription(t *testing.T) {
	dir := t.TempDir()
	writeFile(
		t, filepath.Join(dir, ".etch", "skills", "blocky", "SKILL.md"),
		"---\nname: blocky\ndescription: |\n  Use for block "+
			"descriptions.\n  Trigger on multiline "+
			"metadata.\n---\n",
	)

	skills, err := LoadSkills(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(skills) != 1 {
		t.Fatalf("expected one skill, got %d", len(skills))
	}
	if !strings.Contains(
		skills[0].Description, "Trigger on multiline metadata.",
	) {

		t.Fatalf("missing block description: %#v", skills[0])
	}
}

// writeFile writes one test fixture file.
func writeFile(t *testing.T, path string, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
