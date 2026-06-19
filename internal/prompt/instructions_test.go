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

// TestSystemTextIncludesSkillCatalog verifies skill metadata is pinned while
// full skill bodies stay out of the default prompt context.
func TestSystemTextIncludesSkillCatalog(t *testing.T) {
	dir := t.TempDir()
	skillDir := filepath.Join(dir, ".harness", "skills", "go-style")
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
			root, ".harness", "skills", "root-skill", "SKILL.md",
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
			dir, ".harness", "skills", "Bad_Name", "SKILL.md",
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
		t,
		filepath.Join(dir, ".harness", "skills", "blocky", "SKILL.md"),
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
