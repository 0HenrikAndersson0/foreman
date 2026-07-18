package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// resolvePath safely resolves a step path against the current working directory, 
// handling cases where the model hallucinates an absolute path instead of a relative one.
func resolvePath(cwd, stepPath string) string {
	cleanPath := strings.TrimPrefix(stepPath, cwd)
	if strings.HasPrefix(cleanPath, "/") {
		cleanPath = strings.TrimPrefix(cleanPath, "/")
	}
	return filepath.Join(cwd, cleanPath)
}

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
	fullPath := resolvePath(cwd, step.Path)

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
	fullPath := resolvePath(cwd, step.Path)

	// Read existing file
	currentBytes, err := os.ReadFile(fullPath)
	if err != nil {
		step.Status = StateError
		step.ErrorMsg = fmt.Sprintf("failed to read file %s: %s", step.Path, err.Error())
		return err
	}
	currentContent := strings.ReplaceAll(string(currentBytes), "\r\n", "\n")
	targetBlock := strings.ReplaceAll(step.TargetBlock, "\r\n", "\n")

	useBlockReplacement := targetBlock != ""

	maxRetries := 3
	var lastError error
	var syntaxError string

	for i := 0; i < maxRetries; i++ {
		if i > 0 {
			progressChan <- fmt.Sprintf("Attempt %d/%d to fix error...\n", i+1, maxRetries)
		}

		var prompt string
		if useBlockReplacement {
			prompt = fmt.Sprintf(`You are an expert developer modifying code.
Your task is to modify the ORIGINAL BLOCK according to these instructions:
---
Instructions:
%s
---`, step.Instructions)
			
			if step.Feedback != "" {
				prompt += fmt.Sprintf("\nUser Correction Feedback:\n%s\n---", step.Feedback)
			}
			
			if syntaxError != "" {
				prompt += fmt.Sprintf("\nCRITICAL: Your previous attempt caused this error:\n%s\nFix the error in your new output.\n---", syntaxError)
			}

			prompt += fmt.Sprintf(`
Here is the ORIGINAL BLOCK:
%s

You MUST use the Search/Replace block format to output your changes.
Example format:
%s
<<<<
old lines to replace
====
new lines to add
>>>>
%s

Rules:
1. The 'old lines' must exactly match lines in the ORIGINAL BLOCK.
2. Output ONLY the Search/Replace block inside a single markdown code block. Do not write explanations.
3. CRITICAL: DO NOT use placeholders like "// ..." or "..." to skip code. You MUST write every single line of the old block explicitly.`, 
			targetBlock, "```", "```")

		} else {
			prompt = fmt.Sprintf(`You are an expert developer modifying a file.
Your task is to modify the file according to these instructions:
---
Instructions:
%s
---`, step.Instructions)
			
			if step.Feedback != "" {
				prompt += fmt.Sprintf("\nUser Correction Feedback:\n%s\n---", step.Feedback)
			}
			
			if syntaxError != "" {
				prompt += fmt.Sprintf("\nCRITICAL: Your previous attempt caused this error:\n%s\nFix the error in your new output.\n---", syntaxError)
			}

			prompt += fmt.Sprintf(`
Target Block to Replace (if specified):
%s
---

CRITICAL REQUIREMENT: You MUST output the ENTIRE file content. Do NOT truncate or use placeholders. Every unchanged line must remain exactly intact.

Here is the entire current content of the file:
%s

Output the final code inside a single markdown code block starting with %s and ending with %s.`, 
			targetBlock, currentContent, "```", "```")
		}

		var updatedContentRaw string
		var genErr error

		// Bypass Ollama if the Instructions already contain a valid Search/Replace block
		if strings.Contains(step.Instructions, "<<<<") && strings.Contains(step.Instructions, "====") && strings.Contains(step.Instructions, ">>>>") {
			LogDebug(cwd, "--- CLOUD AGENT DIRECT FIX ---\nStep: %s\nInstructions contain exact block.", step.Description)
			if i > 0 {
				progressChan <- "Cloud-Agent fix applied previously but failed verification. Halting.\n"
				lastError = fmt.Errorf("cloud agent direct fix failed verification: %s", syntaxError)
				break
			}
			progressChan <- "Direct Cloud-Agent fix detected. Bypassing local model.\n"
			updatedContentRaw = step.Instructions
		} else {
			LogDebug(cwd, "--- SENDING PROMPT TO OLLAMA (Attempt %d) ---\n%s", i+1, prompt)
			// Call Ollama and stream the output to progressChan
			updatedContentRaw, genErr = GenerateCode(model, prompt, progressChan)
			LogDebug(cwd, "--- OLLAMA RAW RESPONSE ---\n%s", updatedContentRaw)
			if genErr != nil {
				LogDebug(cwd, "Ollama Generation Error: %v", genErr)
				lastError = genErr
				break // if generation fails completely, don't retry
			}
		}

		// Extract the code block from Ollama's response
		updatedContent := extractCodeBlock(updatedContentRaw)
		if updatedContent == "" || updatedContent == updatedContentRaw {
			if len(updatedContentRaw) > 0 {
				updatedContent = updatedContentRaw
			} else {
				lastError = fmt.Errorf("Ollama returned an empty response")
				syntaxError = "Empty response. You must output a code block."
				continue
			}
		}

		var finalContent string
		if useBlockReplacement {
			LogDebug(cwd, "Applying Search/Replace block...")
			finalContent, err = applySearchReplace(cwd, currentContent, updatedContent)
			if err != nil {
				LogDebug(cwd, "Search/Replace failed: %v", err)
				syntaxError = fmt.Sprintf("Failed to apply Search/Replace block: %s\nMake sure your <<<< section exactly matches the original text.", err.Error())
				lastError = err
				continue
			}
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

		// Inner loop verification
		verifyCommands := getVerificationCommands(cwd, nil)
		if len(verifyCommands) > 0 {
			progressChan <- "Running local verification...\n"
			verifyErrStr := ""
			for _, cmdStr := range verifyCommands {
				cmd := exec.Command("sh", "-c", cmdStr)
				cmd.Dir = cwd
				out, vErr := cmd.CombinedOutput()
				if vErr != nil {
					verifyErrStr += fmt.Sprintf("Command '%s' failed:\n%s\n", cmdStr, string(out))
				}
			}
			
			if verifyErrStr != "" {
				syntaxError = verifyErrStr
				lastError = fmt.Errorf("verification failed")
				// Revert the file so we can try again
				_ = os.WriteFile(fullPath, currentBytes, 0644)
				continue
			}
		}

		progressChan <- "\nChanges successfully applied and verified.\n"
		step.Status = StateSuccess
		return nil
	}

	step.Status = StateError
	if syntaxError != "" {
		step.ErrorMsg = fmt.Sprintf("Failed after %d attempts. Last error: %s", maxRetries, syntaxError)
	} else {
		step.ErrorMsg = fmt.Sprintf("Ollama modification failed: %v", lastError)
	}
	return lastError
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

// applySearchReplace parses an Aider-style Search/Replace block and applies it to the file content.
func applySearchReplace(cwd string, fileContent string, modelOutput string) (string, error) {
	startIdx := strings.Index(modelOutput, "<<<<")
	if startIdx == -1 {
		return "", fmt.Errorf("Search/Replace block failed: missing <<<< marker")
	}

	divIdx := strings.Index(modelOutput[startIdx:], "====")
	if divIdx == -1 {
		return "", fmt.Errorf("Search/Replace block failed: missing ==== marker")
	}
	divIdx += startIdx

	// Check for multiple ==== markers (a common model hallucination)
	secondDivIdx := strings.Index(modelOutput[divIdx+4:], "====")
	if secondDivIdx != -1 {
		// Only consider it an error if the second ==== is before the >>>> marker
		endMarkerIdx := strings.Index(modelOutput[divIdx:], ">>>>")
		if endMarkerIdx != -1 && secondDivIdx < endMarkerIdx {
			return "", fmt.Errorf("Search/Replace block failed: multiple ==== markers found. Do not repeat the divider")
		}
	}

	endIdx := strings.Index(modelOutput[divIdx:], ">>>>")
	if endIdx == -1 {
		return "", fmt.Errorf("Search/Replace block failed: missing >>>> marker")
	}
	endIdx += divIdx

	oldCodeRaw := modelOutput[startIdx+4 : divIdx]
	newCodeRaw := modelOutput[divIdx+4 : endIdx]

	oldCode := strings.Trim(oldCodeRaw, "\r\n")
	newCode := strings.Trim(newCodeRaw, "\r\n")

	if oldCode == "" {
		return "", fmt.Errorf("Search/Replace block failed: old code block is empty")
	}

	// Try exact match first
	if strings.Contains(fileContent, oldCode) {
		return strings.Replace(fileContent, oldCode, newCode, 1), nil
	}

	// Fallback to fuzzy match (ignoring leading/trailing whitespace line by line)
	oldLinesRaw := strings.Split(oldCode, "\n")
	var oldLines []string
	for _, l := range oldLinesRaw {
		oldLines = append(oldLines, strings.TrimSpace(l))
	}

	// Remove leading/trailing empty lines from the search block
	for len(oldLines) > 0 && oldLines[0] == "" {
		oldLines = oldLines[1:]
	}
	for len(oldLines) > 0 && oldLines[len(oldLines)-1] == "" {
		oldLines = oldLines[:len(oldLines)-1]
	}

	if len(oldLines) == 0 {
		return "", fmt.Errorf("Search/Replace block failed: old code block is empty or only whitespace")
	}

	var chunks [][]string
	var currentChunk []string
	hasWildcard := false
	for _, l := range oldLines {
		if l == "..." || l == "// ..." || l == "/* ... */" || l == "//..." {
			if len(currentChunk) > 0 {
				chunks = append(chunks, currentChunk)
				currentChunk = nil
			} else {
				if len(chunks) == 0 {
					return "", fmt.Errorf("Search/Replace block failed: cannot start with a wildcard")
				}
			}
			hasWildcard = true
		} else {
			currentChunk = append(currentChunk, l)
		}
	}
	if len(currentChunk) > 0 {
		chunks = append(chunks, currentChunk)
	}

	if hasWildcard && len(chunks) == 0 {
		return "", fmt.Errorf("Search/Replace block failed: old code block is only wildcards")
	}

	fileLines := strings.Split(fileContent, "\n")
	matchStartIdx := -1
	matchEndIdx := -1
	searchStartLine := 0
	var skippedBlocks []string

	if !hasWildcard {
		// normal contiguous matching
		for i := 0; i <= len(fileLines)-len(oldLines); i++ {
			match := true
			for j := 0; j < len(oldLines); j++ {
				if strings.TrimSpace(fileLines[i+j]) != oldLines[j] {
					match = false
					break
				}
			}
			if match {
				matchStartIdx = i
				matchEndIdx = i + len(oldLines)
				break
			}
		}
	} else {
		// Wildcard matching
		for cIdx, chunk := range chunks {
			found := false
			for i := searchStartLine; i <= len(fileLines)-len(chunk); i++ {
				match := true
				for j := 0; j < len(chunk); j++ {
					if strings.TrimSpace(fileLines[i+j]) != chunk[j] {
						match = false
						break
					}
				}
				if match {
					if cIdx == 0 {
						matchStartIdx = i
					} else {
						// Record the skipped lines
						skipped := fileLines[searchStartLine:i]
						skippedBlocks = append(skippedBlocks, strings.Join(skipped, "\n"))
					}
					searchStartLine = i + len(chunk)
					if cIdx == len(chunks)-1 {
						matchEndIdx = searchStartLine
					}
					found = true
					break
				}
			}
			if !found {
				break
			}
		}
	}

	if matchStartIdx == -1 || matchEndIdx == -1 {
		LogDebug(cwd, "Fuzzy Match Failed. The old lines did not match any contiguous block in the file.\nOldLines parsed:\n%v", oldLines)
		return "", fmt.Errorf("Search/Replace block failed: the old code block was not found exactly in the file")
	}

	finalNewCode := newCode
	if hasWildcard {
		newLinesRaw := strings.Split(newCode, "\n")
		var reconstructedNewLines []string
		skipIdx := 0
		for _, l := range newLinesRaw {
			trimmed := strings.TrimSpace(l)
			if trimmed == "..." || trimmed == "// ..." || trimmed == "/* ... */" || trimmed == "//..." {
				if skipIdx < len(skippedBlocks) {
					reconstructedNewLines = append(reconstructedNewLines, skippedBlocks[skipIdx])
					skipIdx++
				}
			} else {
				reconstructedNewLines = append(reconstructedNewLines, l)
			}
		}
		finalNewCode = strings.Join(reconstructedNewLines, "\n")
	}

	// Replace the exact matching lines in the original file
	exactOldBlock := strings.Join(fileLines[matchStartIdx:matchEndIdx], "\n")
	return strings.Replace(fileContent, exactOldBlock, finalNewCode, 1), nil
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

Your primary goal is to verify that:
1. The changes are strictly related to implementing the requested task and that the project builds/compiles correctly.
2. No required code, imports, or helper functions were accidentally removed or truncated.

CRITICAL RULES:
- Ignore any stylistic refactoring, code cleaning, formatting, or optional improvements. Focus purely on bug detection and logic flaws, not stylistic nitpicks.
- If you provide modifications, DO NOT use placeholders like "// ..." or "..." to skip code in the old block. You MUST write every single line of the old block explicitly.
- Only generate correction steps if there is a compile/syntax error, missing necessary logic, or if essential code was accidentally deleted.
- If the changes are functional, correct, and do not break compilation (even if they could be cleaner), output ONLY the word "VALID" (with no other text).

If there are critical errors or missing needed code, output one or more correction steps formatted EXACTLY as follows:
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
[Provide the exact instructions to fix the bug. CRITICAL: For modifications, you MUST provide the exact Aider-style Search/Replace block (using <<<<, ====, >>>>) inside the Instructions field so it can be applied instantly without the local model.]
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
	
	commands := getVerificationCommands(cwd, progressChan)
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

func getVerificationCommands(cwd string, progressChan chan string) []string {
	var commands []string

	if _, err := os.Stat(filepath.Join(cwd, "go.mod")); err == nil {
		if progressChan != nil {
			progressChan <- "Go project detected.\n"
		}
		commands = append(commands, "go build ./...")
		commands = append(commands, "go test ./...")
	} else if _, err := os.Stat(filepath.Join(cwd, "package.json")); err == nil {
		if progressChan != nil {
			progressChan <- "Node.js project detected.\n"
		}
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
		if progressChan != nil {
			progressChan <- "Rust project detected.\n"
		}
		commands = append(commands, "cargo build")
		commands = append(commands, "cargo test")
	} else {
		if progressChan != nil {
			progressChan <- "Unknown project type. Scanning for common build configurations...\n"
		}
		commands = append(commands, "make")
	}
	return commands
}
