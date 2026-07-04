package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const ollamaBaseURL = "http://localhost:11434"

// FetchOllamaModels retrieves the list of available local models.
func FetchOllamaModels() ([]string, error) {
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(fmt.Sprintf("%s/api/tags", ollamaBaseURL))
	if err != nil {
		return nil, fmt.Errorf("failed to connect to Ollama: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status from Ollama api/tags: %s", resp.Status)
	}

	var tagsResp OllamaTagsResponse
	if err := json.NewDecoder(resp.Body).Decode(&tagsResp); err != nil {
		return nil, fmt.Errorf("failed to decode Ollama models: %w", err)
	}

	var models []string
	for _, m := range tagsResp.Models {
		models = append(models, m.Name)
	}

	return models, nil
}

// GenerateCode calls the Ollama /api/generate endpoint to execute a code task, streaming response tokens to progressChan.
func GenerateCode(model string, prompt string, progressChan chan string) (string, error) {
	client := &http.Client{Timeout: 0} // No timeout for local Ollama calls (allows slow model loading/cold start)

	reqBody := OllamaGenerateRequest{
		Model:  model,
		Prompt: prompt,
		Stream: true,
		Options: OllamaOptions{
			NumCtx: 16384, // Request a 16k context window for larger code files
		},
	}

	jsonBytes, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("failed to marshal request: %w", err)
	}

	resp, err := client.Post(
		fmt.Sprintf("%s/api/generate", ollamaBaseURL),
		"application/json",
		bytes.NewBuffer(jsonBytes),
	)
	if err != nil {
		return "", fmt.Errorf("failed to post to Ollama generate: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("ollama generate returned status %s: %s", resp.Status, string(bodyBytes))
	}

	var fullResponse strings.Builder
	decoder := json.NewDecoder(resp.Body)

	for {
		var chunk struct {
			Response string `json:"response"`
			Done     bool   `json:"done"`
		}
		if err := decoder.Decode(&chunk); err != nil {
			if err == io.EOF {
				break
			}
			return "", fmt.Errorf("error decoding stream chunk: %w", err)
		}

		if chunk.Response != "" {
			fullResponse.WriteString(chunk.Response)
			progressChan <- chunk.Response
		}

		if chunk.Done {
			break
		}
	}

	return fullResponse.String(), nil
}
