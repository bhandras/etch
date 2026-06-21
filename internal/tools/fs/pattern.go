package fs

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// ignoreRule stores one parsed root .gitignore pattern.
type ignoreRule struct {
	// Pattern is the slash-separated glob body.
	Pattern string

	// Negated reports whether this rule re-includes matching paths.
	Negated bool

	// DirectoryOnly reports whether the rule applies only to directories.
	DirectoryOnly bool

	// Rooted reports whether the pattern is anchored at the walk root.
	Rooted bool

	// HasSlash reports whether Pattern contains an explicit path segment.
	HasSlash bool
}

// ignoreMatcher applies root .gitignore rules in file order.
type ignoreMatcher struct {
	// Rules stores parsed .gitignore rules relative to the walk root.
	Rules []ignoreRule
}

// loadIgnoreMatcher loads the root .gitignore subset used by find and grep.
func loadIgnoreMatcher(root string) ignoreMatcher {
	content, err := os.ReadFile(filepath.Join(root, ".gitignore"))
	if err != nil {
		return ignoreMatcher{}
	}

	var rules []ignoreRule
	for _, line := range strings.Split(string(content), "\n") {
		rule, ok := parseIgnoreRule(line)
		if ok {
			rules = append(rules, rule)
		}
	}

	return ignoreMatcher{Rules: rules}
}

// parseIgnoreRule converts one .gitignore line into the supported subset.
func parseIgnoreRule(line string) (ignoreRule, bool) {
	line = strings.TrimSpace(line)
	if line == "" || strings.HasPrefix(line, "#") {
		return ignoreRule{}, false
	}
	rule := ignoreRule{}
	if strings.HasPrefix(line, "!") {
		rule.Negated = true
		line = strings.TrimPrefix(line, "!")
	}
	line = strings.TrimSpace(line)
	if line == "" {
		return ignoreRule{}, false
	}
	if strings.HasSuffix(line, "/") {
		rule.DirectoryOnly = true
		line = strings.TrimSuffix(line, "/")
	}
	if strings.HasPrefix(line, "/") {
		rule.Rooted = true
		line = strings.TrimPrefix(line, "/")
	}
	line = filepath.ToSlash(filepath.Clean(line))
	if line == "." || strings.HasPrefix(line, "../") {
		return ignoreRule{}, false
	}
	rule.Pattern = line
	rule.HasSlash = strings.Contains(line, "/")

	return rule, true
}

// Ignored reports whether rendered should be skipped by the parsed rules.
func (m ignoreMatcher) Ignored(rendered string, isDir bool) bool {
	ignored := false
	for _, rule := range m.Rules {
		if rule.Matches(rendered, isDir) {
			ignored = !rule.Negated
		}
	}

	return ignored
}

// Matches reports whether one ignore rule applies to rendered.
func (r ignoreRule) Matches(rendered string, isDir bool) bool {
	if r.DirectoryOnly && !isDir {
		return false
	}
	rendered = strings.TrimSuffix(filepath.ToSlash(rendered), "/")
	if rendered == "" {
		return false
	}

	if r.Rooted || r.HasSlash {
		return globMatches(r.Pattern, rendered)
	}
	for _, candidate := range pathSegments(rendered) {
		if globMatches(r.Pattern, candidate) {
			return true
		}
	}

	return false
}

// pathSegments returns rendered and every basename segment in rendered.
func pathSegments(rendered string) []string {
	parts := strings.Split(rendered, "/")
	segments := make([]string, 0, len(parts)+1)
	segments = append(segments, rendered)
	segments = append(segments, parts...)

	return segments
}

// globMatches reports whether pattern matches rendered using slash paths.
func globMatches(pattern string, rendered string) bool {
	ok, err := matchPathGlob(pattern, rendered)

	return err == nil && ok
}

// matchPathGlob matches shell-style globs with recursive ** support.
func matchPathGlob(pattern string, rendered string) (bool, error) {
	pattern = strings.TrimSpace(filepath.ToSlash(pattern))
	rendered = strings.TrimSuffix(filepath.ToSlash(rendered), "/")
	if pattern == "" {
		return true, nil
	}
	if !strings.Contains(pattern, "/") &&
		!strings.Contains(pattern, "**") {
		return path.Match(pattern, path.Base(rendered))
	}

	patternParts := strings.Split(strings.Trim(pattern, "/"), "/")
	renderedParts := strings.Split(strings.Trim(rendered, "/"), "/")

	return matchGlobParts(patternParts, renderedParts)
}

// matchGlobParts recursively matches slash-separated glob segments.
func matchGlobParts(patternParts []string,
	renderedParts []string) (bool, error) {

	if len(patternParts) == 0 {
		return len(renderedParts) == 0, nil
	}
	if patternParts[0] == "**" {
		for i := 0; i <= len(renderedParts); i++ {
			ok, err := matchGlobParts(
				patternParts[1:], renderedParts[i:],
			)
			if err != nil || ok {
				return ok, err
			}
		}

		return false, nil
	}
	if len(renderedParts) == 0 {
		return false, nil
	}
	ok, err := path.Match(patternParts[0], renderedParts[0])
	if err != nil {
		return false, fmt.Errorf("match glob segment %q: %w",
			patternParts[0], err)
	}
	if !ok {
		return false, nil
	}

	return matchGlobParts(patternParts[1:], renderedParts[1:])
}
