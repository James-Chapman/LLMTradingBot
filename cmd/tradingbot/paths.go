package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/James-Chapman/LLMTradingBot/internal/config"
)

// defaultFrontendDir is the -frontend default: the dashboard folder, looked
// for in the bot's home folder.
const defaultFrontendDir = "frontend"

// Default file names inside the bot's home folder.
const (
	defaultDatabaseFile = "trading_bot.db"
	defaultLogFile      = "trading_bot.log"
)

// homeCandidates lists where the bot's home folder may be, in order: the
// working folder, the folder holding the executable, and the folder above
// it (the executable is normally built into bin/).
func homeCandidates() []string {
	candidates := []string{"."}
	if exe, err := os.Executable(); err == nil {
		exeDir := filepath.Dir(exe)
		candidates = append(candidates, exeDir, filepath.Dir(exeDir))
	}
	return candidates
}

// hasDashboard reports whether dir holds the dashboard page.
func hasDashboard(dir string) bool {
	info, err := os.Stat(filepath.Join(dir, "index.html"))
	return err == nil && !info.IsDir()
}

// resolvePaths makes the default file locations independent of the folder
// the bot is started from. Double-clicking bin\tradingbot.exe starts it in
// bin, where a relative "frontend" does not exist (every page was a 404) and
// a relative database path silently created a new, empty database.
//
// The home folder is the first candidate that holds frontend/index.html. The
// dashboard, and the database and log file when left at their defaults, are
// taken from it. Paths given with -db, -log or -frontend are used as given.
// With no dashboard to serve, an error says where it looked.
func resolvePaths(boot config.Bootstrap, frontendDir string, candidates []string) (config.Bootstrap, string, error) {
	if frontendDir != defaultFrontendDir {
		if !hasDashboard(frontendDir) {
			return boot, "", fmt.Errorf("-frontend %q holds no index.html", frontendDir)
		}
		return boot, frontendDir, nil
	}

	for _, home := range candidates {
		if !hasDashboard(filepath.Join(home, defaultFrontendDir)) {
			continue
		}
		defaults := config.Defaults()
		if boot.DatabasePath == defaults.DatabasePath {
			boot.DatabasePath = filepath.Join(home, defaultDatabaseFile)
		}
		if boot.LogFile == defaults.LogFile {
			boot.LogFile = filepath.Join(home, defaultLogFile)
		}
		return boot, filepath.Join(home, defaultFrontendDir), nil
	}
	return boot, "", fmt.Errorf(
		"the dashboard files (%s/index.html) were not found in: %s; start the bot with launch.bat, from the folder that holds %s, or pass -frontend <folder>",
		defaultFrontendDir, strings.Join(absolute(candidates), ", "), defaultFrontendDir)
}

// absolute returns each path in absolute form for an error message, leaving
// a path as it is when it cannot be made absolute.
func absolute(paths []string) []string {
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		if abs, err := filepath.Abs(p); err == nil {
			p = abs
		}
		out = append(out, p)
	}
	return out
}
