package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// ExecuteStep runs a single step in the blueprint, streaming output to progressChan.
func ExecuteStep(cwd string, cloudAgent string, model string, step *Step, progressChan chan string) error {
	step.Status = StateRunning
	defer close(progressChan)

	switch step.Type {
	case StepCreate:
		return executeCreate(cwd, step, progressChan)
	case StepModify:
		return executeModify(cwd, model, step, progressChan)
	case StepCommand:
		if step.Command == "foreman-code-review" {
			return executeForemanCodeReview(cwd, cloudAgent, step, progressChan)
		}
		if step.Command == "foreman-build-lint-test" {
			return executeForemanBuildLintTest(cwd, step, progressChan)
		}
		return executeCommand(cwd, step, progressChan)
	default:
		step.Status = StateError
		step.ErrorMsg = fmt.Sprintf("unknown step type: %s", step.Type)
		return fmt.Errorf(step.ErrorMsg)
	}
}

// executeCreate writes a new file to disk.
func executeCreate(cwd string, step *Step, progressChan chan string) error {
	progressChan <- fmt.Sprintf("Creating new file: %s...\n", step.Path)
	fullPath := filepath.Join(cwd, step.Path)

	// Ensure the parent directory exists
	dir := filepath.Dir(fullPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		step.Status = StateError
		step.ErrorMsg = fmt.Sprintf("failed to create directory %s: %s", dir, err.Error())
		return err
	}

	// Write the file
	err := os.WriteFile(fullPath, []byte(step.Instructions), 0644)
	if err != nil {
		step.Status = StateError
		step.ErrorMsg = fmt.Sprintf("failed to write file %s: %s", step.Path, err.Error())
		return err
	}

	progressChan <- "File created successfully.\n"
	step.Status = StateSuccess
	return nil
}

// executeModify reads the existing file, prompts Ollama to apply the change, and overwrites the file.
func executeModify(cwd string, model string, step *Step, progressChan chan string) error {
	progressChan <- fmt.Sprintf("Modifying file: %s using Ollama (%s)...\n", step.Path, model)
	fullPath := filepath.Join(cwd, step.Path)

	// Read existing file
	currentBytes, err := os.ReadFile(fullPath)
	if err != nil {
		step.Status = StateError
		step.ErrorMsg = fmt.Sprintf("failed to read file %s: %s", step.Path, err.Error())
		return err
	}
	currentContent := strings.ReplaceAll(string(currentBytes), "\r\n", "\n")
	targetBlock := strings.ReplaceAll(step.TargetBlock, "\r\n", "\n")

	useBlockReplacement := false
	if targetBlock != "" && strings.Contains(currentContent, targetBlock) {
		useBlockReplacement = true
	}

	var prompt string
	if useBlockReplacement {
		markedContent := strings.Replace(currentContent, targetBlock, fmt.Sprintf("<<<<<<< ORIGINAL BLOCK (DO NOT EDIT OUTSIDE THIS BLOCK)\n%s\n=======\n[THE NEW CODE FOR THIS BLOCK WILL BE RENDERED HERE ACCORDING TO THE INSTRUCTIONS BELOW]\n>>>>>>>", targetBlock), 1)

		if step.Feedback != "" {
			prompt = fmt.Sprintf(`You are a developer rewriting a specific block of code inside a file to fix a previous attempt.
We have marked the target block to replace using <<<<<<< and >>>>>>> markers in the file below.

Here is the entire file for context:
---
%s
---

Your task is to rewrite the ORIGINAL BLOCK of code inside the markers to satisfy these instructions:
---
Instructions:
%s
---
User Correction Feedback:
%s
---

You MUST output ONLY the new replacement code block that goes between the markers. Do NOT output any other parts of the file. Do NOT write any explanation, comments, or intro/outro text. Preserve the indentation level of the original block.
You MUST output the final code inside a single markdown code block starting with %s and ending with %s.`, 
				markedContent, step.Instructions, step.Feedback, "```", "```")
		} else {
			prompt = fmt.Sprintf(`You are a developer rewriting a specific block of code inside a file.
We have marked the target block to replace using <<<<<<< and >>>>>>> markers in the file below.

Here is the entire file for context:
---
%s
---

Your task is to rewrite the ORIGINAL BLOCK of code inside the markers to satisfy these instructions:
---
Instructions:
%s
---

You MUST output ONLY the new replacement code block that goes between the markers. Do NOT output any other parts of the file. Do NOT write any explanation, comments, or intro/outro text. Preserve the indentation level of the original block.
You MUST output the final code inside a single markdown code block starting with %s and ending with %s.`, 
				markedContent, step.Instructions, "```", "```")
		}
	} else {
		if step.Feedback != "" {
			prompt = fmt.Sprintf(`You are a developer fixing a previous code modification attempt that failed or had errors.
Your task is to modify the file according to these instructions:
---
Instructions:
%s
---
User Correction Feedback:
%s
---
Target Block to Replace (if specified):
%s
---

CRITICAL REQUIREMENT: You MUST output the ENTIRE file content, including all unchanged parts, imports, functions, and comments. You MUST NOT truncate the code, use placeholders, or replace code blocks with comments like '// ... rest of code unchanged ...'. Every line of the original file that is not directly modified must remain exactly intact.

Here is the entire current content of the file:
%s

Apply the correction and output the ENTIRE updated content of the file.
Do not write any explanation, comments, or intro/outro text.
You MUST output the final code inside a single markdown code block starting with %s and ending with %s.`, 
				step.Instructions, step.Feedback, targetBlock, currentContent, "```", "```")
		} else {
			prompt = fmt.Sprintf(`You are a developer implementing a code modification in an existing file.
Your task is to modify the file according to these instructions:
---
Instructions:
%s
---
Target Block to Replace (if specified):
%s
---

CRITICAL REQUIREMENT: You MUST output the ENTIRE file content, including all unchanged parts, imports, functions, and comments. You MUST NOT truncate the code, use placeholders, or replace code blocks with comments like '// ... rest of code unchanged ...'. Every line of the original file that is not directly modified must remain exactly intact.

Here is the entire current content of the file:
%s

Apply the modification and output the ENTIRE updated content of the file.
Do not write any explanation, comments, or intro/outro text.
You MUST output the final code inside a single markdown code block starting with %s and ending with %s.`, 
				step.Instructions, targetBlock, currentContent, "```", "```")
		}
	}

	// Call Ollama and stream the output to progressChan
	updatedContentRaw, err := GenerateCode(model, prompt, progressChan)
	if err != nil {
		step.Status = StateError
		step.ErrorMsg = fmt.Sprintf("Ollama generation failed: %s", err.Error())
		return err
	}

	// Extract the code block from Ollama's response
	updatedContent := extractCodeBlock(updatedContentRaw)
	if updatedContent == "" || updatedContent == updatedContentRaw {
		if len(updatedContentRaw) > 0 {
			updatedContent = updatedContentRaw
		} else {
			step.Status = StateError
			step.ErrorMsg = "Ollama returned an empty response"
			return fmt.Errorf(step.ErrorMsg)
		}
	}

	var finalContent string
	if useBlockReplacement {
		// Clean the replacement of any markers the model might have output
		cleanReplacement := cleanReplacementContent(updatedContent)
		finalContent = strings.Replace(currentContent, targetBlock, cleanReplacement, 1)
	} else {
		finalContent = updatedContent
	}

	// Write the modified content back
	err = os.WriteFile(fullPath, []byte(finalContent), 0644)
	if err != nil {
		step.Status = StateError
		step.ErrorMsg = fmt.Sprintf("failed to write file %s: %s", step.Path, err.Error())
		return err
	}

	progressChan <- "\nChanges successfully applied to file.\n"
	step.Status = StateSuccess
	return nil
}

// executeCommand runs a command in the shell, streaming output to progressChan.
func executeCommand(cwd string, step *Step, progressChan chan string) error {
	progressChan <- fmt.Sprintf("Running shell command: %s...\n\n", step.Command)
	
	cmd := exec.Command("sh", "-c", step.Command)
	cmd.Dir = cwd

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		step.Status = StateError
		step.ErrorMsg = fmt.Sprintf("failed to get stdout pipe: %s", err.Error())
		return err
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		step.Status = StateError
		step.ErrorMsg = fmt.Sprintf("failed to get stderr pipe: %s", err.Error())
		return err
	}

	if err := cmd.Start(); err != nil {
		step.Status = StateError
		step.ErrorMsg = fmt.Sprintf("failed to start command: %s", err.Error())
		return err
	}

	doneChan := make(chan struct{}, 2)

	// Stream stdout
	go func() {
		buf := make([]byte, 512)
		for {
			n, err := stdoutPipe.Read(buf)
			if n > 0 {
				progressChan <- string(buf[:n])
			}
			if err != nil {
				break
			}
		}
		doneChan <- struct{}{}
	}()

	// Stream stderr
	go func() {
		buf := make([]byte, 512)
		for {
			n, err := stderrPipe.Read(buf)
			if n > 0 {
				progressChan <- string(buf[:n])
			}
			if err != nil {
				break
			}
		}
		doneChan <- struct{}{}
	}()

	// Wait for pipe readers to finish
	<-doneChan
	<-doneChan

	err = cmd.Wait()
	if err != nil {
		step.Status = StateError
		step.ErrorMsg = fmt.Sprintf("command failed: %s", err.Error())
		return err
	}

	progressChan <- "\nCommand completed successfully.\n"
	step.Status = StateSuccess
	return nil
}

// cleanReplacementContent filters out any formatting markers a model might have returned
func cleanReplacementContent(content string) string {
	lines := strings.Split(content, "\n")
	var cleaned []string
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "<<<<<<<") || 
		   strings.HasPrefix(trimmed, "=======") || 
		   strings.HasPrefix(trimmed, ">>>>>>>") ||
		   strings.Contains(trimmed, "ORIGINAL BLOCK") ||
		   strings.Contains(trimmed, "NEW CODE FOR THIS BLOCK") {
			continue
		}
		cleaned = append(cleaned, line)
	}
	return strings.Join(cleaned, "\n")
}

func executeForemanCodeReview(cwd string, cloudAgent string, step *Step, progressChan chan string) error {
	progressChan <- fmt.Sprintf("Running cloud code review validation using %s...\n", cloudAgent)
	
	// 1. Get git diff
	diffCmd := exec.Command("git", "diff")
	diffCmd.Dir = cwd
	diffOut, err := diffCmd.CombinedOutput()
	if err != nil {
		progressChan <- "Workspace is not a git repository or git command failed. Skipping code review.\n"
		step.Status = StateSuccess
		return nil
	}

	diffStr := string(diffOut)
	if strings.TrimSpace(diffStr) == "" {
		progressChan <- "No modifications detected. Skipping code review.\n"
		step.Status = StateSuccess
		return nil
	}

	progressChan <- fmt.Sprintf("Sending git diff to %s for review...\n\n", cloudAgent)

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

	// 3. Run Cloud Agent to review
	reviewOut, runErr := RunCloudAgent(cloudAgent, cwd, reviewPrompt, true, progressChan)
	if runErr != nil {
		step.Status = StateError
		step.ErrorMsg = fmt.Sprintf("Cloud review execution failed: %s", runErr.Error())
		return runErr
	}

	if strings.Contains(strings.ToUpper(reviewOut), "VALID") && !strings.Contains(reviewOut, "=== STEP ===") {
		progressChan <- "\nCode review validated as OK.\n"
		step.Status = StateSuccess
		return nil
	}

	// Code review found errors
	step.Status = StateError
	step.ErrorMsg = "Code review failed: correction steps generated."
	return fmt.Errorf(step.ErrorMsg)
}

func executeForemanBuildLintTest(cwd string, step *Step, progressChan chan string) error {
	progressChan <- "Detecting project build configuration...\n"
	
	var commands []string

	// Detect Go
	if _, err := os.Stat(filepath.Join(cwd, "go.mod")); err == nil {
		progressChan <- "Go project detected.\n"
		commands = append(commands, "go build ./...")
		commands = append(commands, "go test ./...")
	} else if _, err := os.Stat(filepath.Join(cwd, "package.json")); err == nil {
		// Detect Node
		progressChan <- "Node.js project detected.\n"
		// Read package.json to see what scripts exist
		packageJsonPath := filepath.Join(cwd, "package.json")
		bytes, err := os.ReadFile(packageJsonPath)
		if err == nil {
			var pkg struct {
				Scripts map[string]string `json:"scripts"`
			}
			if json.Unmarshal(bytes, &pkg) == nil {
				if _, ok := pkg.Scripts["build"]; ok {
					commands = append(commands, "npm run build")
				}
				if _, ok := pkg.Scripts["lint"]; ok {
					commands = append(commands, "npm run lint")
				}
				if _, ok := pkg.Scripts["test"]; ok {
					commands = append(commands, "npm test")
				}
			}
		}
		if len(commands) == 0 {
			commands = append(commands, "npm run build")
		}
	} else if _, err := os.Stat(filepath.Join(cwd, "Cargo.toml")); err == nil {
		// Detect Rust
		progressChan <- "Rust project detected.\n"
		commands = append(commands, "cargo build")
		commands = append(commands, "cargo test")
	} else {
		progressChan <- "Unknown project type. Scanning for common build configurations...\n"
		commands = append(commands, "make")
	}

	if len(commands) == 0 {
		progressChan <- "No build or test commands detected. Skipping verification.\n"
		step.Status = StateSuccess
		return nil
	}

	// Chain commands with "&&"
	chainedCommand := strings.Join(commands, " && ")
	progressChan <- fmt.Sprintf("Running verification: %s\n\n", chainedCommand)

	// Update step's command so TUI displays it
	step.Command = chainedCommand

	// Execute the command
	cmd := exec.Command("sh", "-c", chainedCommand)
	cmd.Dir = cwd

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		step.Status = StateError
		step.ErrorMsg = err.Error()
		return err
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		step.Status = StateError
		step.ErrorMsg = err.Error()
		return err
	}

	if err := cmd.Start(); err != nil {
		step.Status = StateError
		step.ErrorMsg = err.Error()
		return err
	}

	doneChan := make(chan struct{}, 2)
	go func() {
		buf := make([]byte, 512)
		for {
			n, err := stdoutPipe.Read(buf)
			if n > 0 {
				progressChan <- string(buf[:n])
			}
			if err != nil {
				break
			}
		}
		doneChan <- struct{}{}
	}()
	go func() {
		buf := make([]byte, 512)
		for {
			n, err := stderrPipe.Read(buf)
			if n > 0 {
				progressChan <- string(buf[:n])
			}
			if err != nil {
				break
			}
		}
		doneChan <- struct{}{}
	}()

	<-doneChan
	<-doneChan

	err = cmd.Wait()
	if err != nil {
		step.Status = StateError
		step.ErrorMsg = fmt.Sprintf("verification failed: %s", err.Error())
		return err
	}

	progressChan <- "\nVerification completed successfully.\n"
	step.Status = StateSuccess
	return nil
}

