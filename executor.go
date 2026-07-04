package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// ExecuteStep runs a single step in the blueprint, streaming output to progressChan.
func ExecuteStep(cwd string, model string, step *Step, progressChan chan string) error {
	step.Status = StateRunning
	defer close(progressChan)

	switch step.Type {
	case StepCreate:
		return executeCreate(cwd, step, progressChan)
	case StepModify:
		return executeModify(cwd, model, step, progressChan)
	case StepCommand:
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
		if step.Feedback != "" {
			prompt = fmt.Sprintf(`You are a developer rewriting a specific block of code inside a file to fix a previous attempt.
Your task is to modify the target block of code according to these instructions:
---
Instructions:
%s
---
User Correction Feedback:
%s
---
Target Block of Code to Replace:
%s
---

You MUST output ONLY the new replacement code block that should replace the Target Block of Code. Do NOT output the rest of the file. Do NOT write any explanation, comments, or intro/outro text. Preserve the indentation level of the target block.
You MUST output the final code inside a single markdown code block starting with %s and ending with %s.`, 
				step.Instructions, step.Feedback, targetBlock, "```", "```")
		} else {
			prompt = fmt.Sprintf(`You are a developer rewriting a specific block of code inside a file.
Your task is to modify the target block of code according to these instructions:
---
Instructions:
%s
---
Target Block of Code to Replace:
%s
---

You MUST output ONLY the new replacement code block that should replace the Target Block of Code. Do NOT output the rest of the file. Do NOT write any explanation, comments, or intro/outro text. Preserve the indentation level of the target block.
You MUST output the final code inside a single markdown code block starting with %s and ending with %s.`, 
				step.Instructions, targetBlock, "```", "```")
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
		finalContent = strings.Replace(currentContent, targetBlock, updatedContent, 1)
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
