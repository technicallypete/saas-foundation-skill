package fixture

import (
	"fmt"
	"os"
	"sync"
	"testing"
)

// Evidence labels distinguish real-infrastructure evidence from unit tests.
const (
	RealPG       = "Real/PG"
	RealS3       = "Real/S3"
	RealSDKInMem = "Real/SDK-inmem"
	RealSDKHTTP  = "Real/SDK-http"
	Unit         = "Unit"
	ControlLabel = "Control"
	labelFileVar = "ARCHETYPE_LABEL_FILE"
)

var validLabels = map[string]bool{RealPG: true, RealS3: true, RealSDKInMem: true, RealSDKHTTP: true, Unit: true, ControlLabel: true}

var labelMu sync.Mutex

// Label records the evidence label of the running test. The harness reads
// ARCHETYPE_LABEL_FILE to tabulate tests by connection and label.
func Label(t testing.TB, label string) {
	t.Helper()
	if !validLabels[label] {
		t.Fatalf("unknown evidence label %q", label)
	}
	t.Logf("evidence-label: %s", label)
	path := os.Getenv(labelFileVar)
	if path == "" {
		return
	}
	labelMu.Lock()
	defer labelMu.Unlock()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("label file: %v", err)
	}
	defer f.Close()
	fmt.Fprintf(f, "%s\t%s\n", t.Name(), label)
}
