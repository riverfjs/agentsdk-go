package asr

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/riverfjs/agentsdk-go/pkg/voice"
)

type AssemblyAI struct {
	Config voice.ASRConfig
	Client *http.Client
}

func (a *AssemblyAI) Transcribe(ctx context.Context, req voice.ASRRequest) (voice.ASRResult, error) {
	cfg := a.Config
	apiKey := strings.TrimSpace(cfg.APIKey)
	if apiKey == "" {
		return voice.ASRResult{}, errors.New("assemblyai: api key required")
	}
	baseURL := strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	if baseURL == "" {
		baseURL = "https://api.assemblyai.com"
	}

	timeout := time.Duration(cfg.TimeoutSec) * time.Second
	if timeout <= 0 {
		timeout = 120 * time.Second
	}
	client := a.Client
	if client == nil {
		client = &http.Client{Timeout: timeout}
	}

	audioData, err := os.ReadFile(req.FilePath)
	if err != nil {
		return voice.ASRResult{}, fmt.Errorf("assemblyai: read file: %w", err)
	}

	uploadReq, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/v2/upload", bytes.NewReader(audioData))
	if err != nil {
		return voice.ASRResult{}, fmt.Errorf("assemblyai: create upload request: %w", err)
	}
	uploadReq.Header.Set("authorization", apiKey)
	uploadResp, err := client.Do(uploadReq)
	if err != nil {
		return voice.ASRResult{}, fmt.Errorf("assemblyai: upload failed: %w", err)
	}
	defer uploadResp.Body.Close()
	uploadBody, _ := io.ReadAll(uploadResp.Body)
	if uploadResp.StatusCode < 200 || uploadResp.StatusCode >= 300 {
		return voice.ASRResult{}, fmt.Errorf("assemblyai: upload status=%d body=%s", uploadResp.StatusCode, strings.TrimSpace(string(uploadBody)))
	}
	var uploadResult struct {
		UploadURL string `json:"upload_url"`
	}
	if err := json.Unmarshal(uploadBody, &uploadResult); err != nil {
		return voice.ASRResult{}, fmt.Errorf("assemblyai: decode upload response: %w", err)
	}
	if strings.TrimSpace(uploadResult.UploadURL) == "" {
		return voice.ASRResult{}, errors.New("assemblyai: upload_url is empty")
	}

	payload := map[string]any{
		"audio_url":          uploadResult.UploadURL,
		"language_detection": cfg.LanguageDetection,
	}
	if len(cfg.SpeechModels) > 0 {
		payload["speech_models"] = cfg.SpeechModels
	}
	payloadBytes, _ := json.Marshal(payload) //nolint:errcheck

	createReq, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/v2/transcript", bytes.NewReader(payloadBytes))
	if err != nil {
		return voice.ASRResult{}, fmt.Errorf("assemblyai: create transcript request: %w", err)
	}
	createReq.Header.Set("authorization", apiKey)
	createReq.Header.Set("content-type", "application/json")
	createResp, err := client.Do(createReq)
	if err != nil {
		return voice.ASRResult{}, fmt.Errorf("assemblyai: create transcript failed: %w", err)
	}
	defer createResp.Body.Close()
	createBody, _ := io.ReadAll(createResp.Body)
	if createResp.StatusCode < 200 || createResp.StatusCode >= 300 {
		return voice.ASRResult{}, fmt.Errorf("assemblyai: create transcript status=%d body=%s", createResp.StatusCode, strings.TrimSpace(string(createBody)))
	}
	var createResult struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(createBody, &createResult); err != nil {
		return voice.ASRResult{}, fmt.Errorf("assemblyai: decode create transcript response: %w", err)
	}
	if strings.TrimSpace(createResult.ID) == "" {
		return voice.ASRResult{}, errors.New("assemblyai: transcript id is empty")
	}

	pollInterval := time.Duration(cfg.PollIntervalSec) * time.Second
	if pollInterval <= 0 {
		pollInterval = 3 * time.Second
	}
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return voice.ASRResult{}, ctx.Err()
		case <-ticker.C:
			pollReq, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/v2/transcript/"+createResult.ID, nil)
			if err != nil {
				return voice.ASRResult{}, fmt.Errorf("assemblyai: create poll request: %w", err)
			}
			pollReq.Header.Set("authorization", apiKey)
			pollResp, err := client.Do(pollReq)
			if err != nil {
				return voice.ASRResult{}, fmt.Errorf("assemblyai: poll transcript failed: %w", err)
			}
			pollBody, _ := io.ReadAll(pollResp.Body)
			pollResp.Body.Close()
			if pollResp.StatusCode < 200 || pollResp.StatusCode >= 300 {
				return voice.ASRResult{}, fmt.Errorf("assemblyai: poll status=%d body=%s", pollResp.StatusCode, strings.TrimSpace(string(pollBody)))
			}

			var pollResult struct {
				Status string `json:"status"`
				Text   string `json:"text"`
				Error  string `json:"error"`
			}
			if err := json.Unmarshal(pollBody, &pollResult); err != nil {
				return voice.ASRResult{}, fmt.Errorf("assemblyai: decode poll response: %w", err)
			}
			switch strings.ToLower(strings.TrimSpace(pollResult.Status)) {
			case "completed":
				return voice.ASRResult{Text: strings.TrimSpace(pollResult.Text)}, nil
			case "error":
				return voice.ASRResult{}, fmt.Errorf("assemblyai: transcript failed: %s", strings.TrimSpace(pollResult.Error))
			}
		}
	}
}
