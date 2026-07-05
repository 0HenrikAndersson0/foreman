package main

import (
	"bytes"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
)

const agyPath = "/Users/henrikandersson/.local/bin/agy"

// GetBlueprintRulesPrompt returns the common blueprint formatting and execution rules.
func GetBlueprintRulesPrompt() string {
	return fmt.Sprintf(`Guidelines for the Blueprint:
1. Break the task down into a sequential list of steps.
2. The instructions must be so precise and unambiguous that a junior developer (or a local code execution model) can execute them step-by-step without making any design choices.
3. Every step must be categorized as either 'create', 'modify', or 'command'.
4. Format each step EXACTLY as follows:

=== STEP ===
Type: [create | modify | command]
Path: [relative path to the file, or N/A]
Description: [brief description of the step]

TargetBlock:
%s
[For 'modify' steps, specify the EXACT block of lines to look for in the file so the worker knows where to edit. Keep it small. For 'create' or 'command' steps, leave this block empty.]
%s

Instructions:
%s
[For 'create' steps, provide the ENTIRE file contents.
 For 'modify' steps, provide the instructions or the exact replacement block.
 for 'command' steps, provide the exact shell command to run.]
%s
=== END ===

5. Crucially, when generating instructions for 'modify' steps, explicitly warn the implementing model that it MUST NOT remove, truncate, or omit any code that is not related to the requested change. It must preserve the entire file context, helper functions, and imports exactly intact.

Do not execute any commands or make any file edits yourself. Output ONLY the step-by-step blueprint using the format above. Do not include any introductory or concluding remarks.`, "```", "```", "```", "```")
}

// BuildInitialPlanningPrompt constructs the prompt to send to AGY for the initial plan.
func BuildInitialPlanningPrompt(userRequest string) string {
	return fmt.Sprintf(`You are the Foreman Architect. Your task is to investigate the workspace and produce a highly detailed, step-by-step implementation guide (the "Blueprint") for the following request:

---
%s
---

%s`, userRequest, GetBlueprintRulesPrompt())
}

// RunAGY executes the AGY CLI to generate or refine the plan, streaming stdout/stderr chunks to progressChan.
func RunAGY(cwd string, prompt string, isContinue bool, progressChan chan string) (string, error) {
	args := []string{}
	if isContinue {
		args = append(args, "--continue")
	}
	args = append(args, "--print", prompt)

	cmd := exec.Command(agyPath, args...)
	cmd.Dir = cwd

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return "", fmt.Errorf("failed to get stdout pipe: %w", err)
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		return "", fmt.Errorf("failed to get stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("failed to start AGY CLI: %w", err)
	}

	var stdoutBuf bytes.Buffer

	// Read stdout and stderr concurrently
	doneChan := make(chan struct{}, 2)

	go func() {
		buf := make([]byte, 1024)
		for {
			n, err := stdoutPipe.Read(buf)
			if n > 0 {
				chunk := string(buf[:n])
				stdoutBuf.WriteString(chunk)
				progressChan <- chunk
			}
			if err != nil {
				break
			}
		}
		doneChan <- struct{}{}
	}()

	go func() {
		buf := make([]byte, 1024)
		for {
			n, err := stderrPipe.Read(buf)
			if n > 0 {
				chunk := string(buf[:n])
				progressChan <- chunk
			}
			if err != nil {
				break
			}
		}
		doneChan <- struct{}{}
	}()

	// Wait for readers to finish and close channel
	go func() {
		<-doneChan
		<-doneChan
		close(progressChan)
	}()

	err = cmd.Wait()
	if err != nil {
		return "", fmt.Errorf("AGY CLI execution failed: %w", err)
	}

	return stdoutBuf.String(), nil
}

// ParseBlueprint parses the markdown output from AGY into structured Step objects.
func ParseBlueprint(blueprint string) ([]Step, error) {
	var steps []Step

	// Split by === STEP === delimiter
	rawSteps := strings.Split(blueprint, "=== STEP ===")
	if len(rawSteps) <= 1 {
		return nil, fmt.Errorf("no steps found in blueprint output. Ensure format follows '=== STEP ==='")
	}

	stepIndex := 1
	for _, rawStep := range rawSteps[1:] {
		// Cut off anything after === END === in this step
		endIdx := strings.Index(rawStep, "=== END ===")
		if endIdx != -1 {
			rawStep = rawStep[:endIdx]
		}

		lines := strings.Split(rawStep, "\n")
		var step Step
		step.Index = stepIndex
		step.Status = StatePending

		var parsingTargetBlock, parsingInstructions bool
		var targetLines, instructionLines []string

		for _, line := range lines {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "Type:") {
				step.Type = StepType(strings.ToLower(strings.TrimSpace(strings.TrimPrefix(trimmed, "Type:"))))
			} else if strings.HasPrefix(trimmed, "Path:") {
				step.Path = strings.TrimSpace(strings.TrimPrefix(trimmed, "Path:"))
			} else if strings.HasPrefix(trimmed, "Description:") {
				step.Description = strings.TrimSpace(strings.TrimPrefix(trimmed, "Description:"))
			} else if strings.HasPrefix(trimmed, "TargetBlock:") {
				parsingTargetBlock = true
				parsingInstructions = false
				continue
			} else if strings.HasPrefix(trimmed, "Instructions:") {
				parsingTargetBlock = false
				parsingInstructions = true
				continue
			}

			if parsingTargetBlock {
				targetLines = append(targetLines, line)
			} else if parsingInstructions {
				instructionLines = append(instructionLines, line)
			}
		}

		// Extract content inside code blocks
		step.TargetBlock = extractCodeBlock(strings.Join(targetLines, "\n"))
		step.Instructions = extractCodeBlock(strings.Join(instructionLines, "\n"))

		// Basic validation
		if step.Type != StepCreate && step.Type != StepModify && step.Type != StepCommand {
			// If not matching, try regex to find it
			if strings.Contains(string(step.Type), "create") {
				step.Type = StepCreate
			} else if strings.Contains(string(step.Type), "modify") {
				step.Type = StepModify
			} else if strings.Contains(string(step.Type), "command") {
				step.Type = StepCommand
			} else {
				step.Type = StepCommand // default fallback
			}
		}

		if step.Type == StepCommand {
			step.Command = step.Instructions
		}

		steps = append(steps, step)
		stepIndex++
	}

	return steps, nil
}

// extractCodeBlock parses text between ``` [lang] and ```
func extractCodeBlock(text string) string {
	text = strings.TrimSpace(text)
	re := regexp.MustCompile("(?s)^```[a-zA-Z0-9_-]*\\n(.*)\\n```$")
	match := re.FindStringSubmatch(text)
	if len(match) > 1 {
		return match[1]
	}

	// Fallback in case of inline code backticks or formatting variants
	reFallback := regexp.MustCompile("(?s)```[a-zA-Z0-9_-]*\\n(.*)```")
	matchFallback := reFallback.FindStringSubmatch(text)
	if len(matchFallback) > 1 {
		return strings.TrimSpace(matchFallback[1])
	}

	return strings.TrimSpace(text)
}

// FormatStepsMarkdown returns a human-readable markdown representation of the steps
func FormatStepsMarkdown(steps []Step) string {
	var sb strings.Builder
	for _, s := range steps {
		sb.WriteString(fmt.Sprintf("### Step %d: %s (%s)\n", s.Index, s.Description, s.Type))
		if s.Path != "" && s.Path != "N/A" {
			sb.WriteString(fmt.Sprintf("**File**: `%s`\n", s.Path))
		}
		if s.Type == StepModify && s.TargetBlock != "" {
			sb.WriteString("**Target lines to modify**:\n```\n" + s.TargetBlock + "\n```\n")
		}
		if s.Type == StepCommand {
			sb.WriteString("**Command**:\n```bash\n" + s.Command + "\n```\n")
		} else {
			sb.WriteString("**Instructions/Code**:\n```\n" + s.Instructions + "\n```\n")
		}
		sb.WriteString("\n---\n\n")
	}
	return sb.String()
}
