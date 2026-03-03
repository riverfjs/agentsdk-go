package voice

import "context"

// ASRConfig configures speech-to-text preprocessing.
type ASRConfig struct {
	Provider          string
	APIKey            string
	BaseURL           string
	SpeechModels      []string
	LanguageDetection bool
	PollIntervalSec   int
	TimeoutSec        int
}

// TTSConfig configures text-to-speech postprocessing.
type TTSConfig struct {
	Provider   string
	Voice      string
	Rate       string
	Volume     string
	Pitch      string
	OutputDir  string
	TimeoutSec int
}

type ASRRequest struct {
	FilePath  string
	MimeType  string
	SessionID string
	RequestID string
}

type ASRResult struct {
	Text string
}

type TTSRequest struct {
	Text      string
	SessionID string
	RequestID string
}

type TTSResult struct {
	FilePath string
	MimeType string
}

type ASRProvider interface {
	Transcribe(ctx context.Context, req ASRRequest) (ASRResult, error)
}

type TTSProvider interface {
	Synthesize(ctx context.Context, req TTSRequest) (TTSResult, error)
}
