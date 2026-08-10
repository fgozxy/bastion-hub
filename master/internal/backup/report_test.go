package backup

import (
	"strings"
	"testing"
)

func TestFormatBackupReportMixedTargets(t *testing.T) {
	msg := FormatBackupReport("计划备份报告", []UnitResult{
		{
			NodeID:    "n1",
			NodeName:  "绿云日本软银",
			TarOK:     true,
			TargetOK:  map[string]bool{"minio": true},
			TargetIDs: []string{"minio"},
		},
		{
			NodeID:    "n1",
			NodeName:  "绿云日本软银",
			TarOK:     true,
			TargetOK:  map[string]bool{ResticIncrementalTargetID: true},
			TargetIDs: []string{ResticIncrementalTargetID},
		},
	}, []TargetInfo{
		{ID: "minio", Name: "绿云储存MinIO"},
		{ID: ResticIncrementalTargetID, Name: "Restic增量"},
	})

	if strings.Contains(msg, "失败原因") {
		t.Fatalf("mixed successful targets should not produce failure notes: %s", msg)
	}
	if !strings.Contains(msg, "🟢") {
		t.Fatalf("mixed successful targets should render green status: %s", msg)
	}
}

// TestFormatBackupReportPartialTarget asserts the three-valued status: when one
// target fails for one unit, the node reads 🟡 X/Y (not a misleading full 🔴),
// and the failure notes still explain which target blew up.
func TestFormatBackupReportPartialTarget(t *testing.T) {
	msg := FormatBackupReport("计划备份报告", []UnitResult{
		{NodeID: "n1", NodeName: "绿云犹他", TarOK: true,
			TargetOK: map[string]bool{"minio": true, "vps": true}, TargetIDs: []string{"minio", "vps"}},
		{NodeID: "n1", NodeName: "绿云犹他", TarOK: true,
			TargetOK: map[string]bool{"minio": true, "vps": false}, TargetIDs: []string{"minio", "vps"}},
	}, []TargetInfo{
		{ID: "minio", Name: "绿云储存MinIO"},
		{ID: "vps", Name: "绿云储存vps"},
	})

	// Scope status checks to the <pre> table: the "🔴 失败原因" notes header
	// below it also contains 🔴, and the table's digits are full-width (３／４),
	// so checking the whole message would misread both.
	table := msg
	if i := strings.Index(msg, "<pre>"); i >= 0 {
		table = msg[i:]
		if j := strings.Index(table, "</pre>"); j >= 0 {
			table = table[:j+len("</pre>")]
		}
	}
	switch {
	case strings.Contains(table, "🔴"):
		t.Fatalf("a single failed cell must not turn the node red: %s", msg)
	case !strings.Contains(table, "🟡"):
		t.Fatalf("partial failure should render amber status: %s", msg)
	case !strings.Contains(table, "３／４"):
		t.Fatalf("expected 3/4 cells succeeded in the table: %s", msg)
	}
	if !strings.Contains(msg, "失败原因") {
		t.Fatalf("failure notes should still explain the failed target: %s", msg)
	}
}

// TestFormatBackupReportTotalFailure asserts a fully-failed node (archive failed
// for its only unit) renders 🔴 0/N, not green or amber.
func TestFormatBackupReportTotalFailure(t *testing.T) {
	msg := FormatBackupReport("计划备份报告", []UnitResult{
		{NodeID: "n1", NodeName: "华为云", TarOK: false, Err: "agent unreachable",
			TargetOK: map[string]bool{"minio": false}, TargetIDs: []string{"minio"}},
	}, []TargetInfo{{ID: "minio", Name: "绿云储存MinIO"}})

	if !strings.Contains(msg, "🔴") {
		t.Fatalf("total failure should render red: %s", msg)
	}
	if strings.Contains(msg, "🟡") {
		t.Fatalf("total failure must not render amber: %s", msg)
	}
}
