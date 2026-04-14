package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"syscall"
	"time"

	"github.com/young1lin/cc-otel/internal/api"
	"github.com/young1lin/cc-otel/internal/config"
	"github.com/young1lin/cc-otel/internal/db"
	"github.com/young1lin/cc-otel/internal/logrotate"
	"github.com/young1lin/cc-otel/internal/receiver"

	"google.golang.org/grpc"
)

var version = "dev"

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	switch os.Args[1] {
	case "version", "-v", "--version":
		fmt.Printf("cc-otel %s\n", version)
		return
	case "start":
		cmdStart()
	case "restart":
		cmdRestart()
	case "stop":
		cmdStop()
	case "status":
		cmdStatus()
	case "serve":
		cmdServe()
	case "init":
		cmdInit()
	case "install":
		cmdInstall()
	case "cleanup":
		cmdCleanup()
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n\n", os.Args[1])
		printUsage()
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Fprintf(os.Stderr, "cc-otel %s — Claude Code Token Monitor\n", version)
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "Usage:")
	fmt.Fprintln(os.Stderr, "  cc-otel init                   Generate default config file")
	fmt.Fprintln(os.Stderr, "  cc-otel install                Install to user AppData (Windows)")
	fmt.Fprintln(os.Stderr, "  cc-otel start [-config path]   Start as background daemon")
	fmt.Fprintln(os.Stderr, "  cc-otel restart [-config path] Stop then start (upgrade / reload binary)")
	fmt.Fprintln(os.Stderr, "  cc-otel stop                   Stop the daemon")
	fmt.Fprintln(os.Stderr, "  cc-otel status                 Show daemon status")
	fmt.Fprintln(os.Stderr, "  cc-otel serve [-config path]   Run in foreground (for debugging)")
	fmt.Fprintln(os.Stderr, "  cc-otel cleanup [-config path] Delete old raw/event data per retention_days")
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "Default config: "+config.DefaultConfigPath())
	fmt.Fprintln(os.Stderr, "Default DB:     "+config.DefaultDBPath())
}

// --- start ---

func cmdStart() {
	fs := flag.NewFlagSet("start", flag.ExitOnError)
	cfgPath := fs.String("config", config.DefaultConfigPath(), "path to config file")
	fs.Parse(os.Args[2:])

	pidPath := config.DefaultPIDPath()

	if pid, err := readPID(pidPath); err == nil {
		if processAlive(pid) {
			fmt.Fprintf(os.Stderr, "cc-otel is already running (PID %d)\n", pid)
			os.Exit(1)
		}
		os.Remove(pidPath)
	}

	startDaemon(*cfgPath)
}

// startDaemon launches serve in the background using os.Executable() and writes the PID file.
func startDaemon(cfgPath string) {
	pidPath := config.DefaultPIDPath()

	self, err := os.Executable()
	if err != nil {
		log.Fatalf("failed to find executable: %v", err)
	}

	// Keep logs next to the selected config path (and therefore the data directory).
	logPath := filepath.Join(filepath.Dir(cfgPath), "cc-otel.log")
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		log.Fatalf("failed to open log file: %v", err)
	}

	cmd := exec.Command(self, "serve", "-config", cfgPath)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	setDetachedProcess(cmd)

	if err := cmd.Start(); err != nil {
		logFile.Close()
		log.Fatalf("failed to start daemon: %v", err)
	}
	logFile.Close()

	pid := cmd.Process.Pid
	if err := os.WriteFile(pidPath, []byte(strconv.Itoa(pid)), 0644); err != nil {
		log.Fatalf("failed to write PID file: %v", err)
	}

	time.Sleep(500 * time.Millisecond)
	if processAlive(pid) {
		fmt.Printf("cc-otel started (PID %d)\n", pid)
		fmt.Printf("Web UI: http://localhost:%d\n", defaultPort(cfgPath))
	} else {
		fmt.Fprintf(os.Stderr, "cc-otel failed to start, check logs\n")
		os.Remove(pidPath)
		os.Exit(1)
	}
}

// cmdRestart stops any running daemon, then starts using this executable (same as start).
// Use after `go build` so the new binary is what gets launched as `serve`.
func cmdRestart() {
	fs := flag.NewFlagSet("restart", flag.ExitOnError)
	cfgPath := fs.String("config", config.DefaultConfigPath(), "path to config file")
	fs.Parse(os.Args[2:])

	stopDaemonIfAny()
	time.Sleep(400 * time.Millisecond)

	pidPath := config.DefaultPIDPath()
	if pid, err := readPID(pidPath); err == nil {
		if processAlive(pid) {
			fmt.Fprintf(os.Stderr, "cc-otel is still running after stop (PID %d), try again\n", pid)
			os.Exit(1)
		}
		os.Remove(pidPath)
	}

	startDaemon(*cfgPath)
}

// killProcess sends SIGTERM (Unix) or Kill (Windows) to the given PID.
func killProcess(pid int) {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return
	}
	if runtime.GOOS == "windows" {
		proc.Kill()
	} else {
		if err := proc.Signal(syscall.SIGTERM); err != nil {
			proc.Kill()
		} else {
			time.Sleep(500 * time.Millisecond)
			if processAlive(pid) {
				proc.Kill()
				time.Sleep(300 * time.Millisecond)
			}
		}
	}
}

// stopDaemonIfAny kills the daemon if PID file points to a live process; cleans stale PID file.
// Does not exit the process; used by restart.
func stopDaemonIfAny() {
	pidPath := config.DefaultPIDPath()
	pid, err := readPID(pidPath)
	if err != nil {
		return
	}
	if !processAlive(pid) {
		os.Remove(pidPath)
		return
	}
	killProcess(pid)
	os.Remove(pidPath)
	fmt.Printf("cc-otel stopped (PID %d)\n", pid)
}

// --- stop ---

func cmdStop() {
	pidPath := config.DefaultPIDPath()

	pid, err := readPID(pidPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "cc-otel is not running (no PID file)")
		os.Exit(1)
	}

	if !processAlive(pid) {
		fmt.Println("cc-otel is not running (stale PID file)")
		os.Remove(pidPath)
		return
	}

	killProcess(pid)
	os.Remove(pidPath)
	fmt.Printf("cc-otel stopped (PID %d)\n", pid)
}

// --- status ---

func cmdStatus() {
	pidPath := config.DefaultPIDPath()

	fmt.Println("=== cc-otel status ===")
	fmt.Printf("Version: %s\n", version)

	// Local config is the fallback source of truth when the daemon is unreachable.
	localCfgPath := config.DefaultConfigPath()
	localCfg, _ := config.Load(localCfgPath)

	pid, pidErr := readPID(pidPath)
	if pidErr != nil {
		printPathsFromLocal(localCfgPath, localCfg)
		fmt.Println("Daemon: not running")
		fmt.Println("No PID file found")
		return
	}
	if !processAlive(pid) {
		printPathsFromLocal(localCfgPath, localCfg)
		fmt.Printf("Daemon: not running (stale PID %d)\n", pid)
		return
	}

	webPort := config.DefaultWebPort()
	otelPort := config.DefaultOTELPort()
	if localCfg != nil {
		if localCfg.WebPort != 0 {
			webPort = localCfg.WebPort
		}
		if localCfg.OTELPort != 0 {
			otelPort = localCfg.OTELPort
		}
	}

	// Ask the running daemon for the authoritative paths + ports. If it answers, use its values.
	remote, remoteOK := fetchDaemonStatus(webPort)
	dbPath := ""
	if remoteOK {
		if remote.WebPort != 0 {
			webPort = remote.WebPort
		}
		if remote.OTELPort != 0 {
			otelPort = remote.OTELPort
		}
		fmt.Printf("Config:  %s\n", remote.ConfigPath)
		fmt.Printf("DB:      %s\n", remote.DBPath)
		dbPath = remote.DBPath
	} else {
		printPathsFromLocal(localCfgPath, localCfg)
		if localCfg != nil {
			dbPath = localCfg.DBPath
		} else {
			dbPath = config.DefaultDBPath()
		}
	}

	fmt.Printf("Daemon PID: %d\n", pid)

	if portOpen(otelPort) {
		fmt.Printf("OTEL Receiver: :%d OK\n", otelPort)
	} else {
		fmt.Printf("OTEL Receiver: :%d NOT RESPONDING\n", otelPort)
	}

	if portOpen(webPort) {
		fmt.Printf("Web UI:        http://localhost:%d OK\n", webPort)
	} else {
		fmt.Printf("Web UI:        http://localhost:%d NOT RESPONDING\n", webPort)
		return
	}

	resp, err := http.Get(fmt.Sprintf("http://localhost:%d/api/dashboard", webPort))
	if err != nil {
		fmt.Println("Dashboard:    unavailable")
		return
	}
	defer resp.Body.Close()

	var dash db.Dashboard
	if err := json.NewDecoder(resp.Body).Decode(&dash); err != nil {
		fmt.Println("Dashboard:    parse error")
		return
	}

	fmt.Println("--- Today ---")
	fmt.Printf("Cost:         $%.4f\n", dash.TotalCostUSD)
	fmt.Printf("Input:        %s tokens\n", formatTokens(dash.TotalInputTokens))
	fmt.Printf("Output:       %s tokens\n", formatTokens(dash.TotalOutputTokens))
	fmt.Printf("Cache Hit:    %.1f%%\n", dash.CacheHitRate*100)
	fmt.Printf("Requests:     %d\n", dash.RequestCount)

	if info, err := os.Stat(dbPath); err == nil {
		fmt.Printf("DB Size:      %.1f MB\n", float64(info.Size())/1024/1024)
	}
}

// printPathsFromLocal prints Config / DB using local discovery, marking the config path
// as "(not found)" when the file is absent so users aren't misled.
func printPathsFromLocal(cfgPath string, cfg *config.Config) {
	if _, err := os.Stat(cfgPath); err != nil {
		fmt.Printf("Config:  %s (not found)\n", cfgPath)
	} else {
		fmt.Printf("Config:  %s\n", cfgPath)
	}
	dbPath := config.DefaultDBPath()
	if cfg != nil && cfg.DBPath != "" {
		dbPath = cfg.DBPath
	}
	fmt.Printf("DB:      %s\n", dbPath)
}

// fetchDaemonStatus asks the running daemon for its authoritative config/db/ports.
func fetchDaemonStatus(webPort int) (api.StatusResponse, bool) {
	var s api.StatusResponse
	client := &http.Client{Timeout: 1500 * time.Millisecond}
	resp, err := client.Get(fmt.Sprintf("http://localhost:%d/api/status", webPort))
	if err != nil {
		return s, false
	}
	defer resp.Body.Close()
	if err := json.NewDecoder(resp.Body).Decode(&s); err != nil {
		return s, false
	}
	return s, true
}

// --- serve (foreground) ---

func cmdServe() {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	cfgPath := fs.String("config", config.DefaultConfigPath(), "path to config file")
	fs.Parse(os.Args[2:])

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		log.Fatalf("failed to load config: %v", err)
	}

	// Set up rotating log (5 MB cap, keep one backup)
	logPath := filepath.Join(filepath.Dir(*cfgPath), "cc-otel.log")
	lw := logrotate.New(logPath)
	defer lw.Close()
	log.SetOutput(lw)
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
	log.Printf("cc-otel serve starting, log: %s", logPath)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	database, err := db.Init(cfg)
	if err != nil {
		log.Fatalf("failed to init database: %v", err)
	}
	defer database.Close()

	repo := db.NewRepository(database)
	defer repo.Close()

	// Bootstrap daily_model_agg from existing api_requests if agg table is empty.
	if needsRebuild, err := repo.NeedsAggRebuild(ctx); err != nil {
		log.Printf("agg rebuild check error: %v", err)
	} else if needsRebuild {
		log.Println("daily_model_agg is empty; rebuilding from api_requests...")
		if err := repo.RebuildDailyAggregates(ctx); err != nil {
			log.Printf("agg rebuild error: %v", err)
		} else {
			log.Println("daily_model_agg rebuild complete")
		}
	}

	// Retention cleanup on startup
	if cfg.RetentionDays > 0 {
		cutoff := time.Now().Unix() - int64(cfg.RetentionDays)*86400
		n, err := repo.Cleanup(ctx, cutoff)
		if err != nil {
			log.Printf("retention cleanup error: %v", err)
		} else if n > 0 {
			log.Printf("retention cleanup: deleted %d records older than %d days", n, cfg.RetentionDays)
		}
	}

	broker := api.NewBroker()

	grpcSrv := grpc.NewServer()
	otelReceiver := receiver.New(repo, cfg, broker)
	otelReceiver.Register(grpcSrv)

	grpcLis, err := net.Listen("tcp", fmt.Sprintf(":%d", cfg.OTELPort))
	if err != nil {
		log.Fatalf("failed to listen on gRPC port %d: %v", cfg.OTELPort, err)
	}
	go func() {
		log.Printf("OTEL gRPC receiver listening on :%d", cfg.OTELPort)
		if err := grpcSrv.Serve(grpcLis); err != nil {
			log.Fatalf("gRPC server error: %v", err)
		}
	}()

	absCfgPath, _ := filepath.Abs(*cfgPath)
	handler := api.NewHandler(repo, broker, cfg, absCfgPath)
	mux := http.NewServeMux()
	handler.Register(mux)
	webSrv := &http.Server{Addr: fmt.Sprintf(":%d", cfg.WebPort), Handler: loggingMiddleware(api.GzipMiddleware(mux))}
	go func() {
		log.Printf("Web UI listening on http://localhost:%d", cfg.WebPort)
		if err := webSrv.ListenAndServe(); err != http.ErrServerClosed {
			log.Fatalf("web server error: %v", err)
		}
	}()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh
	log.Println("shutting down...")

	// Graceful stop with timeout to avoid hanging on stuck clients.
	done := make(chan struct{})
	go func() {
		grpcSrv.GracefulStop()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		log.Println("gRPC graceful stop timed out, forcing stop")
		grpcSrv.Stop()
	}

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()
	webSrv.Shutdown(shutdownCtx)
	log.Println("done")
}

// --- helpers ---

func readPID(path string) (int, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	pid, err := strconv.Atoi(string(data))
	if err != nil {
		return 0, fmt.Errorf("invalid PID: %s", data)
	}
	return pid, nil
}

// processAlive is defined in proc_windows.go / proc_unix.go

func portOpen(port int) bool {
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("localhost:%d", port), 1*time.Second)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

func defaultPort(cfgPath string) int {
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return config.DefaultWebPort()
	}
	return cfg.WebPort
}

func formatTokens(n int64) string {
	if n >= 1000000 {
		return fmt.Sprintf("%.1fM", float64(n)/1000000)
	}
	if n >= 1000 {
		return fmt.Sprintf("%.1fK", float64(n)/1000)
	}
	return strconv.FormatInt(n, 10)
}

// --- init ---

func cmdInit() {
	cfgPath := config.DefaultConfigPath()

	if _, err := os.Stat(cfgPath); err == nil {
		fmt.Printf("Config already exists: %s\n", cfgPath)
		fmt.Println("Delete it first if you want to regenerate.")
		os.Exit(1)
	}

	content := fmt.Sprintf(`# cc-otel configuration
# Edit this file to customize cc-otel behavior.
# Restart the daemon after changes: cc-otel restart   (or: stop && start)

# OTLP gRPC receiver port
# Claude Code sends telemetry to this port.
# Set OTEL_EXPORTER_OTLP_ENDPOINT=http://localhost:<otel_port> in Claude Code env.
otel_port: 4317

# Web UI port
# Access the dashboard at http://localhost:<web_port>
web_port: 8899

# SQLite database path (~ is expanded to home directory)
db_path: %s

# Model name mapping: map a local/proxy model name to the Claude model it uses.
# This lets you see the "actual" model name in the dashboard.
# Format: local_name: claude_model_id
# Example:
#   model_mapping:
#     my-proxy: claude-opus-4-6
#     glm-5: claude-sonnet-4-6
model_mapping: {}
`, config.DefaultDBPath())

	if err := os.MkdirAll(filepath.Dir(cfgPath), 0755); err != nil {
		log.Fatalf("failed to create config dir: %v", err)
	}
	if err := os.WriteFile(cfgPath, []byte(content), 0644); err != nil {
		log.Fatalf("failed to write config: %v", err)
	}
	fmt.Printf("Config written to: %s\n", cfgPath)
	fmt.Println("Edit it as needed, then run: cc-otel start")
}

// --- install ---
// Copies the executable to ~/.claude/bin/ and ensures the data directory exists.
// Cross-platform: works on Windows, macOS, and Linux.
func cmdInstall() {
	binDir := config.DefaultBinDir()
	if binDir == "" {
		log.Fatalf("failed to determine install dir")
	}
	if err := os.MkdirAll(binDir, 0755); err != nil {
		log.Fatalf("failed to create bin dir: %v", err)
	}

	self, err := os.Executable()
	if err != nil {
		log.Fatalf("failed to find executable: %v", err)
	}

	exeName := "cc-otel"
	if runtime.GOOS == "windows" {
		exeName = "cc-otel.exe"
	}
	dstExe := filepath.Join(binDir, exeName)

	// On Windows, we must not overwrite a running executable. Best effort stop first.
	stopDaemonIfAny()
	time.Sleep(500 * time.Millisecond)

	in, err := os.Open(self)
	if err != nil {
		log.Fatalf("failed to open self: %v", err)
	}
	defer in.Close()

	out, err := os.Create(dstExe)
	if err != nil {
		log.Fatalf("failed to create %s: %v", dstExe, err)
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		log.Fatalf("failed to write %s: %v", dstExe, err)
	}
	if err := out.Close(); err != nil {
		log.Fatalf("failed to close %s: %v", dstExe, err)
	}

	// Make executable on Unix
	if runtime.GOOS != "windows" {
		os.Chmod(dstExe, 0755)
	}

	fmt.Printf("Installed: %s\n", dstExe)
	fmt.Printf("Config:    %s\n", config.DefaultConfigPath())
	fmt.Printf("DB:        %s\n", config.DefaultDBPath())
	fmt.Println("Next: cc-otel start")
}

// --- cleanup ---

func cmdCleanup() {
	fs := flag.NewFlagSet("cleanup", flag.ExitOnError)
	cfgPath := fs.String("config", config.DefaultConfigPath(), "path to config file")
	fs.Parse(os.Args[2:])

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		log.Fatalf("failed to load config: %v", err)
	}

	if cfg.RetentionDays <= 0 {
		fmt.Println("retention_days is 0 (disabled), nothing to clean up")
		return
	}

	database, err := db.Init(cfg)
	if err != nil {
		log.Fatalf("failed to init database: %v", err)
	}
	defer database.Close()

	repo := db.NewRepository(database)
	defer repo.Close()
	cutoff := time.Now().Unix() - int64(cfg.RetentionDays)*86400
	n, err := repo.Cleanup(context.Background(), cutoff)
	if err != nil {
		log.Fatalf("cleanup failed: %v", err)
	}
	fmt.Printf("Cleaned up %d records older than %d days\n", n, cfg.RetentionDays)
}

// --- middleware ---

func loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// SSE is long-lived; log at connect time, not on disconnect.
		if r.URL.Path == "/api/events" {
			log.Printf("%s %s (sse)", r.Method, r.URL.Path)
			next.ServeHTTP(w, r)
			return
		}
		start := time.Now()
		next.ServeHTTP(w, r)
		log.Printf("%s %s %s", r.Method, r.URL.Path, time.Since(start))
	})
}

