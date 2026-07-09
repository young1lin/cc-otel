// Online SQLite snapshot via VACUUM INTO.
// Reads a live (WAL) database WITHOUT stopping the process that owns it and
// writes a consistent, defragmented copy to a new file.
//
//	go run ./tools/snapshot_db <src.db> <dst.db>   (dst must not exist)
//
// See .claude/rules/db-copy-no-stop.md — copying the DB must never stop the
// running daemon (the global instance at ~/.claude/cc-otel/ in particular).
package main

import (
	"database/sql"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	_ "github.com/ncruces/go-sqlite3/driver"
)

func main() {
	if len(os.Args) < 3 {
		log.Fatal("usage: go run ./tools/snapshot_db <src.db> <dst.db>")
	}
	srcArg, dstArg := os.Args[1], os.Args[2]
	if _, err := os.Stat(dstArg); err == nil {
		log.Fatalf("destination already exists: %s", dstArg)
	}
	src := filepath.ToSlash(filepath.Clean(srcArg))
	dst := filepath.ToSlash(filepath.Clean(dstArg))

	// Open the source read-only so we can never write to the live database.
	db, err := sql.Open("sqlite3", fmt.Sprintf("file:%s?mode=ro&_busy_timeout=10000", src))
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	// VACUUM INTO takes a read transaction on the source and emits a consistent
	// snapshot — safe while another process keeps writing (WAL).
	quoted := "'" + strings.ReplaceAll(dst, "'", "''") + "'"
	if _, err := db.Exec("VACUUM INTO " + quoted); err != nil {
		log.Fatalf("VACUUM INTO: %v", err)
	}
	st, _ := os.Stat(dstArg)
	fmt.Printf("snapshot OK: %s (%d bytes)\n", dstArg, st.Size())
}
