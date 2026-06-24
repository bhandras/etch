package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode"

	"etch/sdk"
)

const (
	// toolGoInspect inspects Go source through one compact schema.
	toolGoInspect = "go_inspect"

	// defaultSymbolLimit bounds symbol output when no caller limit is
	// supplied.
	defaultSymbolLimit = 200

	// maxSymbolLimit prevents accidental giant symbol listings.
	maxSymbolLimit = 2000

	// maxSymbolPaths bounds multi-root symbol requests.
	maxSymbolPaths = 8

	// maxRegexBytes bounds regex size before compilation.
	maxRegexBytes = 512

	// detailNone renders one compact row per symbol.
	detailNone = "none"

	// detailPackage renders package/file/symbol maps for broad exploration.
	detailPackage = "package"

	// detailSummary renders metadata, godoc, and compact declarations.
	detailSummary = "summary"

	// detailFull renders full source declarations with line numbers.
	detailFull = "full"
)

// symbolsArgs stores the single model-facing Go source inspection query.
type symbolsArgs struct {
	// Paths are Go files or directories to inspect. Empty means current
	// directory.
	Paths []string `json:"paths"`

	// Package is an optional case-insensitive regex over package name or
	// relative package directory.
	Package string `json:"package"`

	// File is an optional case-insensitive regex over repo-relative,
	// root-relative, or raw file path labels.
	File string `json:"file"`

	// Name is an optional case-insensitive regex over symbol name. Methods
	// use Receiver.Method names.
	Name string `json:"name"`

	// IncludeUnexported includes lowercase package symbols when true.
	IncludeUnexported bool `json:"includeUnexported"`

	// Detail controls rendered output detail: package, none, summary, or
	// full.
	Detail string `json:"detail"`

	// Limit caps the number of symbols rendered.
	Limit int `json:"limit"`
}

// packageInfo stores parsed Go package metadata and symbols.
type packageInfo struct {
	dir     string
	relDir  string
	name    string
	files   []string
	symbols []symbolInfo
}

// symbolInfo stores one top-level Go declaration discovered by the plugin.
type symbolInfo struct {
	name               string
	kind               string
	packageName        string
	dir                string
	relDir             string
	file               string
	relFile            string
	displayFile        string
	line               int
	endLine            int
	sourceLine         int
	doc                string
	fullDeclaration    string
	summaryDeclaration string
}

// symbolRegex stores one optional case-insensitive regex filter.
type symbolRegex struct {
	label    string
	raw      string
	compiled *regexp.Regexp
}

// symbolFilters stores compiled filters for one go_inspect call.
type symbolFilters struct {
	packageRegex symbolRegex
	fileRegex    symbolRegex
	nameRegex    symbolRegex
	includeAll   bool
}

// main serves the Go intelligence plugin protocol until stdin closes.
func main() {
	if err := sdk.ServePlugin(sdk.Plugin{
		Name: "go-intel",
		Tools: []sdk.Tool{
			goInspectSpec(),
		},
	}); err != nil {

		fmt.Fprintln(os.Stderr, err)
	}
}

// goInspectSpec returns the schema for the single Go source inspection tool.
func goInspectSpec() sdk.Tool {
	return sdk.Tool{
		Name: toolGoInspect,
		Description: "Inspect Go packages, files, symbols, and source " +
			"declarations with case-insensitive Go regex filters " +
			"over package, file, and symbol name. Omit a filter to " +
			"match everything. Methods are named Receiver.Method. " +
			"Use detail=package or none to map code, summary after " +
			"narrowing, and full to read actual Go source " +
			"declarations, including function bodies, without a " +
			"separate read call.",
		ParallelSafety: sdk.ParallelSafetyReadOnly,
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"paths": arrayStringSchema(
					"Go files or directories to " +
						"inspect. Defaults to " +
						"[\".\"]. Multiple roots " +
						"are searched in one call.",
				),
				"package": stringSchema(
					"Optional case-insensitive regex " +
						"matched against package " +
						"name and relative package " +
						"directory, for example " +
						"\"plugins\" or " +
						"\"internal/plugins\".",
				),
				"file": stringSchema(
					"Optional case-insensitive regex " +
						"matched against displayed " +
						"repo-relative paths, " +
						"root-relative paths, and " +
						"raw paths. Examples: " +
						"\"client\\\\.go$\", " +
						"\"_test\\\\.go$\", or " +
						"\"internal/plugins\".",
				),
				"name": stringSchema(
					"Optional case-insensitive regex " +
						"matched against symbol " +
						"name. Methods are named " +
						"like \"Client.call\". " +
						"Examples: " +
						"\"^Client\\\\.\", " +
						"\"call$\", or " +
						"\"^(Start|Close)$\".",
				),
				"includeUnexported": boolSchema(
					"Include lowercase package " +
						"symbols. Set true when " +
						"inspecting package internals.",
				),
				"detail": stringSchema(
					"Detail level: package, none, " +
						"summary, or full. " +
						"Defaults to summary. For " +
						"broad maps use package or " +
						"none; use summary only " +
						"after narrowing. Use full " +
						"to read matching Go " +
						"source declarations " +
						"directly; for functions " +
						"and methods this includes " +
						"the complete body.",
				),
				"limit": integerSchema(
					"Maximum symbols to render. " +
						"Defaults to 200 and caps " +
						"at 2000. Prefer lower " +
						"limits for summary or " +
						"full detail.",
				),
			},
		},
		Handler: handleGoInspect,
	}
}

// stringSchema returns a JSON schema property for a string argument.
func stringSchema(description string) map[string]any {
	return map[string]any{"type": "string", "description": description}
}

// arrayStringSchema returns a JSON schema property for a string array argument.
func arrayStringSchema(description string) map[string]any {
	return map[string]any{
		"type":        "array",
		"description": description,
		"items": map[string]any{
			"type": "string",
		},
	}
}

// boolSchema returns a JSON schema property for a boolean argument.
func boolSchema(description string) map[string]any {
	return map[string]any{"type": "boolean", "description": description}
}

// integerSchema returns a JSON schema property for an integer argument.
func integerSchema(description string) map[string]any {
	return map[string]any{"type": "integer", "description": description}
}

// handleGoInspect executes go_inspect through the SDK handler.
func handleGoInspect(ctx context.Context,
	call sdk.ToolCall) (sdk.ToolResult, error) {

	if err := ctx.Err(); err != nil {
		return sdk.ToolResult{}, err
	}
	text, err := runGoInspect(call.Arguments)
	if err != nil {
		return sdk.ToolResult{}, err
	}

	return sdk.TextResult(text), nil
}

// runGoInspect finds and renders Go source declarations matching regex filters.
func runGoInspect(raw json.RawMessage) (string, error) {
	var args symbolsArgs
	if err := decodeArguments(raw, &args); err != nil {
		return "", err
	}
	paths, err := normalizeSymbolPaths(args.Paths)
	if err != nil {
		return "", err
	}
	detail, err := normalizeDetail(args.Detail)
	if err != nil {
		return "", err
	}
	filters, err := compileSymbolFilters(args)
	if err != nil {
		return "", err
	}
	packages, err := parsePackagesFromPaths(paths)
	if err != nil {
		return "", err
	}
	all := allSymbols(packages)
	symbols := filterSymbols(all, filters)

	return formatSymbols(
		paths, packages, all, symbols, filters, detail,
		normalizeSymbolLimit(args.Limit),
	), nil
}

// decodeArguments unmarshals a possibly empty JSON object into out.
func decodeArguments(raw json.RawMessage, out any) error {
	if len(strings.TrimSpace(string(raw))) == 0 {
		raw = json.RawMessage(`{}`)
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("decode arguments: %w", err)
	}

	return nil
}

// normalizeSymbolPaths trims, validates, de-duplicates, and caps search roots.
func normalizeSymbolPaths(paths []string) ([]string, error) {
	if len(paths) == 0 {
		return []string{"."}, nil
	}
	roots := make([]string, 0, len(paths))
	seen := make(map[string]bool, len(paths))
	for index, path := range paths {
		root := strings.TrimSpace(path)
		if root == "" {
			return nil, fmt.Errorf("paths[%d] is empty", index)
		}
		if _, err := os.Stat(root); err != nil {
			return nil, fmt.Errorf("stat search root %q: %w", root,
				err)
		}
		clean := filepath.Clean(root)
		if seen[clean] {
			continue
		}
		roots = append(roots, root)
		seen[clean] = true
	}
	if len(roots) == 0 {
		return nil, fmt.Errorf("paths must contain at least one path")
	}
	if len(roots) > maxSymbolPaths {
		return nil, fmt.Errorf("paths accepts at most %d entries",
			maxSymbolPaths)
	}

	return roots, nil
}

// normalizeDetail returns the declaration detail mode for output rendering.
func normalizeDetail(detail string) (string, error) {
	detail = strings.ToLower(strings.TrimSpace(detail))
	if detail == "" {
		return detailSummary, nil
	}
	switch detail {
	case detailPackage, detailNone, detailSummary, detailFull:
		return detail, nil

	default:
		return "", fmt.Errorf("detail must be %q, %q, %q, or %q",
			detailPackage, detailNone, detailSummary, detailFull)
	}
}

// normalizeSymbolLimit returns a bounded symbol output limit.
func normalizeSymbolLimit(limit int) int {
	if limit <= 0 {
		return defaultSymbolLimit
	}
	if limit > maxSymbolLimit {
		return maxSymbolLimit
	}

	return limit
}

// compileSymbolFilters compiles all regex filters from one request.
func compileSymbolFilters(args symbolsArgs) (symbolFilters, error) {
	packageRegex, err := compileSymbolRegex("package", args.Package)
	if err != nil {
		return symbolFilters{}, err
	}
	fileRegex, err := compileSymbolRegex("file", args.File)
	if err != nil {
		return symbolFilters{}, err
	}
	nameRegex, err := compileSymbolRegex("name", args.Name)
	if err != nil {
		return symbolFilters{}, err
	}

	return symbolFilters{
		packageRegex: packageRegex,
		fileRegex:    fileRegex,
		nameRegex:    nameRegex,
		includeAll:   args.IncludeUnexported,
	}, nil
}

// compileSymbolRegex compiles one optional case-insensitive Go regex.
func compileSymbolRegex(label string, pattern string) (symbolRegex, error) {
	pattern = strings.TrimSpace(pattern)
	if pattern == "" {
		return symbolRegex{label: label}, nil
	}
	if len(pattern) > maxRegexBytes {
		return symbolRegex{}, fmt.Errorf("%s regex exceeds %d bytes",
			label, maxRegexBytes)
	}
	compiled, err := regexp.Compile("(?i:" + pattern + ")")
	if err != nil {
		return symbolRegex{}, fmt.Errorf("invalid %s regex %q: %w",
			label, pattern, err)
	}

	return symbolRegex{
		label:    label,
		raw:      pattern,
		compiled: compiled,
	}, nil
}

// matches reports whether text matches r or r is empty.
func (r symbolRegex) matches(text string) bool {
	if r.compiled == nil {
		return true
	}

	return r.compiled.MatchString(text)
}

// active reports whether r has a compiled regex.
func (r symbolRegex) active() bool {
	return r.compiled != nil
}

// parsePackagesFromPaths parses all requested roots into one package slice.
func parsePackagesFromPaths(paths []string) ([]packageInfo, error) {
	var packages []packageInfo
	multiRoot := len(paths) > 1
	for _, path := range paths {
		parsed, err := parsePackages(path)
		if err != nil {
			return nil, err
		}
		if multiRoot {
			prefixPackages(parsed, path)
		}
		packages = append(packages, parsed...)
	}

	return packages, nil
}

// parsePackages parses Go files under path into package summaries.
func parsePackages(path string) ([]packageInfo, error) {
	root, files, err := discoverGoFiles(path)
	if err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("no Go files found under %s", path)
	}

	fset := token.NewFileSet()
	groups := make(map[string]*packageInfo)
	for _, file := range files {
		parsed, err := parser.ParseFile(
			fset, file, nil, parser.ParseComments,
		)
		if err != nil {
			return nil, fmt.Errorf("parse %s: %w", file, err)
		}
		dir := filepath.Dir(file)
		pkgName := parsed.Name.Name
		key := dir + "\x00" + pkgName
		pkg := groups[key]
		if pkg == nil {
			pkg = &packageInfo{
				dir:    dir,
				relDir: relativePath(root, dir),
				name:   pkgName,
			}
			groups[key] = pkg
		}
		pkg.files = append(pkg.files, relativePath(root, file))
		pkg.symbols = append(
			pkg.symbols, fileSymbols(fset, root, file, parsed)...,
		)
	}

	packages := make([]packageInfo, 0, len(groups))
	for _, pkg := range groups {
		sort.Strings(pkg.files)
		sortSymbols(pkg.symbols)
		packages = append(packages, *pkg)
	}
	sort.Slice(packages, func(i, j int) bool {
		if packages[i].relDir == packages[j].relDir {
			return packages[i].name < packages[j].name
		}

		return packages[i].relDir < packages[j].relDir
	})

	return packages, nil
}

// discoverGoFiles returns the parse root and Go files selected by path.
func discoverGoFiles(path string) (string, []string, error) {
	clean := filepath.Clean(defaultPath(path))
	info, err := os.Stat(clean)
	if err != nil {
		return "", nil, fmt.Errorf("stat %s: %w", clean, err)
	}
	if !info.IsDir() {
		if filepath.Ext(clean) != ".go" {
			return "", nil, fmt.Errorf("not a Go source file: %s",
				clean)
		}

		return filepath.Dir(clean), []string{clean}, nil
	}

	var files []string
	err = filepath.WalkDir(clean, func(path string, entry fs.DirEntry,
		walkErr error) error {

		if walkErr != nil {
			return walkErr
		}
		if shouldSkipDir(path, entry) {
			return filepath.SkipDir
		}
		if entry.IsDir() || filepath.Ext(path) != ".go" {
			return nil
		}
		files = append(files, path)

		return nil
	})
	if err != nil {
		return "", nil, err
	}
	sort.Strings(files)

	return clean, files, nil
}

// defaultPath returns the caller path or the current directory.
func defaultPath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return "."
	}

	return path
}

// shouldSkipDir reports whether path should be omitted from recursive parsing.
func shouldSkipDir(path string, entry fs.DirEntry) bool {
	if !entry.IsDir() {
		return false
	}
	name := entry.Name()
	if path == "." || name == "." {
		return false
	}
	if strings.HasPrefix(name, ".") {
		return true
	}
	switch name {
	case "bin", "node_modules", "vendor":
		return true

	default:
		return false
	}
}

// prefixPackages keeps multi-root output unambiguous by preserving root labels.
func prefixPackages(packages []packageInfo, root string) {
	prefix := searchRootLabel(root)
	if prefix == "" || prefix == "." {
		return
	}
	for pkgIndex := range packages {
		packages[pkgIndex].relDir = joinSymbolPath(
			prefix, packages[pkgIndex].relDir,
		)
		for fileIndex := range packages[pkgIndex].files {
			packages[pkgIndex].files[fileIndex] = joinSymbolPath(
				prefix, packages[pkgIndex].files[fileIndex],
			)
		}
		for symbolIndex := range packages[pkgIndex].symbols {
			symbol := &packages[pkgIndex].symbols[symbolIndex]
			symbol.relDir = joinSymbolPath(prefix, symbol.relDir)
			symbol.relFile = joinSymbolPath(prefix, symbol.relFile)
			if !insideCurrentDirectory(symbol.file) {
				symbol.displayFile = symbol.relFile
			}
		}
	}
}

// searchRootLabel returns a compact slash-separated label for one search root.
func searchRootLabel(root string) string {
	clean := filepath.Clean(root)
	info, err := os.Stat(clean)
	if err == nil && !info.IsDir() {
		return filepath.ToSlash(clean)
	}
	if rel, err := filepath.Rel(".", clean); err == nil &&
		!strings.HasPrefix(rel, "..") && rel != "." {
		return filepath.ToSlash(rel)
	}

	return filepath.ToSlash(clean)
}

// joinSymbolPath joins root and relative path labels while suppressing ".".
func joinSymbolPath(root string, rel string) string {
	rel = filepath.ToSlash(strings.TrimSpace(rel))
	if rel == "" || rel == "." {
		return root
	}

	return filepath.ToSlash(filepath.Join(root, rel))
}

// displayPath returns the path label shown to callers for one source file.
func displayPath(root string, path string) string {
	if insideCurrentDirectory(path) {
		return currentDirectoryPath(path)
	}

	return relativePath(root, path)
}

// insideCurrentDirectory reports whether path can be described from cwd.
func insideCurrentDirectory(path string) bool {
	label := currentDirectoryPath(path)

	return label != "" && label != "."
}

// currentDirectoryPath returns a slash-separated cwd-relative path label.
func currentDirectoryPath(path string) string {
	cwd, err := os.Getwd()
	if err != nil {
		return ""
	}
	abs, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return ""
	}
	rel, err := filepath.Rel(cwd, abs)
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
		return ""
	}

	return filepath.ToSlash(rel)
}

// fileSymbols extracts top-level symbols from one parsed Go file.
func fileSymbols(fset *token.FileSet, root string, file string,
	parsed *ast.File) []symbolInfo {

	// #nosec G304 -- Plugin callers intentionally choose local Go files to
	// inspect, matching etch' explicit local tooling model.
	content, err := os.ReadFile(file)
	if err != nil {
		return []symbolInfo{{
			name:        filepath.Base(file),
			kind:        "error",
			packageName: parsed.Name.Name,
			file:        file,
			relFile:     relativePath(root, file),
			displayFile: displayPath(root, file),
			doc:         err.Error(),
		}}
	}

	var symbols []symbolInfo
	for _, decl := range parsed.Decls {
		symbols = append(
			symbols, declarationSymbols(
				fset, root, file, parsed.Name.Name, content,
				decl,
			)...,
		)
	}

	return symbols
}

// declarationSymbols extracts symbol records from one top-level declaration.
func declarationSymbols(fset *token.FileSet, root string, file string,
	packageName string, content []byte, decl ast.Decl) []symbolInfo {

	switch typed := decl.(type) {
	case *ast.FuncDecl:
		return []symbolInfo{funcSymbol(
			fset, root, file, packageName, content, typed,
		)}

	case *ast.GenDecl:
		return genDeclSymbols(
			fset, root, file, packageName, content, typed,
		)

	default:
		return nil
	}
}

// funcSymbol returns the symbol represented by a function or method.
func funcSymbol(fset *token.FileSet, root string, file string,
	packageName string, content []byte, decl *ast.FuncDecl) symbolInfo {

	kind := "func"
	name := decl.Name.Name
	if decl.Recv != nil && len(decl.Recv.List) > 0 {
		kind = "method"
		name = receiverName(decl.Recv.List[0].Type) + "." + name
	}

	return newSymbolInfo(
		fset, root, file, packageName, name, kind, content, decl,
		decl.Doc, funcSignature(fset, decl),
	)
}

// genDeclSymbols returns symbols represented by type, const, or var specs.
func genDeclSymbols(fset *token.FileSet, root string, file string,
	packageName string, content []byte, decl *ast.GenDecl) []symbolInfo {

	var symbols []symbolInfo
	for _, spec := range decl.Specs {
		switch typed := spec.(type) {
		case *ast.TypeSpec:
			symbols = append(
				symbols, typeSymbol(
					fset, root, file, packageName, content,
					decl, typed,
				),
			)

		case *ast.ValueSpec:
			kind := strings.ToLower(decl.Tok.String())
			for _, name := range typed.Names {
				symbols = append(
					symbols, valueSymbol(
						fset, root, file, packageName,
						name.Name, kind, content, decl,
						typed,
					),
				)
			}
		}
	}

	return symbols
}

// typeSymbol returns the symbol represented by a type specification.
func typeSymbol(fset *token.FileSet, root string, file string,
	packageName string, content []byte, decl *ast.GenDecl,
	spec *ast.TypeSpec) symbolInfo {

	kind := "type"
	switch spec.Type.(type) {
	case *ast.StructType:
		kind = "struct"

	case *ast.InterfaceType:
		kind = "interface"
	}
	doc := specDoc(spec.Doc, decl.Doc)
	summary := typeSummary(fset, spec)

	return newSymbolInfo(
		fset, root, file, packageName, spec.Name.Name, kind, content,
		decl, doc, summary,
	)
}

// valueSymbol returns the symbol represented by one const or var name.
func valueSymbol(fset *token.FileSet, root string, file string,
	packageName string, name string, kind string, content []byte,
	decl *ast.GenDecl, spec *ast.ValueSpec) symbolInfo {

	doc := specDoc(spec.Doc, decl.Doc)
	summary := valueSummary(fset, decl.Tok.String(), spec)
	full := valueFullDeclaration(
		fset, content, decl.Tok.String(), spec, doc,
	)

	symbol := newSymbolInfo(
		fset, root, file, packageName, name, kind, content, spec, doc,
		summary,
	)
	symbol.fullDeclaration = full

	return symbol
}

// specDoc returns a spec-specific doc comment or the surrounding declaration
// doc.
func specDoc(specDoc *ast.CommentGroup,
	declDoc *ast.CommentGroup) *ast.CommentGroup {

	if specDoc != nil {
		return specDoc
	}

	return declDoc
}

// newSymbolInfo builds a symbol record from a source node and optional docs.
func newSymbolInfo(fset *token.FileSet, root string, file string,
	packageName string, name string, kind string, content []byte,
	node ast.Node, doc *ast.CommentGroup, summary string) symbolInfo {

	position := fset.Position(node.Pos())
	sourceLine := position.Line
	if doc != nil {
		sourceLine = fset.Position(doc.Pos()).Line
	}
	endLine := fset.Position(node.End()).Line

	return symbolInfo{
		name:               name,
		kind:               kind,
		packageName:        packageName,
		dir:                filepath.Dir(file),
		relDir:             relativePath(root, filepath.Dir(file)),
		file:               file,
		relFile:            relativePath(root, file),
		displayFile:        displayPath(root, file),
		line:               position.Line,
		endLine:            endLine,
		sourceLine:         sourceLine,
		doc:                docText(doc),
		fullDeclaration:    sourceForNode(fset, content, node, doc),
		summaryDeclaration: summary,
	}
}

// funcSignature renders a function declaration without its body or doc block.
func funcSignature(fset *token.FileSet, decl *ast.FuncDecl) string {
	clone := *decl
	clone.Doc = nil
	clone.Body = nil
	var out bytes.Buffer
	config := printer.Config{Mode: printer.RawFormat, Tabwidth: 8}
	if err := config.Fprint(&out, fset, &clone); err != nil {
		return ""
	}

	return compactRenderedGo(out.String())
}

// typeSummary renders a compact declaration for one type symbol.
func typeSummary(fset *token.FileSet, spec *ast.TypeSpec) string {
	switch typed := spec.Type.(type) {
	case *ast.StructType:
		return fmt.Sprintf("type %s struct { ... }", spec.Name.Name)

	case *ast.InterfaceType:
		clone := *spec
		clone.Doc = nil
		clone.Type = typed

		return "type " + compactRenderedNode(fset, &clone)

	default:
		return "type " + compactRenderedNode(fset, spec)
	}
}

// valueSummary renders a compact declaration for one const or var spec.
func valueSummary(fset *token.FileSet, tokenName string,
	spec *ast.ValueSpec) string {

	return tokenName + " " + compactRenderedNode(fset, spec)
}

// valueFullDeclaration renders a const or var spec with its declaration token.
func valueFullDeclaration(fset *token.FileSet, content []byte, tokenName string,
	spec *ast.ValueSpec, doc *ast.CommentGroup) string {

	source := sourceForNode(fset, content, spec, doc)
	if source == "" {
		return ""
	}
	lines := strings.Split(source, "\n")
	for index := len(lines) - 1; index >= 0; index-- {
		if strings.TrimSpace(lines[index]) == "" {
			continue
		}
		trimmed := strings.TrimLeft(lines[index], " 	")
		if !strings.HasPrefix(trimmed, tokenName+" ") &&
			trimmed != tokenName {

			trimmed = tokenName + " " + trimmed
		}
		lines[index] = trimmed
		break
	}

	return strings.Join(lines, "\n")
}

// compactRenderedNode renders node and folds whitespace for summary output.
func compactRenderedNode(fset *token.FileSet, node any) string {
	var out bytes.Buffer
	config := printer.Config{Mode: printer.RawFormat, Tabwidth: 8}
	if err := config.Fprint(&out, fset, node); err != nil {
		return ""
	}

	return compactRenderedGo(out.String())
}

// compactRenderedGo folds rendered Go source onto one logical line.
func compactRenderedGo(source string) string {
	return strings.Join(strings.Fields(source), " ")
}

// docText returns normalized documentation text for a declaration.
func docText(doc *ast.CommentGroup) string {
	if doc == nil {
		return ""
	}

	return strings.TrimSpace(doc.Text())
}

// sourceForNode returns source text for node, including doc when available.
func sourceForNode(fset *token.FileSet, content []byte, node ast.Node,
	doc *ast.CommentGroup) string {

	start := fset.Position(node.Pos()).Offset
	if doc != nil {
		start = fset.Position(doc.Pos()).Offset
	}
	end := fset.Position(node.End()).Offset
	if start < 0 || end > len(content) || start >= end {
		return ""
	}

	return strings.TrimSpace(string(content[start:end]))
}

// receiverName returns a printable receiver base type name.
func receiverName(expr ast.Expr) string {
	switch typed := expr.(type) {
	case *ast.Ident:
		return typed.Name

	case *ast.StarExpr:
		return receiverName(typed.X)

	case *ast.IndexExpr:
		return receiverName(typed.X)

	case *ast.IndexListExpr:
		return receiverName(typed.X)

	case *ast.SelectorExpr:
		return receiverName(typed.X) + "." + typed.Sel.Name

	default:
		return "receiver"
	}
}

// allSymbols returns every symbol from packages in deterministic order.
func allSymbols(packages []packageInfo) []symbolInfo {
	var symbols []symbolInfo
	for _, pkg := range packages {
		symbols = append(symbols, pkg.symbols...)
	}
	sortSymbols(symbols)

	return symbols
}

// filterSymbols returns symbols that match regex and export filters.
func filterSymbols(symbols []symbolInfo, filters symbolFilters) []symbolInfo {
	var filtered []symbolInfo
	for _, symbol := range symbols {
		if !filters.includeAll && !isExportedSymbol(symbol.name) {
			continue
		}
		if !filters.packageRegex.matches(symbol.packageName) &&
			!filters.packageRegex.matches(symbol.relDir) {

			continue
		}
		if !matchesSymbolFile(filters.fileRegex, symbol) {
			continue
		}
		if !filters.nameRegex.matches(symbol.name) &&
			!filters.nameRegex.matches(
				simpleSymbolName(symbol.name),
			) {

			continue
		}
		filtered = append(filtered, symbol)
	}

	return filtered
}

// matchesSymbolFile reports whether a file regex matches any path label a
// caller is likely to use for a symbol.
func matchesSymbolFile(filter symbolRegex, symbol symbolInfo) bool {
	if !filter.active() {
		return true
	}
	for _, candidate := range []string{
		symbol.displayFile,
		symbol.relFile,
		filepath.ToSlash(symbol.file),
	} {
		if filter.matches(candidate) {
			return true
		}
	}

	return false
}

// isExportedSymbol reports whether name begins with an exported identifier.
func isExportedSymbol(name string) bool {
	base := name
	if before, _, ok := strings.Cut(name, "."); ok {
		base = before
	}
	for _, r := range base {
		return unicode.IsUpper(r)
	}

	return false
}

// simpleSymbolName returns the member portion of a qualified method name.
func simpleSymbolName(name string) string {
	_, after, ok := strings.Cut(name, ".")
	if !ok {
		return name
	}

	return after
}

// formatSymbols renders filtered symbols in the requested detail mode.
func formatSymbols(paths []string, packages []packageInfo, all []symbolInfo,
	symbols []symbolInfo, filters symbolFilters, detail string,
	limit int) string {

	var builder strings.Builder
	fmt.Fprintf(&builder, "paths: %s\n", strings.Join(paths, ", "))
	fmt.Fprintf(&builder, "filters: %s\n", formatFilters(filters))
	fmt.Fprintf(&builder, "packages: %d\n", len(packages))
	fmt.Fprintf(&builder, "symbols: %d", len(symbols))
	if len(symbols) > limit {
		fmt.Fprintf(&builder, " (showing %d)", limit)
	}
	fmt.Fprintln(&builder)
	fmt.Fprintf(&builder, "detail: %s\n", detail)

	if len(symbols) == 0 {
		fmt.Fprintln(&builder)
		fmt.Fprintln(&builder, "No Go symbols matched.")
		fmt.Fprintf(
			&builder, "Parsed symbols before filters: %d.\n",
			len(all),
		)
		writeNoMatchHints(&builder, all)

		return strings.TrimRight(builder.String(), "\n")
	}

	rendered := symbols
	if len(rendered) > limit {
		rendered = rendered[:limit]
	}
	if detail == detailPackage {
		fmt.Fprintln(&builder)
		writePackageOverview(&builder, packages, rendered)

		return strings.TrimRight(builder.String(), "\n")
	}
	if detail == detailNone {
		fmt.Fprintln(&builder)
		writeSymbolRows(&builder, rendered)

		return strings.TrimRight(builder.String(), "\n")
	}
	for _, symbol := range rendered {
		fmt.Fprintf(
			&builder, "\n---\n%s\n",
			formatSymbolDetail(symbol, detail),
		)
	}

	return strings.TrimRight(builder.String(), "\n")
}

// writePackageOverview renders packages, files, and compact matching symbols.
func writePackageOverview(builder *strings.Builder, packages []packageInfo,
	symbols []symbolInfo) {

	byPackage := symbolsByPackage(symbols)
	for _, pkg := range packages {
		key := packageKey(pkg)
		matched := byPackage[key]
		if len(matched) == 0 {
			continue
		}
		fmt.Fprintf(
			builder, "package %s (%s): %d files, %d symbols\n",
			pkg.name, pkg.relDir, len(pkg.files), len(matched),
		)
		fmt.Fprintln(builder, "files:")
		for _, file := range pkg.files {
			fmt.Fprintf(builder, "- %s\n", file)
		}
		fmt.Fprintln(builder, "symbols:")
		writeSymbolRows(builder, matched)
		fmt.Fprintln(builder)
	}
}

// symbolsByPackage groups symbols by package identity.
func symbolsByPackage(symbols []symbolInfo) map[string][]symbolInfo {
	grouped := make(map[string][]symbolInfo)
	for _, symbol := range symbols {
		key := symbol.packageName + "\x00" + symbol.relDir
		grouped[key] = append(grouped[key], symbol)
	}

	return grouped
}

// packageKey returns the grouping key shared with symbolsByPackage.
func packageKey(pkg packageInfo) string {
	return pkg.name + "\x00" + pkg.relDir
}

// formatFilters renders active filters for the output header.
func formatFilters(filters symbolFilters) string {
	var parts []string
	for _, filter := range []symbolRegex{
		filters.packageRegex,
		filters.fileRegex,
		filters.nameRegex,
	} {
		if filter.active() {
			parts = append(
				parts, fmt.Sprintf("%s=/%s/i", filter.label,
					filter.raw),
			)
		}
	}
	if len(parts) == 0 {
		return "none"
	}

	return strings.Join(parts, " ")
}

// writeNoMatchHints renders compact diagnostics for a zero-result query.
func writeNoMatchHints(builder *strings.Builder, symbols []symbolInfo) {
	files := sampleSymbolFiles(symbols, 8)
	if len(files) > 0 {
		fmt.Fprintln(builder, "Sample searchable files:")
		for _, file := range files {
			fmt.Fprintf(builder, "- %s\n", file)
		}
	}
	names := sampleSymbolNames(symbols, 8)
	if len(names) > 0 {
		fmt.Fprintln(builder, "Sample searchable symbols:")
		for _, name := range names {
			fmt.Fprintf(builder, "- %s\n", name)
		}
	}
	fmt.Fprintln(
		builder, "Hint: file regexes match displayed repo-relative "+
			"paths, root-relative paths, and raw paths. Use "+
			"detail=none with a broad file/package filter to "+
			"map available symbols before asking for summary "+
			"or full detail.",
	)
}

// sampleSymbolFiles returns sorted unique file labels for diagnostics.
func sampleSymbolFiles(symbols []symbolInfo, limit int) []string {
	seen := make(map[string]bool)
	var files []string
	for _, symbol := range symbols {
		if symbol.displayFile == "" || seen[symbol.displayFile] {
			continue
		}
		seen[symbol.displayFile] = true
		files = append(files, symbol.displayFile)
	}
	sort.Strings(files)
	if len(files) > limit {
		files = files[:limit]
	}

	return files
}

// sampleSymbolNames returns sorted unique symbol names for diagnostics.
func sampleSymbolNames(symbols []symbolInfo, limit int) []string {
	seen := make(map[string]bool)
	var names []string
	for _, symbol := range symbols {
		if symbol.name == "" || seen[symbol.name] {
			continue
		}
		seen[symbol.name] = true
		names = append(names, symbol.name)
	}
	sort.Strings(names)
	if len(names) > limit {
		names = names[:limit]
	}

	return names
}

// writeSymbolRows renders a compact row for each symbol.
func writeSymbolRows(builder *strings.Builder, symbols []symbolInfo) {
	for _, symbol := range symbols {
		fmt.Fprintf(
			builder, "- %s %s %s:%d", symbol.kind, symbol.name,
			symbol.displayFile, symbol.line,
		)
		if symbol.endLine > symbol.line {
			fmt.Fprintf(builder, "-%d", symbol.endLine)
		}
		fmt.Fprintln(builder)
	}
}

// formatSymbolDetail renders one symbol in summary or full detail.
func formatSymbolDetail(symbol symbolInfo, detail string) string {
	var builder strings.Builder
	fmt.Fprintf(&builder, "symbol: %s\n", symbol.name)
	fmt.Fprintf(&builder, "kind: %s\n", symbol.kind)
	fmt.Fprintf(
		&builder, "package: %s (%s)\n", symbol.packageName,
		symbol.relDir,
	)
	fmt.Fprintf(&builder, "file: %s:%d", symbol.displayFile, symbol.line)
	if symbol.endLine > symbol.line {
		fmt.Fprintf(&builder, "-%d", symbol.endLine)
	}
	fmt.Fprintln(&builder)
	switch detail {
	case detailFull:
		writeDeclaration(&builder, numberedDeclaration(symbol))

	default:
		if symbol.doc != "" {
			fmt.Fprintf(&builder, "\ngodoc:\n%s\n", symbol.doc)
		}
		writeDeclaration(&builder, symbol.summaryDeclaration)
	}

	return strings.TrimRight(builder.String(), "\n")
}

// writeDeclaration writes a fenced declaration block when source is available.
func writeDeclaration(builder *strings.Builder, declaration string) {
	if declaration == "" {
		return
	}
	fmt.Fprintf(
		builder, "\ndeclaration:\n```go\n%s\n```",
		declaration,
	)
}

// numberedDeclaration renders a symbol declaration with source line prefixes.
func numberedDeclaration(symbol symbolInfo) string {
	lines := strings.Split(symbol.fullDeclaration, "\n")
	width := len(fmt.Sprintf("%d", symbol.sourceLine+len(lines)-1))
	for i, line := range lines {
		lines[i] = fmt.Sprintf("%*d | %s", width, symbol.sourceLine+i,
			line)
	}

	return strings.Join(lines, "\n")
}

// sortSymbols orders symbols by file, line, kind, and name.
func sortSymbols(symbols []symbolInfo) {
	sort.Slice(symbols, func(i, j int) bool {
		if symbols[i].displayFile != symbols[j].displayFile {
			return symbols[i].displayFile < symbols[j].displayFile
		}
		if symbols[i].line != symbols[j].line {
			return symbols[i].line < symbols[j].line
		}
		if symbols[i].kind != symbols[j].kind {
			return symbols[i].kind < symbols[j].kind
		}

		return symbols[i].name < symbols[j].name
	})
}

// relativePath returns a stable slash-separated path for output.
func relativePath(root string, path string) string {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return filepath.ToSlash(path)
	}
	if rel == "." {
		return "."
	}

	return filepath.ToSlash(rel)
}
