package main

import (
	"path/filepath"
	"reflect"
	"testing"
)

func TestBuildPathsUsesLocalAndGlobalNamesInBinDir(t *testing.T) {
	got, err := buildPaths(`D:\repo\bin`, `C:\Users\me\.claude\cc-otel`, "20260427-090000")
	if err != nil {
		t.Fatalf("build paths: %v", err)
	}

	want := mergePaths{
		BinDir:     filepath.Clean(`D:\repo\bin`),
		GlobalDir:  filepath.Clean(`C:\Users\me\.claude\cc-otel`),
		BinDB:      filepath.Clean(`D:\repo\bin\cc-otel.db`),
		BinWAL:     filepath.Clean(`D:\repo\bin\cc-otel.db-wal`),
		BinSHM:     filepath.Clean(`D:\repo\bin\cc-otel.db-shm`),
		GlobalDB:   filepath.Clean(`C:\Users\me\.claude\cc-otel\cc-otel.db`),
		LocalCopy:  filepath.Clean(`D:\repo\bin\local.db`),
		GlobalCopy: filepath.Clean(`D:\repo\bin\global.db`),
		ExportFile: filepath.Clean(`D:\repo\bin\merge-bin-global-20260427-090000.jsonl`),
		BackupDir:  filepath.Clean(`D:\repo\bin\backup-merge-bin-global-20260427-090000`),
		PIDFile:    filepath.Clean(`D:\repo\bin\cc-otel.pid`),
		BinExe:     filepath.Clean(`D:\repo\bin\cc-otel.exe`),
	}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("paths mismatch\nwant: %#v\n got: %#v", want, got)
	}
}

func TestOperationPlanStopsBinBeforeSnapshotAndReplacesAfterVerify(t *testing.T) {
	got := operationPlan()
	want := []string{
		"backup-bin-db-files",
		"stop-bin-process",
		"snapshot-local-db",
		"snapshot-global-db",
		"export-local-jsonl",
		"import-jsonl-into-global-copy",
		"repair-daily-agg",
		"verify-merged-copy",
		"replace-bin-db",
	}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("operation plan mismatch\nwant: %#v\n got: %#v", want, got)
	}
}

func TestSamePathTreatsRelativeAndAbsolutePathsAsEqual(t *testing.T) {
	rel := filepath.Join(".", "bin", "cc-otel.exe")
	abs, err := filepath.Abs(rel)
	if err != nil {
		t.Fatalf("abs path: %v", err)
	}

	if !samePath(rel, abs) {
		t.Fatalf("samePath(%q, %q) = false, want true", rel, abs)
	}
}
