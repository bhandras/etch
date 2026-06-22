package textutil

import "fmt"

// FormatCount renders integer counters with comma group separators.
func FormatCount(count int) string {
	sign := ""
	if count < 0 {
		sign = "-"
		count = -count
	}
	digits := fmt.Sprintf("%d", count)
	if len(digits) <= 3 {
		return sign + digits
	}
	first := len(digits) % 3
	if first == 0 {
		first = 3
	}
	out := sign + digits[:first]
	for index := first; index < len(digits); index += 3 {
		out += "," + digits[index:index+3]
	}

	return out
}

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
