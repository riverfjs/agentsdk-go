package tts

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	edge_tts "github.com/bytectlgo/edge-tts/pkg/edge_tts"
	"github.com/riverfjs/agentsdk-go/pkg/voice"
)

type Edge struct {
	Config voice.TTSConfig
}

func (e *Edge) Synthesize(ctx context.Context, req voice.TTSRequest) (voice.TTSResult, error) {
	text := strings.TrimSpace(req.Text)
	if text == "" {
		return voice.TTSResult{}, fmt.Errorf("edge tts: text is empty")
	}
	cfg := e.Config
	outputDir := strings.TrimSpace(cfg.OutputDir)
	if outputDir == "" {
		outputDir = filepath.Join(".", ".claude", "voice", "tts")
	}
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return voice.TTSResult{}, fmt.Errorf("edge tts: create output dir: %w", err)
	}

	voiceName := strings.TrimSpace(cfg.Voice)
	if voiceName == "" {
		voiceName = "en-US-MichelleNeural"
	}

	fileName := fmt.Sprintf("%s-%d.mp3", sanitizeFileName(req.SessionID), time.Now().UnixNano())
	audioPath := filepath.Join(outputDir, fileName)

	timeout := time.Duration(cfg.TimeoutSec) * time.Second
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	ttsCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	opts := make([]edge_tts.Option, 0, 3)
	if rate := strings.TrimSpace(cfg.Rate); rate != "" {
		opts = append(opts, edge_tts.WithRate(rate))
	}
	if volume := strings.TrimSpace(cfg.Volume); volume != "" {
		opts = append(opts, edge_tts.WithVolume(volume))
	}
	if pitch := strings.TrimSpace(cfg.Pitch); pitch != "" {
		opts = append(opts, edge_tts.WithPitch(pitch))
	}
	engine := edge_tts.NewCommunicate(text, voiceName, opts...)
	if err := engine.Save(ttsCtx, audioPath, ""); err != nil {
		return voice.TTSResult{}, fmt.Errorf("edge tts: synthesize failed: %w", err)
	}

	return voice.TTSResult{
		FilePath: audioPath,
		MimeType: "audio/mpeg",
	}, nil
}

func sanitizeFileName(input string) string {
	s := strings.TrimSpace(input)
	if s == "" {
		return "session"
	}
	replacer := strings.NewReplacer("/", "_", "\\", "_", " ", "_", ":", "_")
	s = replacer.Replace(s)
	if len(s) > 60 {
		s = s[:60]
	}
	return s
}
