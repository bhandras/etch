package fs

// skipDir reports whether a directory should be hidden from builtin tools.
func skipDir(name string) bool {
	switch name {
	case ".git", ".harness", "bin", "node_modules", "vendor":
		return true

	default:
		return false
	}
}
