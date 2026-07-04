package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type screenState int

const (
	screenFolderInput screenState = iota
	screenLoadingModels
	screenModelSelect
	screenPromptInput
	screenPlanning
	screenReview
	screenExecution
	screenFinished
	screenError
	screenValidating
)

// Styles
var (
	titleStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#7D56F4")).
			Padding(0, 1).
			MarginBottom(1)

	subtitleStyle = lipgloss.NewStyle().
			Italic(true).
			Foreground(lipgloss.Color("#8A8A8A"))

	selectedItemStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(lipgloss.Color("#7D56F4"))

	itemStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#C8C8C8"))

	successStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#43BF6D"))

	errorStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#E06C75"))

	runningStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#61AFEF"))

	borderStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("#6272A4")).
			Padding(1)

	helpStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#6272A4")).
			Italic(true)

	fadedStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#5c6370"))
)

// Message types
type modelsLoadedMsg []string
type stepFinishedMsg struct {
	index int
	err   error
}
type errMsg error

type agyResult struct {
	plan string
	err  error
}

type agyProgressMsg string
type agyCompletedMsg agyResult

type agyRunner struct {
	progress chan string
	result   chan agyResult
}

func newAGYRunner() *agyRunner {
	return &agyRunner{
		progress: make(chan string, 100),
		result:   make(chan agyResult, 1),
	}
}

type stepProgressMsg string

type stepRunner struct {
	progress chan string
	result   chan stepFinishedMsg
}

func newStepRunner() *stepRunner {
	return &stepRunner{
		progress: make(chan string, 100),
		result:   make(chan stepFinishedMsg, 1),
	}
}

type Model struct {
	state          screenState
	cwd            string
	err            error
	
	// Directory select
	folderInput    textinput.Model
	folderError    string

	// Ollama Models
	models         []string
	selectedModel  int

	// Inputs
	promptInput     textinput.Model
	feedbackInput   textinput.Model

	// Loading indicators
	spinner        spinner.Model

	// Viewport for reading the plan
	viewport       viewport.Model
	rawPlan        string
	steps          []Step

	// AGY CLI streaming
	agyRunner      *agyRunner
	streamingLog   string

	// Step execution streaming
	stepRunner     *stepRunner
	executionLog   string

	// Execution tracking
	currentStepIdx int
	autoExecute    bool
	isExecuting    bool
	statusMsg      string
	terminalWidth  int
	terminalHeight int
}

func InitialModel(cwd string) Model {
	s := spinner.New()
	s.Spinner = spinner.Dot
	s.Style = lipgloss.NewStyle().Foreground(lipgloss.Color("#7D56F4"))

	fo := textinput.New()
	fo.Placeholder = "Enter target project folder..."
	fo.SetValue(cwd)
	fo.Focus()
	fo.Width = 80

	pi := textinput.New()
	pi.Placeholder = "Type task prompt here (e.g. 'Add a log helper file to utils')..."
	pi.Width = 80

	fi := textinput.New()
	fi.Placeholder = "Type feedback here to refine plan (e.g. 'Use string type for helper argument')..."
	fi.Width = 80

	return Model{
		state:         screenFolderInput,
		cwd:           cwd,
		folderInput:   fo,
		promptInput:   pi,
		feedbackInput: fi,
		spinner:       s,
		viewport:      viewport.New(80, 20),
		terminalWidth: 80, // default fallback
		terminalHeight: 24, // default fallback
	}
}

func (m Model) Init() tea.Cmd {
	return m.spinner.Tick
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c":
			return m, tea.Quit
		}

		// Handle keys based on state
		switch m.state {
		case screenFolderInput:
			switch msg.String() {
			case "enter":
				path := m.folderInput.Value()
				if strings.TrimSpace(path) == "/exit" {
					return m, tea.Quit
				}
				cleanPath := filepath.Clean(path)
				
				// Handle relative paths
				if !filepath.IsAbs(cleanPath) {
					// We temporarily store the starting directory in m.cwd during initialization,
					// so we can resolve relative path against it.
					cleanPath = filepath.Join(m.cwd, cleanPath)
				}
				
				info, err := os.Stat(cleanPath)
				if err != nil || !info.IsDir() {
					m.folderError = "Path does not exist or is not a directory. Please try again."
				} else {
					m.cwd = cleanPath
					m.state = screenLoadingModels
					return m, m.loadModelsCmd
				}
			}
			m.folderInput, cmd = m.folderInput.Update(msg)
			cmds = append(cmds, cmd)

		case screenModelSelect:
			switch msg.String() {
			case "up", "k":
				if m.selectedModel > 0 {
					m.selectedModel--
				}
			case "down", "j":
				if m.selectedModel < len(m.models)-1 {
					m.selectedModel++
				}
			case "enter":
				m.state = screenPromptInput
				m.promptInput.Focus()
			}

		case screenPromptInput:
			switch msg.String() {
			case "enter":
				prompt := m.promptInput.Value()
				if strings.TrimSpace(prompt) == "/exit" {
					return m, tea.Quit
				}
				if strings.TrimSpace(prompt) != "" {
					m, cmd = m.startPlanning(prompt, false)
					return m, cmd
				}
			}
			m.promptInput, cmd = m.promptInput.Update(msg)
			cmds = append(cmds, cmd)

		case screenReview:
			switch msg.String() {
			case "ctrl+e":
				m.state = screenExecution
				m.currentStepIdx = 0
				m.isExecuting = false
				return m, nil
			case "enter":
				feedback := m.feedbackInput.Value()
				if strings.TrimSpace(feedback) == "/exit" {
					return m, tea.Quit
				}
				if strings.TrimSpace(feedback) != "" {
					m.feedbackInput.SetValue("")
					m, cmd = m.startPlanning(feedback, true)
					return m, cmd
				}
			}

			// Scroll viewport using keys
			m.viewport, cmd = m.viewport.Update(msg)
			cmds = append(cmds, cmd)

			m.feedbackInput, cmd = m.feedbackInput.Update(msg)
			cmds = append(cmds, cmd)

		case screenExecution:
			step := &m.steps[m.currentStepIdx]
			
			if step.Status == StateError {
				switch msg.String() {
				case "ctrl+s":
					// Skip step
					step.Status = StateSuccess
					m.currentStepIdx++
					m.feedbackInput.Blur()
					if m.currentStepIdx >= len(m.steps) {
						return m.startValidation()
					}
					return m, nil
				case "ctrl+r":
					// Retry step raw (clear feedback)
					step.Feedback = ""
					m.feedbackInput.Blur()
					m.statusMsg = fmt.Sprintf("Retrying step %d: %s...", step.Index, step.Description)
					return m.startStepExecution(step)
				case "ctrl+q":
					// Abort back to review
					m.state = screenReview
					m.autoExecute = false
					m.isExecuting = false
					m.feedbackInput.Focus()
					return m, nil
				case "enter":
					feedback := m.feedbackInput.Value()
					if strings.TrimSpace(feedback) == "/exit" {
						return m, tea.Quit
					}
					
					if step.Type == StepCommand {
						// A command step failed.
						// We find the last modified file step.
						prevIdx := -1
						for idx := m.currentStepIdx - 1; idx >= 0; idx-- {
							if m.steps[idx].Type == StepModify || m.steps[idx].Type == StepCreate {
								prevIdx = idx
								break
							}
						}
						
						if prevIdx != -1 {
							prevStep := &m.steps[prevIdx]
							
							if strings.TrimSpace(feedback) == "" {
								// AUTO-FIX MODE: empty feedback, use the command error output to instruct the fix
								prevStep.Feedback = fmt.Sprintf("The build check command '%s' failed with the following error output. Please fix the code in this file to resolve this build error:\n\n%s", step.Command, m.executionLog)
								m.statusMsg = fmt.Sprintf("Ollama is auto-fixing %s based on compiler errors...", prevStep.Path)
							} else {
								// MANUAL FEEDBACK MODE: use user's explicit instructions
								prevStep.Feedback = feedback
								m.statusMsg = fmt.Sprintf("Re-applying Step %d: %s with correction...", prevStep.Index, prevStep.Description)
							}
							
							m.currentStepIdx = prevIdx
							m.feedbackInput.SetValue("")
							m.feedbackInput.Blur()
							return m.startStepExecution(prevStep)
						}
					} else {
						// A modify or create step failed.
						if strings.TrimSpace(feedback) == "" {
							// If empty, just retry raw
							step.Feedback = ""
							m.statusMsg = fmt.Sprintf("Retrying step %d: %s...", step.Index, step.Description)
						} else {
							step.Feedback = feedback
							m.statusMsg = fmt.Sprintf("Retrying step %d: %s with correction...", step.Index, step.Description)
						}
						m.feedbackInput.SetValue("")
						m.feedbackInput.Blur()
						return m.startStepExecution(step)
					}
				}
				
				// Let text input process keys
				m.feedbackInput, cmd = m.feedbackInput.Update(msg)
				cmds = append(cmds, cmd)
			} else {
				// Step is pending or running
				switch msg.String() {
				case "enter":
					if !m.isExecuting && m.currentStepIdx < len(m.steps) {
						m.statusMsg = fmt.Sprintf("Executing step %d: %s...", step.Index, step.Description)
						return m.startStepExecution(step)
					}
				case "ctrl+a":
					m.autoExecute = true
					if !m.isExecuting && m.currentStepIdx < len(m.steps) {
						m.statusMsg = fmt.Sprintf("Executing step %d: %s...", step.Index, step.Description)
						return m.startStepExecution(step)
					}
				case "q":
					m.state = screenReview
					m.autoExecute = false
					m.isExecuting = false
				}
			}

		case screenFinished:
			switch msg.String() {
			case "enter":
				m.state = screenPromptInput
				m.promptInput.SetValue("")
				m.promptInput.Focus()
				m.steps = nil
				m.rawPlan = ""
				m.streamingLog = ""
				m.executionLog = ""
				m.currentStepIdx = 0
				m.autoExecute = false
				m.isExecuting = false
				return m, nil
			}

		case screenError:
			switch msg.String() {
			case "enter":
				m.state = screenPromptInput
				m.promptInput.SetValue("")
				m.promptInput.Focus()
				m.steps = nil
				m.rawPlan = ""
				m.streamingLog = ""
				m.executionLog = ""
				m.currentStepIdx = 0
				m.autoExecute = false
				m.isExecuting = false
				m.err = nil
				return m, nil
			}
		}

	case modelsLoadedMsg:
		m.models = msg
		if len(m.models) > 0 {
			m.state = screenModelSelect
		} else {
			m.state = screenError
			m.err = fmt.Errorf("no models detected in local Ollama instance. Please run 'ollama run [model]' first")
		}

	case tea.WindowSizeMsg:
		m.terminalWidth = msg.Width
		m.terminalHeight = msg.Height
		m.viewport.Width = msg.Width - 4
		m.viewport.Height = msg.Height - 16 // adjust to make space for header & status bar
		if len(m.steps) > 0 {
			wrapped := lipgloss.NewStyle().Width(m.viewport.Width).Render(FormatStepsMarkdown(m.steps))
			m.viewport.SetContent(wrapped)
		}

	case agyProgressMsg:
		m.streamingLog += string(msg)
		return m, m.listenAGYProgressCmd()

	case agyCompletedMsg:
		if msg.err != nil {
			m.state = screenError
			m.err = msg.err
			return m, nil
		}

		if m.state == screenValidating {
			reviewOutput := msg.plan
			if strings.Contains(strings.ToUpper(reviewOutput), "VALID") && !strings.Contains(reviewOutput, "=== STEP ===") {
				// The changes are valid! Go to finished screen
				m.state = screenFinished
				return m, nil
			}

			// Try to parse correction steps
			steps, err := ParseBlueprint(reviewOutput)
			if err != nil || len(steps) == 0 {
				m.state = screenFinished
				return m, nil
			}

			// Adjust indices to be consecutive with existing steps
			startIdx := len(m.steps) + 1
			for i := range steps {
				steps[i].Index = startIdx + i
				steps[i].Status = StatePending
			}

			// Append new correction steps to m.steps
			m.steps = append(m.steps, steps...)

			// Set the active step index to the first new correction step
			m.currentStepIdx = startIdx - 1
			m.state = screenExecution
			m.autoExecute = false // Let user review and run the corrections
			m.statusMsg = ""
			
			// Refresh viewport content
			wrapped := lipgloss.NewStyle().Width(m.viewport.Width).Render(FormatStepsMarkdown(m.steps))
			m.viewport.SetContent(wrapped)

			return m, nil
		}

		m.rawPlan = msg.plan
		steps, err := ParseBlueprint(m.rawPlan)
		if err != nil {
			m.state = screenError
			m.err = fmt.Errorf("failed to parse plan: %w", err)
			return m, nil
		}
		m.steps = steps
		
		// Setup viewport with plan details and wrap text
		wrapped := lipgloss.NewStyle().Width(m.viewport.Width).Render(FormatStepsMarkdown(m.steps))
		m.viewport.SetContent(wrapped)
		m.state = screenReview
		m.feedbackInput.Focus()

	case stepProgressMsg:
		m.executionLog += string(msg)
		return m, m.listenStepProgressCmd()

	case stepFinishedMsg:
		m.isExecuting = false
		step := &m.steps[msg.index]
		if msg.err != nil {
			step.Status = StateError
			step.ErrorMsg = msg.err.Error()
			m.autoExecute = false // Pause auto execution on error
			m.feedbackInput.SetValue("")
			m.feedbackInput.Focus() // Focus text input for fix instructions
		} else {
			step.Status = StateSuccess
			m.currentStepIdx++

			if m.currentStepIdx >= len(m.steps) {
				return m.startValidation()
			} else if m.autoExecute {
				// Immediately execute next step
				nextStep := &m.steps[m.currentStepIdx]
				m.statusMsg = fmt.Sprintf("Auto-executing step %d: %s...", nextStep.Index, nextStep.Description)
				return m.startStepExecution(nextStep)
			}
		}

	case errMsg:
		m.state = screenError
		m.err = msg

	default:
		m.spinner, cmd = m.spinner.Update(msg)
		cmds = append(cmds, cmd)
	}

	return m, tea.Batch(cmds...)
}

func (m Model) View() string {
	var s strings.Builder

	header := `  ___ ___  ___ ___ __  __   _   _  _ 
 | __/ _ \| _ \ __|  \/  | /_\ | \| |
 | _| (_) |   / _|| |\/| |/ _ \| .' |
 |_|  \___/|_|_\___|_|  |_/_/ \_\_|\_|
  ___  ___   ___ _  _ ___  ___ _____ ___   _ _____ ___  ___  
 / _ \| _ \ / __| || | __/ __|_   _| _ \ /_\_   _/ _ \| _ \ 
| (_) |   /| (__| __ | _|\__ \ | | |   // _ \ | || (_) |   / 
 \___/|_|_\\___|_||_|___|___/ |_| |_|_/_/ \_\|_| \___/|_|_\`

	s.WriteString(titleStyle.Render(header))
	s.WriteString("\n\n")

	switch m.state {
	case screenFolderInput:
		s.WriteString("Enter the target project directory (absolute or relative, type /exit to quit):\n\n")
		s.WriteString(m.folderInput.View())
		s.WriteString("\n\n")
		if m.folderError != "" {
			s.WriteString(errorStyle.Render(m.folderError) + "\n\n")
		}
		s.WriteString(helpStyle.Render("[Enter] Confirm Folder  |  Type /exit to quit"))

	case screenLoadingModels:
		s.WriteString(fmt.Sprintf("%s Loading local Ollama models...\n", m.spinner.View()))

	case screenModelSelect:
		s.WriteString("Select Ollama model to use for execution:\n\n")
		for i, item := range m.models {
			if i == m.selectedModel {
				s.WriteString(fmt.Sprintf("  > %s\n", selectedItemStyle.Render(item)))
			} else {
				s.WriteString(fmt.Sprintf("    %s\n", itemStyle.Render(item)))
			}
		}
		s.WriteString("\n" + helpStyle.Render("[Up/Down] Navigate  |  [Enter] Select Model"))

	case screenPromptInput:
		s.WriteString(fmt.Sprintf("Using Model: %s\n\n", selectedItemStyle.Render(m.models[m.selectedModel])))
		s.WriteString("Describe the task you want to execute (type /exit to quit):\n")
		s.WriteString(m.promptInput.View())
		s.WriteString("\n\n" + helpStyle.Render("[Enter] Submit prompt to AGY for planning  |  Type /exit to quit"))

	case screenPlanning, screenValidating:
		s.WriteString(fmt.Sprintf("%s %s\n\n", m.spinner.View(), m.statusMsg))
		
		// Header = 9 lines (art + spacing)
		// Status = 3 lines
		// Status bar = 2 lines
		// Available log lines = m.terminalHeight - 14
		logHeight := m.terminalHeight - 14
		if logHeight > 8 {
			logHeight = 8 // Cap at 8 lines to prevent bouncing
		}
		if logHeight < 3 {
			logHeight = 3
		}
		
		lines := strings.Split(m.streamingLog, "\n")
		start := 0
		if len(lines) > logHeight {
			start = len(lines) - logHeight
		}
		for _, line := range lines[start:] {
			if strings.TrimSpace(line) != "" {
				s.WriteString(fadedStyle.Render(fmt.Sprintf("  %s\n", line)))
			}
		}

	case screenReview:
		s.WriteString(fmt.Sprintf("Using Model: %s | Plan generated by AGY:\n", selectedItemStyle.Render(m.models[m.selectedModel])))
		s.WriteString(borderStyle.Render(m.viewport.View()))
		s.WriteString("\nType feedback to refine the blueprint, or approve to execute:\n")
		s.WriteString(m.feedbackInput.View())
		s.WriteString("\n\n" + helpStyle.Render("[Enter] Send feedback to AGY  |  [Ctrl+E] Approve and Execute  |  [Ctrl+C] Quit"))

	case screenExecution:
		s.WriteString(fmt.Sprintf("Executing Blueprint using %s:\n\n", selectedItemStyle.Render(m.models[m.selectedModel])))
		for _, step := range m.steps {
			var icon string
			var statusText string
			switch step.Status {
			case StateSuccess:
				icon = successStyle.Render("✔")
				statusText = successStyle.Render("Success")
			case StateRunning:
				icon = runningStyle.Render("▶")
				statusText = runningStyle.Render("Running...")
			case StateError:
				icon = errorStyle.Render("✘")
				statusText = errorStyle.Render("Error")
			default:
				icon = " "
				statusText = "Pending"
			}
			s.WriteString(fmt.Sprintf(" [%s] Step %d: %s (%s) - %s\n", icon, step.Index, step.Description, step.Type, statusText))
			if step.Status == StateError {
				s.WriteString(errorStyle.Render(fmt.Sprintf("     └ Error: %s", step.ErrorMsg)) + "\n")
			}
		}

		s.WriteString("\n")
		if m.isExecuting {
			s.WriteString(fmt.Sprintf("%s %s\n\n", m.spinner.View(), m.statusMsg))
			
			// Header = 9 lines
			// Steps list = len(m.steps) lines
			// Status = 3 lines
			// Status bar = 2 lines
			// Available log lines = m.terminalHeight - 15 - len(m.steps)
			logHeight := m.terminalHeight - 15 - len(m.steps)
			if logHeight > 5 {
				logHeight = 5 // Cap at 5 lines max to prevent terminal scroll/bounce on line wraps
			}
			if logHeight < 2 {
				logHeight = 2
			}
			
			lines := strings.Split(m.executionLog, "\n")
			start := 0
			if len(lines) > logHeight {
				start = len(lines) - logHeight
			}
			for _, line := range lines[start:] {
				if strings.TrimSpace(line) != "" {
					s.WriteString(fadedStyle.Render(fmt.Sprintf("  %s\n", line)))
				}
			}
		} else if m.currentStepIdx < len(m.steps) {
			step := m.steps[m.currentStepIdx]
			if step.Status == StateError {
				if step.Type == StepCommand {
					s.WriteString(errorStyle.Render("Build check failed! Press [Enter] to auto-fix errors via Ollama, or type instructions:") + "\n")
				} else {
					s.WriteString(errorStyle.Render("Step failed! Give instructions to correct and retry:") + "\n")
				}
				s.WriteString(m.feedbackInput.View())
				s.WriteString("\n\n" + helpStyle.Render("[Enter] Apply & Retry  |  [Ctrl+S] Skip step  |  [Ctrl+R] Retry raw  |  [Ctrl+Q] Back to Review"))
			} else {
				s.WriteString(fmt.Sprintf("Ready to run Step %d (%s: %s)\n", step.Index, step.Type, step.Description))
				s.WriteString(helpStyle.Render("[Enter] Execute step  |  [Ctrl+A] Execute all remaining  |  [q] Back to Review"))
			}
		}

	case screenFinished:
		s.WriteString(successStyle.Render("🎉 Blueprint execution finished successfully!") + "\n\n")
		s.WriteString("All files updated and commands executed without errors.\n")
		s.WriteString(helpStyle.Render("Press [Enter] to start a new task, or type /exit in the next prompt to quit."))

	case screenError:
		s.WriteString(errorStyle.Render("❌ Error occurred:") + "\n\n")
		s.WriteString(m.err.Error())
		s.WriteString("\n\n" + helpStyle.Render("Press [Enter] to go back to the prompt, or type /exit to quit."))
	}

	// Count how many newlines are in our rendered content so far
	renderedContent := s.String()
	lineCount := strings.Count(renderedContent, "\n")
	
	// If the terminal height is set and the content is shorter than the terminal,
	// pad it with newlines so the status bar stays fixed at the absolute bottom
	padHeight := m.terminalHeight - lineCount - 2 // subtract 2 for status bar lines
	if padHeight > 0 {
		s.WriteString(strings.Repeat("\n", padHeight))
	}

	s.WriteString(m.renderStatusBar(m.terminalWidth))
	return s.String()
}

func (m Model) renderStatusBar(width int) string {
	modelName := "N/A"
	if m.state > screenModelSelect && len(m.models) > 0 && m.selectedModel < len(m.models) {
		modelName = m.models[m.selectedModel]
	}

	leftText := fmt.Sprintf(" 📂 %s ", m.cwd)
	if m.state == screenFolderInput {
		leftText = " 📂 [Selecting Directory...] "
	}

	rightText := fmt.Sprintf(" 🤖 %s ", modelName)

	leftStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#f8f8f2")).
		Background(lipgloss.Color("#44475a")).
		Bold(true)

	rightStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#f8f8f2")).
		Background(lipgloss.Color("#6272a4")).
		Bold(true)

	barStyle := lipgloss.NewStyle().
		Background(lipgloss.Color("#282a36"))

	l := leftStyle.Render(leftText)
	r := rightStyle.Render(rightText)

	lWidth := lipgloss.Width(l)
	rWidth := lipgloss.Width(r)
	spaceWidth := width - lWidth - rWidth
	if spaceWidth < 0 {
		spaceWidth = 0
	}

	spaces := barStyle.Render(strings.Repeat(" ", spaceWidth))
	return "\n" + l + spaces + r
}

// Commands
func (m Model) loadModelsCmd() tea.Msg {
	models, err := FetchOllamaModels()
	if err != nil {
		return errMsg(err)
	}
	return modelsLoadedMsg(models)
}

func (m Model) startPlanning(prompt string, isContinue bool) (Model, tea.Cmd) {
	m.state = screenPlanning
	m.streamingLog = ""
	m.agyRunner = newAGYRunner()

	if isContinue {
		m.statusMsg = "Refining plan with AGY based on feedback..."
	} else {
		m.statusMsg = "Running AGY CLI to analyze the workspace and create the plan..."
	}

	go func() {
		var plan string
		var err error
		if isContinue {
			refinementPrompt := fmt.Sprintf(`The user provided feedback to refine the blueprint. Please update the step-by-step blueprint accordingly. 

User Feedback:
%s

Remember to follow the exact "=== STEP ===" structure for all steps, and do not execute any commands or write files yourself. Return ONLY the refined steps.
Crucial: Ensure that the final step remains a verification 'command' step that compiles, lints, and tests the project (based on the project type you identified).`, prompt)
			plan, err = RunAGY(m.cwd, refinementPrompt, true, m.agyRunner.progress)
		} else {
			planningPrompt := BuildInitialPlanningPrompt(prompt)
			plan, err = RunAGY(m.cwd, planningPrompt, false, m.agyRunner.progress)
		}
		m.agyRunner.result <- agyResult{plan: plan, err: err}
	}()

	return m, m.listenAGYProgressCmd()
}

func (m Model) listenAGYProgressCmd() tea.Cmd {
	return func() tea.Msg {
		select {
		case progress, ok := <-m.agyRunner.progress:
			if ok {
				return agyProgressMsg(progress)
			}
			res := <-m.agyRunner.result
			return agyCompletedMsg(res)
		}
	}
}

func (m Model) startValidation() (Model, tea.Cmd) {
	m.state = screenValidating
	m.streamingLog = ""
	m.statusMsg = "Running cloud code review validation on implemented changes..."
	m.agyRunner = newAGYRunner()

	go func() {
		// 1. Get git diff
		diffCmd := exec.Command("git", "diff")
		diffCmd.Dir = m.cwd
		diffOut, err := diffCmd.CombinedOutput()
		if err != nil {
			// If git fails, fallback to finished
			m.agyRunner.result <- agyResult{plan: "VALID", err: nil}
			return
		}

		diffStr := string(diffOut)
		if strings.TrimSpace(diffStr) == "" {
			// No changes made? Return VALID
			m.agyRunner.result <- agyResult{plan: "VALID", err: nil}
			return
		}

		// 2. Build review prompt
		reviewPrompt := fmt.Sprintf(`You are the Senior Code Reviewer.
The junior developer (local model) has implemented the requested task. Here is the git diff of the modifications made in the workspace:
---
%s
---

Please review this diff for any syntax errors, compile errors, accidental deletions, naming mismatches, or incorrect modifications.
- If everything is correct and complete, output ONLY the word "VALID" (with no other text).
- If there are errors, output one or more correction steps formatted EXACTLY as follows:
=== STEP ===
Type: modify
Path: [relative path to the file]
Description: [brief description of the fix]

TargetBlock:
%s
[Specify the EXACT block of lines to look for in the file so the worker knows where to edit.]
%s

Instructions:
%s
[Provide the exact instructions or replacement block to fix the bug.]
%s
=== END ===

Do not execute any commands or write files yourself. Return ONLY "VALID" or the correction steps in the format above.`, diffStr, "```", "```", "```", "```")

		// 3. Run AGY to review
		reviewOut, runErr := RunAGY(m.cwd, reviewPrompt, true, m.agyRunner.progress)
		m.agyRunner.result <- agyResult{plan: reviewOut, err: runErr}
	}()

	return m, m.listenAGYProgressCmd()
}

func (m Model) startStepExecution(step *Step) (Model, tea.Cmd) {
	m.isExecuting = true
	m.executionLog = ""
	m.stepRunner = newStepRunner()
	step.Status = StateRunning

	go func() {
		model := m.models[m.selectedModel]
		err := ExecuteStep(m.cwd, model, step, m.stepRunner.progress)
		m.stepRunner.result <- stepFinishedMsg{
			index: step.Index - 1,
			err:   err,
		}
	}()

	return m, m.listenStepProgressCmd()
}

func (m Model) listenStepProgressCmd() tea.Cmd {
	return func() tea.Msg {
		select {
		case progress, ok := <-m.stepRunner.progress:
			if ok {
				return stepProgressMsg(progress)
			}
			res := <-m.stepRunner.result
			return res
		}
	}
}
