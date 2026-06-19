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
)

// SystemText returns base instructions plus discovered project instructions.
func SystemText(cwd string) (string, error) {
	files, err := LoadInstructionFiles(cwd)
	if err != nil {
		return "", err
	}
	if len(files) == 0 {
		return BaseSystemPrompt, nil
	}

	var out strings.Builder
	out.WriteString(BaseSystemPrompt)
	for _, file := range files {
		out.WriteString("\n\nProject instructions from ")
		out.WriteString(file.Path)
		out.WriteString(":\n")
		out.WriteString(file.Text)
	}

	return out.String(), nil
}

// InstructionFile stores one loaded project instruction file.
type InstructionFile struct {
	// Path is the absolute filesystem path that was loaded.
	Path string

	// Text is the possibly truncated instruction file content.
	Text string
}

// LoadInstructionFiles loads AGENTS.md files from cwd and its ancestors.
func LoadInstructionFiles(cwd string) ([]InstructionFile, error) {
	dirs, err := ancestorDirs(cwd)
	if err != nil {
		return nil, err
	}

	var files []InstructionFile
	for _, dir := range dirs {
		path := filepath.Join(dir, instructionFileName)
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
