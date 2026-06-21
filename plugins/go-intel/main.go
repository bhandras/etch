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
	"sort"
	"strings"
	"unicode"

	"harness/sdk"
)

const (
	// toolGoListSymbols lists Go symbols under a path with optional
	// grouping.
	toolGoListSymbols = "go_list_symbols"

	// toolGoPackageSymbols lists symbols in one package discovered under a
	// path.
	toolGoPackageSymbols = "go_package_symbols"

	// toolGoFileSymbols lists symbols declared by one Go source file.
	toolGoFileSymbols = "go_file_symbols"

	// toolGoSymbol returns the doc comment and source for one Go symbol.
	toolGoSymbol = "go_symbol"

	// defaultSymbolLimit bounds list output when no caller limit is
	// supplied.
	defaultSymbolLimit = 200

	// maxSymbolLimit prevents accidental giant symbol listings.
	maxSymbolLimit = 2000

	// declarationFull renders a symbol's full source declaration.
	declarationFull = "full"

	// declarationSignature renders only metadata, godoc, and signatures.
	declarationSignature = "signature"

	// declarationNone renders metadata and godoc without source
	// declarations.
	declarationNone = "none"
)

// listSymbolsArgs stores arguments shared by symbol-listing tools.
type listSymbolsArgs struct {
	// Path is a Go file or directory to inspect. Empty means current
	// directory.
	Path string `json:"path"`

	// GroupBy controls list grouping: package, file, or flat.
	GroupBy string `json:"groupBy"`

	// Kind filters symbols by kind, such as func, method, struct, or const.
	Kind string `json:"kind"`

	// IncludeUnexported includes lowercase package symbols when true.
	IncludeUnexported bool `json:"includeUnexported"`

	// Limit caps the number of symbols rendered.
	Limit int `json:"limit"`
}

// packageSymbolsArgs stores arguments for package-scoped symbol listings.
type packageSymbolsArgs struct {
	// Path is a Go directory tree to inspect. Empty means current
	// directory.
	Path string `json:"path"`

	// Package selects a package by name or relative directory.
	Package string `json:"package"`

	// Kind filters symbols by kind, such as func, method, struct, or const.
	Kind string `json:"kind"`

	// IncludeUnexported includes lowercase package symbols when true.
	IncludeUnexported bool `json:"includeUnexported"`

	// Limit caps the number of symbols rendered.
	Limit int `json:"limit"`
}

// fileSymbolsArgs stores arguments for file-scoped symbol listings.
type fileSymbolsArgs struct {
	// Path is the Go source file to inspect.
	Path string `json:"path"`

	// Kind filters symbols by kind, such as func, method, struct, or const.
	Kind string `json:"kind"`

	// IncludeUnexported includes lowercase package symbols when true.
	IncludeUnexported bool `json:"includeUnexported"`

	// Limit caps the number of symbols rendered.
	Limit int `json:"limit"`
}

// symbolArgs stores arguments for a specific Go symbol lookup.
type symbolArgs struct {
	// Path is a Go file or directory to inspect. Empty means current
	// directory.
	Path string `json:"path"`

	// Name is the symbol name to find, such as Config or Store.Append.
	Name string `json:"name"`

	// Package optionally narrows the search by package name or relative
	// dir.
	Package string `json:"package"`

	// File optionally narrows the search by file path or relative file
	// path.
	File string `json:"file"`

	// IncludeUnexported permits matching lowercase package symbols when
	// true.
	IncludeUnexported bool `json:"includeUnexported"`

	// Declaration controls how much source is returned: full, signature, or
	// none. Empty defaults to full.
	Declaration string `json:"declaration"`
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
	name        string
	kind        string
	packageName string
	dir         string
	relDir      string
	file        string
	relFile     string
	line        int
	doc         string
	signature   string
	declaration string
}

// symbolFilter stores normalized filters applied to discovered symbols.
type symbolFilter struct {
	kind              string
	includeUnexported bool
}

// main serves the Go intelligence plugin protocol until stdin closes.
func main() {
	if err := sdk.ServePlugin(sdk.Plugin{
		Name: "go-intel",
		Tools: []sdk.Tool{
			goListSymbolsSpec(),
			goPackageSymbolsSpec(),
			goFileSymbolsSpec(),
			goSymbolSpec(),
		},
	}); err != nil {

		fmt.Fprintln(os.Stderr, err)
	}
}

// goListSymbolsSpec returns the schema for recursive Go symbol listings.
func goListSymbolsSpec() sdk.Tool {
	return sdk.Tool{
		Name: toolGoListSymbols,
		Description: "List top-level Go symbols under a file or " +
			"directory using the Go standard library parser.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": stringSchema(
					"Go file or directory to inspect. " +
						"Defaults to .",
				),
				"groupBy": stringSchema(
					"Grouping: package, file, or flat. " +
						"Defaults to package.",
				),
				"kind": stringSchema(
					"Optional kind filter: func, " +
						"method, struct, " +
						"interface, type, const, " +
						"or var.",
				),
				"includeUnexported": boolSchema(
					"Include lowercase package symbols.",
				),
				"limit": integerSchema(
					"Maximum symbols to render. " +
						"Defaults to 200 and caps " +
						"at 2000.",
				),
			},
		},
		Handler: handleGoListSymbols,
	}
}

// goPackageSymbolsSpec returns the schema for package-scoped symbol listings.
func goPackageSymbolsSpec() sdk.Tool {
	return sdk.Tool{
		Name: toolGoPackageSymbols,
		Description: "List Go symbols for one package selected by package " +
			"name or relative directory.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": stringSchema(
					"Directory tree to inspect. " +
						"Defaults to .",
				),
				"package": stringSchema(
					"Package name or relative " +
						"directory to select.",
				),
				"kind": stringSchema(
					"Optional kind filter: func, " +
						"method, struct, " +
						"interface, type, const, " +
						"or var.",
				),
				"includeUnexported": boolSchema(
					"Include lowercase package symbols.",
				),
				"limit": integerSchema(
					"Maximum symbols to render. " +
						"Defaults to 200 and caps " +
						"at 2000.",
				),
			},
			"required": []string{
				"package",
			},
		},
		Handler: handleGoPackageSymbols,
	}
}

// goFileSymbolsSpec returns the schema for file-scoped symbol listings.
func goFileSymbolsSpec() sdk.Tool {
	return sdk.Tool{
		Name:        toolGoFileSymbols,
		Description: "List Go symbols declared by one source file.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": stringSchema(
					"Go source file to inspect.",
				),
				"kind": stringSchema(
					"Optional kind filter: func, " +
						"method, struct, " +
						"interface, type, const, " +
						"or var.",
				),
				"includeUnexported": boolSchema(
					"Include lowercase package symbols.",
				),
				"limit": integerSchema(
					"Maximum symbols to render. " +
						"Defaults to 200 and caps " +
						"at 2000.",
				),
			},
			"required": []string{
				"path",
			},
		},
		Handler: handleGoFileSymbols,
	}
}

// goSymbolSpec returns the schema for specific symbol source lookup.
func goSymbolSpec() sdk.Tool {
	return sdk.Tool{
		Name: toolGoSymbol,
		Description: "Return structured godoc, function signatures, " +
			"and optional source declaration for one Go symbol.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": stringSchema(
					"Go file or directory to inspect. " +
						"Defaults to .",
				),
				"name": stringSchema(
					"Symbol name, such as Config or " +
						"Store.Append.",
				),
				"package": stringSchema(
					"Optional package name or relative " +
						"directory filter.",
				),
				"file": stringSchema(
					"Optional file path or relative " +
						"file path filter.",
				),
				"includeUnexported": boolSchema(
					"Permit lowercase package symbols.",
				),
				"declaration": stringSchema(
					"Declaration detail: full, " +
						"signature, or none. " +
						"Defaults to full.",
				),
			},
			"required": []string{
				"name",
			},
		},
		Handler: handleGoSymbol,
	}
}

// stringSchema returns a JSON schema property for a string argument.
func stringSchema(description string) map[string]any {
	return map[string]any{"type": "string", "description": description}
}

// boolSchema returns a JSON schema property for a boolean argument.
func boolSchema(description string) map[string]any {
	return map[string]any{"type": "boolean", "description": description}
}

// integerSchema returns a JSON schema property for an integer argument.
func integerSchema(description string) map[string]any {
	return map[string]any{"type": "integer", "description": description}
}

// handleGoListSymbols executes go_list_symbols through the SDK handler.
func handleGoListSymbols(ctx context.Context,
	call sdk.ToolCall) (sdk.ToolResult, error) {

	if err := ctx.Err(); err != nil {
		return sdk.ToolResult{}, err
	}
	text, err := runGoListSymbols(call.Arguments)
	if err != nil {
		return sdk.ToolResult{}, err
	}

	return sdk.TextResult(text), nil
}

// handleGoPackageSymbols executes go_package_symbols through the SDK handler.
func handleGoPackageSymbols(ctx context.Context,
	call sdk.ToolCall) (sdk.ToolResult, error) {

	if err := ctx.Err(); err != nil {
		return sdk.ToolResult{}, err
	}
	text, err := runGoPackageSymbols(call.Arguments)
	if err != nil {
		return sdk.ToolResult{}, err
	}

	return sdk.TextResult(text), nil
}

// handleGoFileSymbols executes go_file_symbols through the SDK handler.
func handleGoFileSymbols(ctx context.Context,
	call sdk.ToolCall) (sdk.ToolResult, error) {

	if err := ctx.Err(); err != nil {
		return sdk.ToolResult{}, err
	}
	text, err := runGoFileSymbols(call.Arguments)
	if err != nil {
		return sdk.ToolResult{}, err
	}

	return sdk.TextResult(text), nil
}

// handleGoSymbol executes go_symbol through the SDK handler.
func handleGoSymbol(ctx context.Context,
	call sdk.ToolCall) (sdk.ToolResult, error) {

	if err := ctx.Err(); err != nil {
		return sdk.ToolResult{}, err
	}
	text, err := runGoSymbol(call.Arguments)
	if err != nil {
		return sdk.ToolResult{}, err
	}

	return sdk.TextResult(text), nil
}

// runGoListSymbols lists Go symbols under a caller-provided path.
func runGoListSymbols(raw json.RawMessage) (string, error) {
	var args listSymbolsArgs
	if err := decodeArguments(raw, &args); err != nil {
		return "", err
	}
	path := defaultPath(args.Path)
	packages, err := parsePackages(path)
	if err != nil {
		return "", err
	}
	filter := symbolFilter{
		kind:              strings.TrimSpace(args.Kind),
		includeUnexported: args.IncludeUnexported,
	}
	symbols := filterSymbols(allSymbols(packages), filter)
	limit := normalizeSymbolLimit(args.Limit)
	groupBy := strings.TrimSpace(args.GroupBy)
	if groupBy == "" {
		groupBy = "package"
	}

	return formatSymbolList(path, packages, symbols, groupBy, limit), nil
}

// runGoPackageSymbols lists symbols for one discovered package.
func runGoPackageSymbols(raw json.RawMessage) (string, error) {
	var args packageSymbolsArgs
	if err := decodeArguments(raw, &args); err != nil {
		return "", err
	}
	if strings.TrimSpace(args.Package) == "" {
		return "", fmt.Errorf("package must not be empty")
	}
	path := defaultPath(args.Path)
	packages, err := parsePackages(path)
	if err != nil {
		return "", err
	}
	selected := selectPackageSymbols(packages, args.Package)
	filter := symbolFilter{
		kind:              strings.TrimSpace(args.Kind),
		includeUnexported: args.IncludeUnexported,
	}
	symbols := filterSymbols(selected, filter)

	return formatSymbolList(
		path, packages, symbols, "flat",
		normalizeSymbolLimit(args.Limit),
	), nil
}

// runGoFileSymbols lists symbols declared by one Go file.
func runGoFileSymbols(raw json.RawMessage) (string, error) {
	var args fileSymbolsArgs
	if err := decodeArguments(raw, &args); err != nil {
		return "", err
	}
	if strings.TrimSpace(args.Path) == "" {
		return "", fmt.Errorf("path must not be empty")
	}
	packages, err := parsePackages(args.Path)
	if err != nil {
		return "", err
	}
	filter := symbolFilter{
		kind:              strings.TrimSpace(args.Kind),
		includeUnexported: args.IncludeUnexported,
	}
	symbols := filterSymbols(allSymbols(packages), filter)

	return formatSymbolList(
		args.Path, packages, symbols, "flat",
		normalizeSymbolLimit(args.Limit),
	), nil
}

// runGoSymbol returns source and documentation for a specific symbol.
func runGoSymbol(raw json.RawMessage) (string, error) {
	var args symbolArgs
	if err := decodeArguments(raw, &args); err != nil {
		return "", err
	}
	name := strings.TrimSpace(args.Name)
	if name == "" {
		return "", fmt.Errorf("name must not be empty")
	}
	declarationMode, err := normalizeDeclarationMode(args.Declaration)
	if err != nil {
		return "", err
	}
	path := defaultPath(args.Path)
	packages, err := parsePackages(path)
	if err != nil {
		return "", err
	}
	filter := symbolFilter{includeUnexported: args.IncludeUnexported}
	matches := matchSymbols(
		filterSymbols(
			allSymbols(packages), filter,
		),
		args,
	)
	if len(matches) == 0 {
		return "", fmt.Errorf("symbol %q not found", name)
	}
	if len(matches) > 1 {
		return formatAmbiguousSymbol(name, matches), nil
	}

	return formatSymbolDetail(matches[0], declarationMode), nil
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

// defaultPath returns the caller path or the current directory.
func defaultPath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return "."
	}

	return path
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

// normalizeDeclarationMode returns a supported source rendering mode.
func normalizeDeclarationMode(mode string) (string, error) {
	mode = strings.ToLower(strings.TrimSpace(mode))
	if mode == "" {
		return declarationFull, nil
	}
	switch mode {
	case declarationFull, declarationSignature, declarationNone:
		return mode, nil

	default:
		return "", fmt.Errorf("declaration must be %q, %q, or %q",
			declarationFull, declarationSignature, declarationNone)
	}
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

// fileSymbols extracts top-level symbols from one parsed Go file.
func fileSymbols(fset *token.FileSet, root string, file string,
	parsed *ast.File) []symbolInfo {

	// #nosec G304 -- Plugin callers intentionally choose local Go files to
	// inspect, matching Harness' explicit local tooling model.
	content, err := os.ReadFile(file)
	if err != nil {
		return []symbolInfo{{
			name:        filepath.Base(file),
			kind:        "error",
			packageName: parsed.Name.Name,
			file:        file,
			relFile:     relativePath(root, file),
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

// funcSymbol returns the symbol represented by a function or method
// declaration.
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
		decl.Doc,
	).withSignature(funcSignature(fset, decl))
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
					symbols,
					newSymbolInfo(
						fset, root, file, packageName,
						name.Name, kind, content, typed,
						specDoc(typed.Doc, decl.Doc),
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

	return newSymbolInfo(
		fset, root, file, packageName, spec.Name.Name, kind, content,
		decl, specDoc(spec.Doc, decl.Doc),
	)
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
	node ast.Node, doc *ast.CommentGroup) symbolInfo {

	position := fset.Position(node.Pos())

	return symbolInfo{
		name:        name,
		kind:        kind,
		packageName: packageName,
		dir:         filepath.Dir(file),
		relDir:      relativePath(root, filepath.Dir(file)),
		file:        file,
		relFile:     relativePath(root, file),
		line:        position.Line,
		doc:         docText(doc),
		declaration: sourceForNode(fset, content, node, doc),
	}
}

// withSignature stores a function or method signature on symbol.
func (s symbolInfo) withSignature(signature string) symbolInfo {
	s.signature = signature

	return s
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

	return compactSignature(out.String())
}

// compactSignature folds a rendered function signature onto one logical line.
func compactSignature(signature string) string {
	return strings.Join(strings.Fields(signature), " ")
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

// filterSymbols returns symbols that match a kind/export filter.
func filterSymbols(symbols []symbolInfo, filter symbolFilter) []symbolInfo {
	var filtered []symbolInfo
	kind := strings.ToLower(strings.TrimSpace(filter.kind))
	for _, symbol := range symbols {
		if kind != "" && symbol.kind != kind {
			continue
		}
		if !filter.includeUnexported && !isExportedSymbol(symbol.name) {
			continue
		}
		filtered = append(filtered, symbol)
	}

	return filtered
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

// selectPackageSymbols returns symbols from packages matching selector.
func selectPackageSymbols(packages []packageInfo,
	selector string) []symbolInfo {

	selector = filepath.ToSlash(strings.TrimSpace(selector))
	var symbols []symbolInfo
	for _, pkg := range packages {
		if pkg.name == selector || pkg.relDir == selector ||
			filepath.ToSlash(pkg.dir) == selector {

			symbols = append(symbols, pkg.symbols...)
		}
	}
	sortSymbols(symbols)

	return symbols
}

// matchSymbols returns symbols matching a specific lookup request.
func matchSymbols(symbols []symbolInfo, args symbolArgs) []symbolInfo {
	name := strings.TrimSpace(args.Name)
	pkgSelector := strings.TrimSpace(args.Package)
	fileSelector := filepath.ToSlash(strings.TrimSpace(args.File))
	var matches []symbolInfo
	for _, symbol := range symbols {
		if symbol.name != name &&
			simpleSymbolName(symbol.name) != name {

			continue
		}
		if pkgSelector != "" && symbol.packageName != pkgSelector &&
			symbol.relDir != filepath.ToSlash(pkgSelector) {

			continue
		}
		if fileSelector != "" && symbol.relFile != fileSelector &&
			filepath.ToSlash(symbol.file) != fileSelector {

			continue
		}
		matches = append(matches, symbol)
	}

	return matches
}

// simpleSymbolName returns the member portion of a qualified method name.
func simpleSymbolName(name string) string {
	_, after, ok := strings.Cut(name, ".")
	if !ok {
		return name
	}

	return after
}

// formatSymbolList renders symbols in the requested grouping.
func formatSymbolList(root string, packages []packageInfo, symbols []symbolInfo,
	groupBy string, limit int) string {

	var builder strings.Builder
	fmt.Fprintf(&builder, "path: %s\n", root)
	fmt.Fprintf(&builder, "packages: %d\n", len(packages))
	fmt.Fprintf(&builder, "symbols: %d", len(symbols))
	if len(symbols) > limit {
		fmt.Fprintf(&builder, " (showing %d)", limit)
	}
	fmt.Fprintln(&builder)

	rendered := symbols
	if len(rendered) > limit {
		rendered = rendered[:limit]
	}
	switch groupBy {
	case "file":
		writeGroupedSymbols(&builder, rendered, symbolFileGroup)

	case "flat":
		writeSymbolRows(&builder, rendered)

	default:
		writeGroupedSymbols(&builder, rendered, symbolPackageGroup)
	}

	return strings.TrimRight(builder.String(), "\n")
}

// symbolPackageGroup returns the package grouping key for symbol.
func symbolPackageGroup(symbol symbolInfo) string {
	return fmt.Sprintf("package %s (%s)", symbol.packageName, symbol.relDir)
}

// symbolFileGroup returns the file grouping key for symbol.
func symbolFileGroup(symbol symbolInfo) string {
	return symbol.relFile
}

// writeGroupedSymbols renders symbols grouped by keyFunc.
func writeGroupedSymbols(builder *strings.Builder, symbols []symbolInfo,
	keyFunc func(symbolInfo) string) {

	groups := make(map[string][]symbolInfo)
	var keys []string
	for _, symbol := range symbols {
		key := keyFunc(symbol)
		if _, ok := groups[key]; !ok {
			keys = append(keys, key)
		}
		groups[key] = append(groups[key], symbol)
	}
	sort.Strings(keys)
	for _, key := range keys {
		fmt.Fprintf(builder, "\n%s:\n", key)
		writeSymbolRows(builder, groups[key])
	}
}

// writeSymbolRows renders a compact row for each symbol.
func writeSymbolRows(builder *strings.Builder, symbols []symbolInfo) {
	for _, symbol := range symbols {
		fmt.Fprintf(
			builder, "- %s %s %s:%d\n", symbol.kind, symbol.name,
			symbol.relFile, symbol.line,
		)
	}
}

// formatSymbolDetail renders a detailed source view for one symbol.
func formatSymbolDetail(symbol symbolInfo, declarationMode string) string {
	var builder strings.Builder
	fmt.Fprintf(&builder, "symbol: %s\n", symbol.name)
	fmt.Fprintf(&builder, "kind: %s\n", symbol.kind)
	fmt.Fprintf(
		&builder, "package: %s (%s)\n", symbol.packageName,
		symbol.relDir,
	)
	fmt.Fprintf(&builder, "file: %s:%d\n", symbol.relFile, symbol.line)
	if symbol.signature != "" {
		fmt.Fprintf(
			&builder, "\nsignature:\n```go\n%s\n```\n",
			symbol.signature,
		)
	}
	if symbol.doc != "" {
		fmt.Fprintf(&builder, "\ngodoc:\n%s\n", symbol.doc)
	}
	if declarationMode == declarationFull && symbol.declaration != "" {
		fmt.Fprintf(
			&builder, "\ndeclaration:\n```go\n%s\n```",
			symbol.declaration,
		)
	}

	return strings.TrimRight(builder.String(), "\n")
}

// formatAmbiguousSymbol renders candidate symbols for an ambiguous lookup.
func formatAmbiguousSymbol(name string, matches []symbolInfo) string {
	var builder strings.Builder
	fmt.Fprintf(
		&builder, "symbol %q is ambiguous; refine package or file\n",
		name,
	)
	writeSymbolRows(&builder, matches)

	return strings.TrimRight(builder.String(), "\n")
}

// sortSymbols orders symbols by file, line, kind, and name.
func sortSymbols(symbols []symbolInfo) {
	sort.Slice(symbols, func(i, j int) bool {
		if symbols[i].relFile != symbols[j].relFile {
			return symbols[i].relFile < symbols[j].relFile
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

	return filepath.ToSlash(rel)
}
