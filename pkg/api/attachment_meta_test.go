package api

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDetectAttachmentMIME_ImageByContent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sample.bin")
	// PNG signature.
	data := []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A}
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	got := DetectAttachmentMIME("", path)
	if got != "image/png" {
		t.Fatalf("got %q, want image/png", got)
	}
}

func TestDetectAttachmentMIME_AudioByContent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sample.raw")
	// Minimal WAV header-like bytes.
	data := []byte("RIFF\x24\x80\x00\x00WAVEfmt ")
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	got := DetectAttachmentMIME("", path)
	if !strings.HasPrefix(got, "audio/") {
		t.Fatalf("got %q, want audio/*", got)
	}
}

func TestDetectAttachmentMIME_MissingFileFallback(t *testing.T) {
	if got := DetectAttachmentMIME("audio", "/path/not/exist.wav"); got != "" {
		t.Fatalf("audio missing-file got %q, want empty", got)
	}
	if got := DetectAttachmentMIME("image", "/path/not/exist.png"); got != "" {
		t.Fatalf("image missing-file got %q, want empty", got)
	}
}

func TestDetectAttachmentMIME_ConflictKindUsesDetectedContent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sample.dat")
	data := []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A}
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	got := DetectAttachmentMIME("audio", path)
	if got != "image/png" {
		t.Fatalf("got %q, want image/png", got)
	}
}

