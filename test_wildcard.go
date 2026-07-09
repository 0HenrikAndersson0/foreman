package main

import (
	"fmt"
	"strings"
)

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

	// Try exact match first (only if no wildcards)
	if !strings.Contains(oldCode, "// ...") && !strings.Contains(oldCode, "\n...") {
		if strings.Contains(fileContent, oldCode) {
			return strings.Replace(fileContent, oldCode, newCode, 1), nil
		}
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

	exactOldBlock := strings.Join(fileLines[matchStartIdx:matchEndIdx], "\n")
	return strings.Replace(fileContent, exactOldBlock, finalNewCode, 1), nil
}

func main() {
	fileContent := `import React from 'react';

export async function deleteFile(filePath: string): Promise<{ success: boolean }> {
  const x = 1;
  const y = 2;
  const z = 3;
  return { success: true };
}

export function Hello() {}
`
	modelOutput := `
<<<<
export async function deleteFile(filePath: string): Promise<{ success: boolean }> {
  // ...
  return { success: true };
}
====
export async function deleteFile(filePath: string): Promise<{ success: boolean }> {
  console.log("DELETED");
  // ...
  return { success: true };
}
>>>> 
`
	fmt.Println("Testing applySearchReplace with Wildcard...")
	result, err := applySearchReplace("cwd", fileContent, modelOutput)
	if err != nil {
		fmt.Println("Error:", err)
	} else {
		fmt.Println("Success!\n---")
		fmt.Println(result)
		fmt.Println("---")
	}
}
