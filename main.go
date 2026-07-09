package main

import (
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"
)

func main() {
	// 1. Get current working directory (the target repository/workspace)
	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error getting current working directory: %v\n", err)
		os.Exit(1)
	}

	// 2. Initialize logger (wipe previous log)
	InitLogger(cwd)

	// 3. Initialize and run Bubble Tea program
	p := tea.NewProgram(InitialModel(cwd), tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "Alas, there's been an error: %v\n", err)
		os.Exit(1)
	}
}
