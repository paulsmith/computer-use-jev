package text

import (
	"testing"
	"time"
)

func TestCoreUtilityBoundaries(t *testing.T) {
	if FormatTokens(-1) != "?" || FormatTokens(5520) != "5.4k" || FormatTokens(1<<63-1) != "8796093022208M" || FormatDuration(42500*time.Millisecond) != "43s" || FormatDuration(1<<63-1) != "2562047h 47m" {
		t.Fatal("format")
	}
	if FormatContext(9113, 262144) != "8.9k / 256k (3%)" || FormatContext(-1, 262144) != "? / 256k" || FormatCost(.00421) != "$0.0042" {
		t.Fatal("display")
	}
}
