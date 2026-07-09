package main

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// LogDebug appends a formatted message to .foreman/debug.log in the target cwd.
func LogDebug(cwd string, format string, args ...interface{}) {
	if cwd == "" {
		cwd = "."
	}
	
	logDir := filepath.Join(cwd, ".foreman")
	if err := os.MkdirAll(logDir, 0755); err != nil {
		// Ignore errors creating directory, we just won't log
		return
	}

	logFile := filepath.Join(logDir, "debug.log")
	f, err := os.OpenFile(logFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return
	}
	defer f.Close()

	timestamp := time.Now().Format("2006-01-02 15:04:05")
	msg := fmt.Sprintf(format, args...)
	
	// Ensure the message ends with a newline
	if len(msg) > 0 && msg[len(msg)-1] != '\n' {
		msg += "\n"
	}
	
	logLine := fmt.Sprintf("[%s] %s", timestamp, msg)
	if _, err := f.WriteString(logLine); err != nil {
		// Ignore write errors
	}
}
