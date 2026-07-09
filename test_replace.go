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

	fileLines := strings.Split(fileContent, "\n")
	matchStartIdx := -1
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
			break
		}
	}

	if matchStartIdx == -1 {
		fmt.Printf("Fuzzy Match Failed. The old lines did not match any contiguous block in the file.\nOldLines parsed:\n%v\n", oldLines)
		return "", fmt.Errorf("Search/Replace block failed: the old code block was not found exactly in the file")
	}

	// Replace the exact matching lines in the original file
	exactOldBlock := strings.Join(fileLines[matchStartIdx:matchStartIdx+len(oldLines)], "\n")
	return strings.Replace(fileContent, exactOldBlock, newCode, 1), nil
}

func main() {
	fileContent := `
export async function deleteFile(filePath: string): Promise<{ success: boolean; error?: string; errorType?: string }> {
	return { success: true };
}
`
	modelOutput := `
<<<<
export async function deleteFile(filePath: string): Promise<{ success: boolean; error?: string; errorType?: string }> {
====
export async function revertFilesChanges(filePaths: string[]): Promise<{ success: boolean; error?: string; errorType?: string }> {
  try {
    return { success: true };
  } catch (error: any) {
    return { success: false };
  }
}

export async function deleteFile(filePath: string): Promise<{ success: boolean; error?: string; errorType?: string }> {
====
>>>> 
`
	fmt.Println("Testing applySearchReplace...")
	result, err := applySearchReplace("cwd", fileContent, modelOutput)
	if err != nil {
		fmt.Println("Error:", err)
	} else {
		fmt.Println("Success!")
		fmt.Println(result)
	}
}
