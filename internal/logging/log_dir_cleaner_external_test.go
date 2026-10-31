package logging

import "testing"

func TestIsLogFileNameSkipsExternalOpsLogs(t *testing.T) {
	for _, n := range []string{"watchdog.log", "tunnel.log", "resume-events.log", "boot.log"} {
		if isLogFileName(n) {
			t.Fatalf("%s must not be cleaned", n)
		}
	}
	if !isLogFileName("main-2026-10-31T06-34-24.818.log") {
		t.Fatal("rotated main log should still be cleaned")
	}
}
