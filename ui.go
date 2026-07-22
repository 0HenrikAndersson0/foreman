package main

import (
	"fmt"
	"os"
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
	screenCloudAgentSelect
	screenLoadingModels
	screenModelSelect
	screenPromptInput
	screenPlanning
	screenReview
	screenExecution
	screenFinished
	screenError
	screenSessionPrompt
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
	currentStepIdx        int
	autoExecute           bool
	isExecuting           bool
	statusMsg             string
	terminalWidth         int
	terminalHeight        int
	continueSession       bool
	selectedSessionOption int // 0: continue, 1: new
	cloudAgents           []string
	selectedCloudAgent    int
	showDetailedBlueprint bool
	codeReviewLoopCount   int
	spinnerTickCount      int
	quoteIndex            int
}

var aiQuotes = []string{
	"Releasing the robots...",
	"Connecting to skynet...",
	"Brewing digital coffee...",
	"Compiling the matrix...",
	"Waking up the cloud architects...",
	"Consulting the silicon oracle...",
	"Reticulating splines...",
	"Generating 10x code...",
	"Unleashing the algorithms...",
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
		state:                 screenFolderInput,
		cwd:                   cwd,
		folderInput:           fo,
		promptInput:           pi,
		feedbackInput:         fi,
		spinner:               s,
		viewport:              viewport.New(80, 20),
		terminalWidth:         80, // default fallback
		terminalHeight:        24, // default fallback
		cloudAgents:           []string{"AGY (Google Antigravity CLI)", "Claude Code (Anthropic CLI)", "GitHub Copilot (GitHub CLI)"},
		selectedCloudAgent:    0,
		showDetailedBlueprint: false,
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
					m.state = screenCloudAgentSelect
					return m, nil
				}
			}
			m.folderInput, cmd = m.folderInput.Update(msg)
			cmds = append(cmds, cmd)

		case screenCloudAgentSelect:
			switch msg.String() {
			case "up", "k":
				if m.selectedCloudAgent > 0 {
					m.selectedCloudAgent--
				}
				return m, nil
			case "down", "j":
				if m.selectedCloudAgent < len(m.cloudAgents)-1 {
					m.selectedCloudAgent++
				}
				return m, nil
			case "enter":
				m.state = screenLoadingModels
				return m, m.loadModelsCmd
			}

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
					m, cmd = m.startPlanning(prompt, m.continueSession, false)
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
			case "tab":
				m.showDetailedBlueprint = !m.showDetailedBlueprint
				var content string
				if m.showDetailedBlueprint {
					content = FormatStepsMarkdown(m.steps)
				} else {
					content = FormatStepsSummaryMarkdown(m.steps)
				}
				wrapped := lipgloss.NewStyle().Width(m.viewport.Width).Render(content)
				m.viewport.SetContent(wrapped)
				return m, nil
			case "enter":
				feedback := m.feedbackInput.Value()
				if strings.TrimSpace(feedback) == "/exit" {
					return m, tea.Quit
				}
				if strings.TrimSpace(feedback) != "" {
					m.feedbackInput.SetValue("")
					m, cmd = m.startPlanning(feedback, true, true)
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
						m.state = screenFinished
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
					if !m.isExecuting {
						m.state = screenReview
						m.autoExecute = false
						m.isExecuting = false
					}
				}
			}

		case screenFinished, screenError:
			switch msg.String() {
			case "enter":
				m.state = screenSessionPrompt
				m.selectedSessionOption = 0
				return m, nil
			}

		case screenSessionPrompt:
			switch msg.String() {
			case "up", "down", "j", "k", "tab":
				if m.selectedSessionOption == 0 {
					m.selectedSessionOption = 1
				} else {
					m.selectedSessionOption = 0
				}
				return m, nil
			case "enter":
				if m.selectedSessionOption == 0 {
					m.continueSession = true
				} else {
					m.continueSession = false
				}
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
			var content string
			if m.showDetailedBlueprint {
				content = FormatStepsMarkdown(m.steps)
			} else {
				content = FormatStepsSummaryMarkdown(m.steps)
			}
			wrapped := lipgloss.NewStyle().Width(m.viewport.Width).Render(content)
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

		m.rawPlan = msg.plan
		m.codeReviewLoopCount = 0
		LogDebug(m.cwd, "--- RAW BLUEPRINT RECEIVED ---\n%s\n------------------------------", m.rawPlan)
		steps, err := ParseBlueprint(m.rawPlan)
		if err != nil {
			m.state = screenError
			m.err = fmt.Errorf("failed to parse plan: %w", err)
			return m, nil
		}

		steps = filterRedundantSteps(steps)

		// Programmatically append Code Review and Build/Lint/Test steps!
		steps = append(steps, Step{
			Index:       len(steps) + 1,
			Type:        StepCommand,
			Command:     "foreman-code-review",
			Description: "Cloud Code Review Validation",
			Status:      StatePending,
		})
		steps = append(steps, Step{
			Index:       len(steps) + 1,
			Type:        StepCommand,
			Command:     "foreman-build-lint-test",
			Description: "Project Build, Lint & Test Verification",
			Status:      StatePending,
		})

		m.steps = steps
		m.showDetailedBlueprint = false
		
		// Setup viewport with plan details and wrap text
		wrapped := lipgloss.NewStyle().Width(m.viewport.Width).Render(FormatStepsSummaryMarkdown(m.steps))
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
			
			if step.Command == "foreman-code-review" {
				steps, parseErr := ParseBlueprint(m.executionLog)
				if parseErr == nil && len(steps) > 0 {
					// We parsed correction steps!
					m.codeReviewLoopCount++
					if m.codeReviewLoopCount > 2 {
						step.ErrorMsg = "Code review failed 3 times. Please review the diff and provide manual guidance."
						m.feedbackInput.SetValue("")
						m.feedbackInput.Focus()
						return m, nil
					}
					// We will insert them right BEFORE the current code-review step.
					// The index of the current code-review step is m.currentStepIdx.
					
					var newSteps []Step
					
					// 1. Copy steps before the current code-review step
					newSteps = append(newSteps, m.steps[:m.currentStepIdx]...)
					
					// 2. Insert correction steps
					startIdx := m.currentStepIdx + 1
					for i := range steps {
						steps[i].Index = startIdx + i
						steps[i].Status = StatePending
						newSteps = append(newSteps, steps[i])
					}
					
					// 3. Copy the remaining steps (including the code-review step and build step), shifting their indices!
					shift := len(steps)
					for i := m.currentStepIdx; i < len(m.steps); i++ {
						shiftedStep := m.steps[i]
						shiftedStep.Index += shift
						// Reset code-review and build steps to pending so they run again!
						shiftedStep.Status = StatePending
						shiftedStep.ErrorMsg = ""
						shiftedStep.Feedback = ""
						newSteps = append(newSteps, shiftedStep)
					}
					
					m.steps = newSteps
					m.showDetailedBlueprint = false
					
					m.feedbackInput.SetValue("")
					m.feedbackInput.Blur()
					
					// Refresh viewport content
					wrapped := lipgloss.NewStyle().Width(m.viewport.Width).Render(FormatStepsSummaryMarkdown(m.steps))
					m.viewport.SetContent(wrapped)
					
					return m, nil
				}
			}

			m.feedbackInput.SetValue("")
			m.feedbackInput.Focus() // Focus text input for manual fix instructions
		} else {
			step.Status = StateSuccess
			m.currentStepIdx++

			if m.currentStepIdx >= len(m.steps) {
				m.state = screenFinished
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
		switch msg.(type) {
		case spinner.TickMsg:
			if m.state == screenPlanning {
				m.spinnerTickCount++
				// spinner.Dot ticks every 100ms (10 frames/sec), so 300 ticks = 30 seconds
				if m.spinnerTickCount%300 == 0 {
					m.quoteIndex = (m.quoteIndex + 1) % len(aiQuotes)
					m.statusMsg = aiQuotes[m.quoteIndex]
				}
			}
		}
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

	case screenCloudAgentSelect:
		s.WriteString("Select Cloud Agent (Architect) to use for planning & review:\n\n")
		for i, item := range m.cloudAgents {
			if i == m.selectedCloudAgent {
				s.WriteString(fmt.Sprintf("  > %s\n", selectedItemStyle.Render(item)))
			} else {
				s.WriteString(fmt.Sprintf("    %s\n", itemStyle.Render(item)))
			}
		}
		s.WriteString("\n" + helpStyle.Render("[Up/Down] Navigate  |  [Enter] Select Cloud Agent"))

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
		s.WriteString("\n\n" + helpStyle.Render("[Enter] Submit prompt for planning  |  Type /exit to quit"))

	case screenPlanning:
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
		cloudAgentName := m.cloudAgents[m.selectedCloudAgent]
		s.WriteString(fmt.Sprintf("Using Model: %s | Plan generated by %s:\n", selectedItemStyle.Render(m.models[m.selectedModel]), cloudAgentName))
		s.WriteString(borderStyle.Render(m.viewport.View()))
		s.WriteString("\nType feedback to refine the blueprint, or approve to execute:\n")
		s.WriteString(m.feedbackInput.View())
		s.WriteString("\n\n" + helpStyle.Render("[Enter] Send feedback  |  [Tab] Toggle Detailed View  |  [Ctrl+E] Approve and Execute  |  [Ctrl+C] Quit"))

	case screenExecution:
		cloudAgentName := m.cloudAgents[m.selectedCloudAgent]
		s.WriteString(fmt.Sprintf("Executing Blueprint using %s (Architect: %s):\n\n", selectedItemStyle.Render(m.models[m.selectedModel]), cloudAgentName))
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
		s.WriteString(helpStyle.Render("Press [Enter] to choose session mode & start next task"))

	case screenError:
		s.WriteString(errorStyle.Render("❌ Error occurred:") + "\n\n")
		s.WriteString(m.err.Error())
		s.WriteString("\n\n" + helpStyle.Render("Press [Enter] to choose session mode, or type /exit to quit."))

	case screenSessionPrompt:
		s.WriteString("🔄 Task execution finished!\n\n")
		s.WriteString("Would you like to continue in the current session or start a new one?\n")
		s.WriteString("Continuing keeps the previous history and files in memory for the next task.\n\n")
		for i, opt := range []string{"Continue current session (keeps context & history)", "Start a new session (fresh start)"} {
			if i == m.selectedSessionOption {
				s.WriteString(fmt.Sprintf("  > %s\n", selectedItemStyle.Render(opt)))
			} else {
				s.WriteString(fmt.Sprintf("    %s\n", itemStyle.Render(opt)))
			}
		}
		s.WriteString("\n" + helpStyle.Render("[Up/Down] Navigate  |  [Enter] Select Option"))
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

	cloudAgentName := "N/A"
	if m.state > screenCloudAgentSelect && len(m.cloudAgents) > 0 && m.selectedCloudAgent < len(m.cloudAgents) {
		parts := strings.Split(m.cloudAgents[m.selectedCloudAgent], " (")
		cloudAgentName = parts[0]
	}

	left := fmt.Sprintf(" 📂 %s", m.cwd)
	right := fmt.Sprintf("☁️ %s | 🤖 %s ", cloudAgentName, modelName)

	if m.state == screenFolderInput {
		left = " 📂 [Selecting Directory...] "
	}

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

	l := leftStyle.Render(left)
	r := rightStyle.Render(right)

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

func (m Model) startPlanning(prompt string, isContinue bool, isRefinement bool) (Model, tea.Cmd) {
	m.state = screenPlanning
	m.streamingLog = ""
	m.agyRunner = newAGYRunner()
	m.spinnerTickCount = 0
	m.quoteIndex = 0

	cloudAgentName := m.cloudAgents[m.selectedCloudAgent]
	if isRefinement {
		m.statusMsg = fmt.Sprintf("Refining plan with %s based on feedback...", cloudAgentName)
	} else if isContinue {
		m.statusMsg = fmt.Sprintf("Running %s in continue mode for the new task...", cloudAgentName)
	} else {
		m.statusMsg = fmt.Sprintf("Running %s to analyze the workspace and create the plan...", cloudAgentName)
	}

	go func() {
		var plan string
		var err error
		if isRefinement {
			refinementPrompt := fmt.Sprintf(`The user provided feedback to refine the blueprint. Please update the step-by-step blueprint accordingly. 

User Feedback:
%s

Remember to follow the exact "=== STEP ===" structure for all steps, and do not execute any commands or write files yourself. Return ONLY the refined steps.`, prompt)
			plan, err = RunCloudAgent(cloudAgentName, m.cwd, refinementPrompt, true, m.agyRunner.progress)
		} else {
			var planningPrompt string
			if isContinue {
				planningPrompt = fmt.Sprintf(`The user has a new task request to run in the current session. Please analyze the workspace and previous actions, and output a new step-by-step blueprint.

User Request:
%s

%s`, prompt, GetBlueprintRulesPrompt())
			} else {
				planningPrompt = BuildInitialPlanningPrompt(prompt)
			}
			plan, err = RunCloudAgent(cloudAgentName, m.cwd, planningPrompt, isContinue, m.agyRunner.progress)
		}
		close(m.agyRunner.progress)
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

func (m Model) startStepExecution(step *Step) (Model, tea.Cmd) {
	m.isExecuting = true
	m.executionLog = ""
	m.stepRunner = newStepRunner()
	step.Status = StateRunning

	go func() {
		cloudAgent := m.cloudAgents[m.selectedCloudAgent]
		model := m.models[m.selectedModel]
		err := ExecuteStep(m.cwd, cloudAgent, model, step, m.stepRunner.progress)
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

func filterRedundantSteps(steps []Step) []Step {
	patterns := []string{
		"build",
		"lint",
		"test",
		"compile",
		"npm test",
		"npm run build",
		"npm run lint",
		"go test",
		"go build",
		"cargo build",
		"cargo test",
	}

	var newSteps []Step
	for _, s := range steps {
		isRedundant := false
		if s.Type == StepCommand {
			cmdLower := strings.ToLower(s.Command)
			descLower := strings.ToLower(s.Description)
			for _, pat := range patterns {
				if strings.Contains(cmdLower, pat) || strings.Contains(descLower, pat) {
					isRedundant = true
					break
				}
			}
		}
		if !isRedundant {
			newSteps = append(newSteps, s)
		}
	}

	// Re-index remaining steps to be consecutive
	for i := range newSteps {
		newSteps[i].Index = i + 1
	}

	return newSteps
}
