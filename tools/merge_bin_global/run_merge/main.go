package main

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	_ "github.com/ncruces/go-sqlite3/driver"
)

type mergePaths struct {
	BinDir     string
	GlobalDir  string
	BinDB      string
	BinWAL     string
	BinSHM     string
	GlobalDB   string
	LocalCopy  string
	GlobalCopy string
	ExportFile string
	BackupDir  string
	PIDFile    string
	BinExe     string
}

type config struct {
	RepoDir        string
	BinDir         string
	GlobalDir      string
	From           string
	To             string
	RepairFromDate string
	RepairToDate   string
	Yes            bool
	Timeout        time.Duration
}

func main() {
	cfg := parseFlags()
	if err := run(context.Background(), cfg); err != nil {
		fmt.Fprintf(os.Stderr, "merge failed: %v\n", err)
		os.Exit(1)
	}
}

func parseFlags() config {
	home, _ := os.UserHomeDir()
	defaultGlobal := filepath.Join(home, ".claude", "cc-otel")

	var cfg config
	flag.StringVar(&cfg.RepoDir, "repo", ".", "repository root used for go run helper tools")
	flag.StringVar(&cfg.BinDir, "bin-dir", filepath.Join(".", "bin"), "bin directory containing local cc-otel.db and cc-otel.exe")
	flag.StringVar(&cfg.GlobalDir, "global-dir", defaultGlobal, "global ~/.claude/cc-otel directory")
	flag.StringVar(&cfg.From, "from", "1970-01-01T00:00:00+08:00", "export window start RFC3339")
	flag.StringVar(&cfg.To, "to", "", "export window end RFC3339; default is now")
	flag.StringVar(&cfg.RepairFromDate, "repair-from-date", "", "daily_model_agg repair start date YYYY-MM-DD; default from local data")
	flag.StringVar(&cfg.RepairToDate, "repair-to-date", "", "daily_model_agg repair end date YYYY-MM-DD; default from local data")
	flag.BoolVar(&cfg.Yes, "yes", false, "execute changes; without this flag only prints the operation plan")
	flag.DurationVar(&cfg.Timeout, "timeout", 2*time.Minute, "timeout for stopping the bin process")
	flag.Parse()
	return cfg
}

func operationPlan() []string {
	return []string{
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
}

func buildPaths(binDir, globalDir, stamp string) (mergePaths, error) {
	if strings.TrimSpace(binDir) == "" {
		return mergePaths{}, errors.New("bin dir is required")
	}
	if strings.TrimSpace(globalDir) == "" {
		return mergePaths{}, errors.New("global dir is required")
	}
	if strings.TrimSpace(stamp) == "" {
		return mergePaths{}, errors.New("stamp is required")
	}

	binDir = filepath.Clean(binDir)
	globalDir = filepath.Clean(globalDir)
	binDB := filepath.Join(binDir, "cc-otel.db")
	return mergePaths{
		BinDir:     binDir,
		GlobalDir:  globalDir,
		BinDB:      binDB,
		BinWAL:     binDB + "-wal",
		BinSHM:     binDB + "-shm",
		GlobalDB:   filepath.Join(globalDir, "cc-otel.db"),
		LocalCopy:  filepath.Join(binDir, "local.db"),
		GlobalCopy: filepath.Join(binDir, "global.db"),
		ExportFile: filepath.Join(binDir, "merge-bin-global-"+stamp+".jsonl"),
		BackupDir:  filepath.Join(binDir, "backup-merge-bin-global-"+stamp),
		PIDFile:    filepath.Join(binDir, "cc-otel.pid"),
		BinExe:     filepath.Join(binDir, "cc-otel.exe"),
	}, nil
}

func run(ctx context.Context, cfg config) error {
	stamp := time.Now().Format("20060102-150405")
	paths, err := buildPaths(cfg.BinDir, cfg.GlobalDir, stamp)
	if err != nil {
		return err
	}

	fmt.Println("Operation plan:")
	for i, step := range operationPlan() {
		fmt.Printf("%d. %s\n", i+1, step)
	}
	fmt.Printf("\nlocal copy:  %s\n", paths.LocalCopy)
	fmt.Printf("global copy: %s\n", paths.GlobalCopy)
	fmt.Printf("backup dir:  %s\n\n", paths.BackupDir)

	if !cfg.Yes {
		fmt.Println("Dry run only. Re-run with -yes to execute.")
		return nil
	}

	to := cfg.To
	if to == "" {
		to = time.Now().Format(time.RFC3339)
	}
	if _, err := time.Parse(time.RFC3339, cfg.From); err != nil {
		return fmt.Errorf("parse -from: %w", err)
	}
	if _, err := time.Parse(time.RFC3339, to); err != nil {
		return fmt.Errorf("parse -to: %w", err)
	}

	if err := ensureFile(paths.BinDB); err != nil {
		return err
	}
	if err := ensureFile(paths.GlobalDB); err != nil {
		return err
	}

	fmt.Println("Backing up bin DB files...")
	if err := backupBinDBFiles(paths); err != nil {
		return err
	}

	fmt.Println("Stopping bin cc-otel process...")
	if err := stopBinProcess(ctx, paths, cfg.Timeout); err != nil {
		return err
	}

	fmt.Println("Creating local.db snapshot from bin DB...")
	if err := snapshotDB(ctx, paths.BinDB, paths.LocalCopy); err != nil {
		return err
	}

	fmt.Println("Creating global.db snapshot from global DB...")
	if err := snapshotDB(ctx, paths.GlobalDB, paths.GlobalCopy); err != nil {
		return err
	}

	repairFrom, repairTo := cfg.RepairFromDate, cfg.RepairToDate
	if repairFrom == "" || repairTo == "" {
		minDay, maxDay, err := localDateRange(ctx, paths.LocalCopy)
		if err != nil {
			return err
		}
		if repairFrom == "" {
			repairFrom = minDay
		}
		if repairTo == "" {
			repairTo = maxDay
		}
	}

	fmt.Println("Exporting local data...")
	if err := runGoTool(ctx, cfg.RepoDir, ".\\tools\\merge_bin_global\\export_bin",
		"-src", paths.LocalCopy,
		"-out", paths.ExportFile,
		"-from", cfg.From,
		"-to", to,
	); err != nil {
		return err
	}

	fmt.Println("Importing local data into global.db...")
	if err := runGoTool(ctx, cfg.RepoDir, ".\\tools\\merge_bin_global\\import_global",
		"-dst", paths.GlobalCopy,
		"-in", paths.ExportFile,
		"-source", "bin",
	); err != nil {
		return err
	}

	if repairFrom != "" && repairTo != "" {
		fmt.Printf("Repairing daily_model_agg for %s..%s...\n", repairFrom, repairTo)
		if err := runGoTool(ctx, cfg.RepoDir, ".\\tools\\merge_bin_global\\repair_daily_agg",
			"-db", paths.GlobalCopy,
			"-from-date", repairFrom,
			"-to-date", repairTo,
		); err != nil {
			return err
		}
	}

	fmt.Println("Verifying merged global.db...")
	verifyArgs := []string{
		"-bin", paths.LocalCopy,
		"-global", paths.GlobalCopy,
		"-from", cfg.From,
		"-to", to,
	}
	if repairTo != "" {
		verifyArgs = append(verifyArgs, "-local-day", repairTo)
	}
	if err := runGoTool(ctx, cfg.RepoDir, ".\\tools\\merge_bin_global\\verify_merge", verifyArgs...); err != nil {
		return err
	}

	fmt.Println("Replacing bin cc-otel.db with merged global.db...")
	if err := replaceBinDB(paths); err != nil {
		return err
	}

	fmt.Println("Merged DB installed at:", paths.BinDB)
	fmt.Println("Original bin DB backup:", paths.BackupDir)
	return nil
}

func ensureFile(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("stat %s: %w", path, err)
	}
	if info.IsDir() {
		return fmt.Errorf("%s is a directory", path)
	}
	return nil
}

func backupBinDBFiles(paths mergePaths) error {
	if err := os.MkdirAll(paths.BackupDir, 0o755); err != nil {
		return fmt.Errorf("create backup dir: %w", err)
	}
	for _, src := range []string{paths.BinDB, paths.BinWAL, paths.BinSHM} {
		if _, err := os.Stat(src); err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return err
		}
		dst := filepath.Join(paths.BackupDir, filepath.Base(src))
		if err := copyFile(src, dst); err != nil {
			return err
		}
	}
	return nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("open %s: %w", src, err)
	}
	defer in.Close()

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("create %s: %w", dst, err)
	}
	defer out.Close()

	if _, err := io.Copy(out, in); err != nil {
		return fmt.Errorf("copy %s to %s: %w", src, dst, err)
	}
	return out.Sync()
}

func stopBinProcess(ctx context.Context, paths mergePaths, timeout time.Duration) error {
	pidBytes, err := os.ReadFile(paths.PIDFile)
	if err != nil {
		if os.IsNotExist(err) {
			fmt.Println("No pid file found; assuming bin process is already stopped.")
			return nil
		}
		return fmt.Errorf("read pid file: %w", err)
	}
	pidText := strings.TrimSpace(string(pidBytes))
	if pidText == "" {
		fmt.Println("Pid file is empty; assuming bin process is already stopped.")
		return nil
	}
	pid, err := strconv.Atoi(pidText)
	if err != nil {
		return fmt.Errorf("parse pid %q: %w", pidText, err)
	}

	exePath, err := processExecutablePath(ctx, pid)
	if err != nil {
		return err
	}
	if exePath == "" {
		fmt.Printf("Process %d is not running.\n", pid)
		return nil
	}
	if !samePath(exePath, paths.BinExe) {
		return fmt.Errorf("pid %d points to %s, not %s; refusing to stop it", pid, exePath, paths.BinExe)
	}

	if runtime.GOOS == "windows" {
		_ = exec.CommandContext(ctx, "taskkill", "/PID", pidText, "/T").Run()
	} else {
		proc, err := os.FindProcess(pid)
		if err == nil {
			_ = proc.Signal(os.Interrupt)
		}
	}

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		exePath, err = processExecutablePath(ctx, pid)
		if err != nil {
			return err
		}
		if exePath == "" {
			return nil
		}
		time.Sleep(500 * time.Millisecond)
	}

	if runtime.GOOS == "windows" {
		if err := exec.CommandContext(ctx, "taskkill", "/PID", pidText, "/T", "/F").Run(); err != nil {
			return fmt.Errorf("force stop process %d: %w", pid, err)
		}
	}
	return nil
}

func processExecutablePath(ctx context.Context, pid int) (string, error) {
	if runtime.GOOS != "windows" {
		return "", nil
	}
	cmd := exec.CommandContext(ctx, "powershell", "-NoProfile", "-Command",
		fmt.Sprintf(`$p = Get-CimInstance Win32_Process -Filter "ProcessId = %d"; if ($p) { $p.ExecutablePath }`, pid),
	)
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("query process %d: %w", pid, err)
	}
	return strings.TrimSpace(string(out)), nil
}

func samePath(a, b string) bool {
	absA, errA := filepath.Abs(filepath.Clean(a))
	absB, errB := filepath.Abs(filepath.Clean(b))
	if errA == nil && errB == nil {
		return strings.EqualFold(absA, absB)
	}
	return strings.EqualFold(filepath.Clean(a), filepath.Clean(b))
}

func snapshotDB(ctx context.Context, src, dst string) error {
	_ = os.Remove(dst)
	db, err := sql.Open("sqlite3", fmt.Sprintf("file:%s?mode=ro&_busy_timeout=5000", filepath.ToSlash(filepath.Clean(src))))
	if err != nil {
		return fmt.Errorf("open snapshot source: %w", err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)

	if _, err := db.ExecContext(ctx, "VACUUM INTO ?", filepath.Clean(dst)); err != nil {
		return fmt.Errorf("vacuum %s into %s: %w", src, dst, err)
	}
	return nil
}

func localDateRange(ctx context.Context, dbPath string) (string, string, error) {
	db, err := sql.Open("sqlite3", fmt.Sprintf("file:%s?mode=ro&_busy_timeout=5000", filepath.ToSlash(filepath.Clean(dbPath))))
	if err != nil {
		return "", "", err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)

	// Union of Claude and Codex api_requests date ranges
	var minDay, maxDay sql.NullString
	err = db.QueryRowContext(ctx, `
		SELECT
			MIN(d),
			MAX(d)
		FROM (
			SELECT date(timestamp, 'unixepoch', 'localtime') AS d FROM api_requests
			UNION ALL
			SELECT date(timestamp, 'unixepoch', 'localtime') AS d FROM codex_api_requests
		)
	`).Scan(&minDay, &maxDay)
	if err != nil {
		return "", "", fmt.Errorf("query local date range: %w", err)
	}
	if !minDay.Valid || !maxDay.Valid {
		return "", "", nil
	}
	return minDay.String, maxDay.String, nil
}

func runGoTool(ctx context.Context, repoDir, pkg string, args ...string) error {
	goArgs := append([]string{"run", pkg}, args...)
	cmd := exec.CommandContext(ctx, "go", goArgs...)
	cmd.Dir = repoDir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("go %s: %w", strings.Join(goArgs, " "), err)
	}
	return nil
}

func replaceBinDB(paths mergePaths) error {
	for _, path := range []string{paths.BinDB, paths.BinWAL, paths.BinSHM} {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove %s: %w", path, err)
		}
	}
	if err := os.Rename(paths.GlobalCopy, paths.BinDB); err != nil {
		return fmt.Errorf("rename %s to %s: %w", paths.GlobalCopy, paths.BinDB, err)
	}
	return nil
}
