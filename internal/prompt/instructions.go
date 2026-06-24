package prompt

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	// BaseSystemPrompt is the default coding-agent instruction block.
	BaseSystemPrompt = `You are a local coding agent running inside a project.
Prefer find, grep, ls, and read before editing unfamiliar files.
Use find to discover files and directories by path substring.
Use grep to search files for literal text with path and line numbers.
Use write for new files, empty files, or complete rewrites.
Use edit for exact replacements in existing non-empty files.
To add a line with edit, replace a unique neighboring block with that same block plus the inserted line.
Use bash for verification commands such as tests and build checks.
Do not claim filesystem changes unless a tool result confirms them.`

	// MaxInstructionFileBytes caps each loaded project instruction file.
	MaxInstructionFileBytes = 32 * 1024

	// instructionFileName is the project instruction filename loaded into
	// model context.
	instructionFileName = "AGENTS.md"

	// systemFileName is the project system-extension filename loaded before
	// AGENTS.md instructions.
	systemFileName = "SYSTEM.md"
)

// ProjectContext describes project-derived prompt context pinned each turn.
type ProjectContext struct {
	// SystemText is the complete system prompt sent to the model.
	SystemText string

	// ConfigPrompt stores inline or file-backed prompt text loaded from
	// config.
	ConfigPrompt string

	// ConfigPromptPath stores the resolved prompt file when ConfigPrompt
	// came from a file.
	ConfigPromptPath string

	// InstructionFiles stores AGENTS.md files included in SystemText.
	InstructionFiles []InstructionFile

	// SystemFiles stores SYSTEM.md files included in SystemText.
	SystemFiles []InstructionFile

	// Skills stores discovered skill packages summarized in SystemText.
	Skills []Skill
}

// ProjectContextOptions supplies config-derived prompt extensions without
// coupling prompt loading to the config package.
type ProjectContextOptions struct {
	// ConfigPath is the loaded TOML config path used to resolve relative
	// config prompt files.
	ConfigPath string

	// SystemPrompt is inline project-level prompt text from config.
	SystemPrompt string

	// SystemPromptFile is a path to project-level prompt text from config.
	SystemPromptFile string
}

// SystemText returns base instructions plus discovered project instructions.
func SystemText(cwd string) (string, error) {
	project, err := LoadProjectContext(cwd)
	if err != nil {
		return "", err
	}

	return project.SystemText, nil
}

// LoadProjectContext returns pinned instructions and summarized skills.
func LoadProjectContext(cwd string) (ProjectContext, error) {
	return LoadProjectContextWithOptions(cwd, ProjectContextOptions{})
}

// LoadProjectContextWithOptions returns pinned instructions, config prompt
// extensions, and summarized skills.
func LoadProjectContextWithOptions(cwd string,
	opts ProjectContextOptions) (ProjectContext, error) {

	configPrompt, configPromptPath, err := loadConfigPrompt(cwd, opts)
	if err != nil {
		return ProjectContext{}, err
	}
	systemFiles, err := LoadSystemFiles(cwd)
	if err != nil {
		return ProjectContext{}, err
	}
	files, err := LoadInstructionFiles(cwd)
	if err != nil {
		return ProjectContext{}, err
	}
	skills, err := LoadSkills(cwd)
	if err != nil {
		return ProjectContext{}, err
	}

	var out strings.Builder
	out.WriteString(BaseSystemPrompt)
	if strings.TrimSpace(configPrompt) != "" {
		out.WriteString("\n\nProject prompt from ")
		if configPromptPath != "" {
			out.WriteString(configPromptPath)
		} else {
			out.WriteString("config")
		}
		out.WriteString(":\n")
		out.WriteString(strings.TrimRight(configPrompt, "\n"))
	}
	for _, file := range systemFiles {
		out.WriteString("\n\nProject system prompt from ")
		out.WriteString(file.Path)
		out.WriteString(":\n")
		out.WriteString(file.Text)
	}
	for _, file := range files {
		out.WriteString("\n\nProject instructions from ")
		out.WriteString(file.Path)
		out.WriteString(":\n")
		out.WriteString(file.Text)
	}
	appendSkillCatalog(&out, skills)

	return ProjectContext{
		SystemText:       out.String(),
		ConfigPrompt:     configPrompt,
		ConfigPromptPath: configPromptPath,
		SystemFiles:      append([]InstructionFile{}, systemFiles...),
		InstructionFiles: append([]InstructionFile{}, files...),
		Skills:           append([]Skill{}, skills...),
	}, nil
}

// loadConfigPrompt reads inline or file-backed prompt text from options.
func loadConfigPrompt(cwd string, opts ProjectContextOptions) (string, string,
	error) {

	if strings.TrimSpace(opts.SystemPrompt) != "" &&
		strings.TrimSpace(opts.SystemPromptFile) != "" {
		return "", "", fmt.Errorf("project prompt must set only one " +
			"of system_prompt or system_prompt_file")
	}
	if strings.TrimSpace(opts.SystemPromptFile) == "" {
		return opts.SystemPrompt, "", nil
	}
	path, err := resolveConfigPromptPath(
		cwd, opts.ConfigPath, opts.SystemPromptFile,
	)
	if err != nil {
		return "", "", err
	}
	// #nosec G304 -- project prompt files are explicit user config.
	content, err := os.ReadFile(path)
	if err != nil {
		return "", "", fmt.Errorf("read project prompt %s: %w", path,
			err)
	}

	return string(content), path, nil
}

// resolveConfigPromptPath resolves prompt paths like other config-local file
// references: absolute paths stay absolute, ~ expands to home, relative paths
// are anchored to the config file directory when available.
func resolveConfigPromptPath(cwd string, configPath string,
	path string) (string, error) {

	if strings.HasPrefix(path, "~") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve home directory: %w", err)
		}
		path = filepath.Join(home, strings.TrimPrefix(path, "~"))
	}
	if filepath.IsAbs(path) {
		return path, nil
	}
	base := cwd
	if configPath != "" {
		base = filepath.Dir(configPath)
	}

	return filepath.Join(base, path), nil
}

// appendSkillCatalog adds compact skill metadata without full skill bodies.
func appendSkillCatalog(out *strings.Builder, skills []Skill) {
	out.WriteString(skillCatalogText(skills))
}

// skillCatalogText renders compact skill metadata for pinned context.
func skillCatalogText(skills []Skill) string {
	if len(skills) == 0 {
		return ""
	}

	var out strings.Builder
	out.WriteString("\n\nAvailable skills:\n")
	out.WriteString("The following skill packages are available by ")
	out.WriteString("description. Read the referenced SKILL.md only when ")
	out.WriteString(
		"the task matches or the user explicitly asks for it.\n",
	)
	for _, skill := range skills {
		out.WriteString("- ")
		out.WriteString(skill.Name)
		out.WriteString(": ")
		out.WriteString(skill.Description)
		out.WriteString(" (")
		out.WriteString(skill.Path)
		out.WriteString(")\n")
	}

	return out.String()
}

// InstructionFile stores one loaded project instruction file.
type InstructionFile struct {
	// Path is the absolute filesystem path that was loaded.
	Path string

	// Text is the possibly truncated instruction file content.
	Text string
}

// LoadSystemFiles loads SYSTEM.md files from cwd and its ancestors.
func LoadSystemFiles(cwd string) ([]InstructionFile, error) {
	return loadNamedInstructionFiles(cwd, systemFileName)
}

// LoadInstructionFiles loads AGENTS.md files from cwd and its ancestors.
func LoadInstructionFiles(cwd string) ([]InstructionFile, error) {
	return loadNamedInstructionFiles(cwd, instructionFileName)
}

// loadNamedInstructionFiles loads named instruction files from ancestors.
func loadNamedInstructionFiles(cwd string, name string) ([]InstructionFile,
	error) {

	dirs, err := ancestorDirs(cwd)
	if err != nil {
		return nil, err
	}

	var files []InstructionFile
	for _, dir := range dirs {
		path := filepath.Join(dir, name)
		text, ok, err := readInstructionFile(path)
		if err != nil {
			return nil, err
		}
		if ok {
			files = append(files, InstructionFile{
				Path: path,
				Text: text,
			})
		}
	}

	return files, nil
}

// ancestorDirs returns absolute directories ordered from root to cwd.
func ancestorDirs(cwd string) ([]string, error) {
	if strings.TrimSpace(cwd) == "" {
		cwd = "."
	}
	abs, err := filepath.Abs(cwd)
	if err != nil {
		return nil, fmt.Errorf("resolve instruction cwd: %w", err)
	}

	var reversed []string
	for {
		reversed = append(reversed, abs)
		parent := filepath.Dir(abs)
		if parent == abs {
			break
		}
		abs = parent
	}

	dirs := make([]string, 0, len(reversed))
	for i := len(reversed) - 1; i >= 0; i-- {
		dirs = append(dirs, reversed[i])
	}

	return dirs, nil
}

// readInstructionFile reads one instruction file when it exists.
func readInstructionFile(path string) (string, bool, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", false, nil
		}

		return "", false, fmt.Errorf("read instruction file %s: %w",
			path, err)
	}
	if len(content) <= MaxInstructionFileBytes {
		return strings.TrimRight(string(content), "\n"), true, nil
	}

	text := string(content[:MaxInstructionFileBytes])
	text = strings.TrimRight(text, "\n")
	text += fmt.Sprintf("\n\n[truncated %d bytes]",
		len(content)-MaxInstructionFileBytes)

	return text, true, nil
}
