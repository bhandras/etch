package textutil

import "testing"

// TestFormatCountRendersCommaGroups verifies large counters are easy to scan
// in terminal summaries.
func TestFormatCountRendersCommaGroups(t *testing.T) {
	if FormatCount(123) != "123" {
		t.Fatalf("unexpected small count: %q", FormatCount(123))
	}
	if FormatCount(1234567) != "1,234,567" {
		t.Fatalf("unexpected large count: %q", FormatCount(1234567))
	}
	if FormatCount(-1234567) != "-1,234,567" {
		t.Fatalf("unexpected negative count: %q", FormatCount(-1234567))
	}
}

// TestTruncateUTF8BytesPreservesRuneBoundaries verifies byte caps do not
// create invalid UTF-8 strings.
func TestTruncateUTF8BytesPreservesRuneBoundaries(t *testing.T) {
	got, truncated := TruncateUTF8Bytes("aé日b", 4)
	if !truncated {
		t.Fatal("expected truncation")
	}
	if got != "aé" {
		t.Fatalf("unexpected truncation: %q", got)
	}
}

// TestFormatBytesRendersCompactUnits verifies common byte displays.
func TestFormatBytesRendersCompactUnits(t *testing.T) {
	if FormatBytes(42) != "42B" {
		t.Fatalf("unexpected byte display: %q", FormatBytes(42))
	}
	if FormatBytes(1536) != "1.5KB" {
		t.Fatalf("unexpected kilobyte display: %q", FormatBytes(1536))
	}
}
