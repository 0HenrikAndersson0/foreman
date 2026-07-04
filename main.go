package main

import (
	"fmt"
	"os"
	"os/exec"

	tea "github.com/charmbracelet/bubbletea"
)

func main() {
	// 1. Get current working directory (the target repository/workspace)
	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error getting current working directory: %v\n", err)
		os.Exit(1)
	}

	// 2. Validate that AGY CLI is accessible
	if _, err := os.Stat(agyPath); os.IsNotExist(err) {
		// Fallback to checking PATH
		_, pathErr := exec.LookPath("agy")
		if pathErr != nil {
			fmt.Fprintf(os.Stderr, "Error: Antigravity CLI ('agy') not found at %s or in system PATH.\n", agyPath)
			fmt.Fprintf(os.Stderr, "Please ensure AGY CLI is installed and running.\n")
			os.Exit(1)
		}
	}

	// 3. Initialize and run Bubble Tea program
	p := tea.NewProgram(InitialModel(cwd), tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "Alas, there's been an error: %v\n", err)
		os.Exit(1)
	}
}
