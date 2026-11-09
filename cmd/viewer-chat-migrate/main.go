package main

import (
	"flag"
	"fmt"
	"os"

	"viewer/internal/plugins/chat"
)

func main() {
	dataDir := flag.String("data-dir", "", "Viewer data directory containing chat.sqlite3")
	confirm := flag.Bool("yes", false, "confirm the offline, one-way Chat-level migration")
	flag.Parse()
	if *dataDir == "" || !*confirm {
		fmt.Fprintln(os.Stderr, "usage: viewer-chat-migrate --data-dir PATH --yes\nStop Viewer first. Only Chat/Role/routing settings are retained; legacy messages and branches are not migrated.")
		os.Exit(2)
	}
	backup, err := chat.MigrateChatLevelSettings(*dataDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "chat-level migration failed: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("chat-level migration complete; legacy backup: %s\n", backup)
}
