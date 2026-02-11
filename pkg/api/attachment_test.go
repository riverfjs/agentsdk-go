package api

import (
	"testing"

	coreevents "github.com/cexll/agentsdk-go/pkg/core/events"
	"github.com/cexll/agentsdk-go/pkg/logger"
)

func TestExtractAttachments_Screenshot(t *testing.T) {
	rt := &Runtime{
		logger: logger.NewDefault(),
	}

	events := []coreevents.Event{
		{
			Type: coreevents.PostToolUse,
			Payload: coreevents.ToolResultPayload{
				Name: "Bash",
				Result: map[string]interface{}{
					"path":     "/tmp/screenshot-1234567890.png",
					"filename": "screenshot-1234567890.png",
					"fullPage": false,
				},
			},
		},
	}

	attachments := rt.extractAttachments(events)

	if len(attachments) != 1 {
		t.Fatalf("expected 1 attachment, got %d", len(attachments))
	}

	att := attachments[0]
	if att.Type != AttachmentTypeImage {
		t.Errorf("expected type %q, got %q", AttachmentTypeImage, att.Type)
	}
	if att.Path != "/tmp/screenshot-1234567890.png" {
		t.Errorf("expected path %q, got %q", "/tmp/screenshot-1234567890.png", att.Path)
	}
	if att.MimeType != "image/png" {
		t.Errorf("expected mime type %q, got %q", "image/png", att.MimeType)
	}
	if att.Source != "Bash" {
		t.Errorf("expected source %q, got %q", "Bash", att.Source)
	}
	if att.Metadata["fullPage"] != false {
		t.Errorf("expected metadata fullPage=false, got %v", att.Metadata["fullPage"])
	}
}

func TestExtractAttachments_MultipleImages(t *testing.T) {
	rt := &Runtime{
		logger: logger.NewDefault(),
	}

	events := []coreevents.Event{
		{
			Type: coreevents.PostToolUse,
			Payload: coreevents.ToolResultPayload{
				Name: "Bash",
				Result: map[string]interface{}{
					"path": "/tmp/screenshot-1.png",
				},
			},
		},
		{
			Type: coreevents.PostToolUse,
			Payload: coreevents.ToolResultPayload{
				Name: "Skill",
				Result: map[string]interface{}{
					"path": "/tmp/screenshot-2.jpg",
				},
			},
		},
	}

	attachments := rt.extractAttachments(events)

	if len(attachments) != 2 {
		t.Fatalf("expected 2 attachments, got %d", len(attachments))
	}

	if attachments[0].MimeType != "image/png" {
		t.Errorf("expected first mime type %q, got %q", "image/png", attachments[0].MimeType)
	}
	if attachments[1].MimeType != "image/jpeg" {
		t.Errorf("expected second mime type %q, got %q", "image/jpeg", attachments[1].MimeType)
	}
}

func TestExtractAttachments_NonImageFile(t *testing.T) {
	rt := &Runtime{
		logger: logger.NewDefault(),
	}

	events := []coreevents.Event{
		{
			Type: coreevents.PostToolUse,
			Payload: coreevents.ToolResultPayload{
				Name: "Write",
				Result: map[string]interface{}{
					"path": "/tmp/document.pdf",
				},
			},
		},
	}

	attachments := rt.extractAttachments(events)

	if len(attachments) != 1 {
		t.Fatalf("expected 1 attachment, got %d", len(attachments))
	}

	att := attachments[0]
	if att.Type != AttachmentTypeFile {
		t.Errorf("expected type %q, got %q", AttachmentTypeFile, att.Type)
	}
	if att.MimeType != "application/pdf" {
		t.Errorf("expected mime type %q, got %q", "application/pdf", att.MimeType)
	}
}

func TestExtractAttachments_NoPath(t *testing.T) {
	rt := &Runtime{
		logger: logger.NewDefault(),
	}

	events := []coreevents.Event{
		{
			Type: coreevents.PostToolUse,
			Payload: coreevents.ToolResultPayload{
				Name: "Bash",
				Result: map[string]interface{}{
					"status": "success",
					"output": "command executed",
				},
			},
		},
	}

	attachments := rt.extractAttachments(events)

	if len(attachments) != 0 {
		t.Errorf("expected 0 attachments, got %d", len(attachments))
	}
}

func TestExtractAttachments_InvalidResultType(t *testing.T) {
	rt := &Runtime{
		logger: logger.NewDefault(),
	}

	events := []coreevents.Event{
		{
			Type: coreevents.PostToolUse,
			Payload: coreevents.ToolResultPayload{
				Name:   "Bash",
				Result: "plain string result",
			},
		},
	}

	attachments := rt.extractAttachments(events)

	if len(attachments) != 0 {
		t.Errorf("expected 0 attachments, got %d", len(attachments))
	}
}

func TestExtractAttachments_NonPostToolUseEvent(t *testing.T) {
	rt := &Runtime{
		logger: logger.NewDefault(),
	}

	events := []coreevents.Event{
		{
			Type: coreevents.PreToolUse,
			Payload: coreevents.ToolUsePayload{
				Name: "Bash",
			},
		},
	}

	attachments := rt.extractAttachments(events)

	if len(attachments) != 0 {
		t.Errorf("expected 0 attachments, got %d", len(attachments))
	}
}

func TestExtractAttachments_EmptyPath(t *testing.T) {
	rt := &Runtime{
		logger: logger.NewDefault(),
	}

	events := []coreevents.Event{
		{
			Type: coreevents.PostToolUse,
			Payload: coreevents.ToolResultPayload{
				Name: "Bash",
				Result: map[string]interface{}{
					"path": "",
				},
			},
		},
	}

	attachments := rt.extractAttachments(events)

	if len(attachments) != 0 {
		t.Errorf("expected 0 attachments, got %d", len(attachments))
	}
}

func TestExtractAttachments_WebPAndGif(t *testing.T) {
	rt := &Runtime{
		logger: logger.NewDefault(),
	}

	events := []coreevents.Event{
		{
			Type: coreevents.PostToolUse,
			Payload: coreevents.ToolResultPayload{
				Name: "Bash",
				Result: map[string]interface{}{
					"path": "/tmp/image.webp",
				},
			},
		},
		{
			Type: coreevents.PostToolUse,
			Payload: coreevents.ToolResultPayload{
				Name: "Bash",
				Result: map[string]interface{}{
					"path": "/tmp/animation.gif",
				},
			},
		},
	}

	attachments := rt.extractAttachments(events)

	if len(attachments) != 2 {
		t.Fatalf("expected 2 attachments, got %d", len(attachments))
	}

	if attachments[0].MimeType != "image/webp" {
		t.Errorf("expected first mime type %q, got %q", "image/webp", attachments[0].MimeType)
	}
	if attachments[1].MimeType != "image/gif" {
		t.Errorf("expected second mime type %q, got %q", "image/gif", attachments[1].MimeType)
	}
}

func TestExtractAttachments_MetadataFiltering(t *testing.T) {
	rt := &Runtime{
		logger: logger.NewDefault(),
	}

	events := []coreevents.Event{
		{
			Type: coreevents.PostToolUse,
			Payload: coreevents.ToolResultPayload{
				Name: "Bash",
				Result: map[string]interface{}{
					"path":     "/tmp/screenshot.png",
					"filename": "screenshot.png",
					"fullPage": true,
					"selector": ".main-content",
					"width":    1920,
					"height":   1080,
				},
			},
		},
	}

	attachments := rt.extractAttachments(events)

	if len(attachments) != 1 {
		t.Fatalf("expected 1 attachment, got %d", len(attachments))
	}

	att := attachments[0]
	
	// "path" and "filename" should be excluded from metadata
	if _, hasPath := att.Metadata["path"]; hasPath {
		t.Error("metadata should not contain 'path'")
	}
	if _, hasFilename := att.Metadata["filename"]; hasFilename {
		t.Error("metadata should not contain 'filename'")
	}
	
	// Other fields should be in metadata
	if att.Metadata["fullPage"] != true {
		t.Errorf("expected metadata fullPage=true, got %v", att.Metadata["fullPage"])
	}
	if att.Metadata["selector"] != ".main-content" {
		t.Errorf("expected metadata selector=%q, got %v", ".main-content", att.Metadata["selector"])
	}
	if att.Metadata["width"] != 1920 {
		t.Errorf("expected metadata width=1920, got %v", att.Metadata["width"])
	}
	if att.Metadata["height"] != 1080 {
		t.Errorf("expected metadata height=1080, got %v", att.Metadata["height"])
	}
}

