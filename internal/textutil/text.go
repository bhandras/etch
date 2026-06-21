package textutil

import "fmt"

// FormatBytes renders byte counts in a compact human-readable form.
func FormatBytes(bytes int) string {
	if bytes < 1024 {
		return fmt.Sprintf("%dB", bytes)
	}
	if bytes < 1024*1024 {
		return fmt.Sprintf("%.1fKB", float64(bytes)/1024)
	}

	return fmt.Sprintf("%.1fMB", float64(bytes)/(1024*1024))
}

// TruncateUTF8Bytes truncates text to at most maxBytes without splitting a
// rune.
func TruncateUTF8Bytes(text string, maxBytes int) (string, bool) {
	if maxBytes < 0 {
		maxBytes = 0
	}
	if len(text) <= maxBytes {
		return text, false
	}

	end := 0
	for index := range text {
		if index > maxBytes {
			break
		}
		end = index
	}
	if end == 0 && len(text) > 0 && maxBytes > 0 {
		for index := range text {
			if index == 0 {
				continue
			}
			if index <= maxBytes {
				end = index
			}
			break
		}
	}

	return text[:end], true
}
