package api

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"time"

	"github.com/tidwall/gjson"

	coreevents "github.com/cexll/agentsdk-go/pkg/core/events"
)

// extractSpecialEvents scans PostToolUse events and generates special events
// (FileAttachment) based on tool results.
// Only SendFile tool triggers FileAttachment (agent's explicit intent to send files)
func (rt *Runtime) extractSpecialEvents(events []coreevents.Event) []coreevents.Event {
	var specialEvents []coreevents.Event
	
	for _, event := range events {
		if event.Type != coreevents.PostToolUse {
			continue
		}
		
		payload, ok := event.Payload.(coreevents.ToolResultPayload)
		if !ok {
			continue
		}
		
		// Only SendFile tool should trigger FileAttachment
		// Other tools (like Bash screenshot) return file paths for reference, not for sending
		if payload.Name != "SendFile" {
			continue
		}
		
		// Convert result to JSON string for gjson parsing
		resultJSON := convertToJSON(payload.Result)
		if resultJSON == "" {
			continue
		}
		
		// Extract file_path from SendFile result
		pathStr := gjson.Get(resultJSON, "file_path").String()
		if pathStr != "" {
			specialEvents = append(specialEvents, rt.createFileAttachmentEvent(event, payload, pathStr, resultJSON))
		}
	}
	
	return specialEvents
}

// convertToJSON converts tool result to JSON string for gjson parsing
func convertToJSON(result interface{}) string {
	switch v := result.(type) {
	case string:
		return v
	case map[string]interface{}:
		// Already a map, marshal to JSON
		data, err := json.Marshal(v)
		if err != nil {
			return ""
		}
		return string(data)
	default:
		return ""
	}
}

// createFileAttachmentEvent creates a FileAttachment event from tool result
func (rt *Runtime) createFileAttachmentEvent(origEvent coreevents.Event, payload coreevents.ToolResultPayload, pathStr string, resultJSON string) coreevents.Event {
	// Determine attachment type
	attType := "file"
	if strings.Contains(pathStr, "screenshot") || 
	   strings.HasSuffix(pathStr, ".png") || 
	   strings.HasSuffix(pathStr, ".jpg") || 
	   strings.HasSuffix(pathStr, ".jpeg") {
		attType = "image"
	}
	
	// Auto-detect MIME type
	mimeType := ""
	ext := strings.ToLower(filepath.Ext(pathStr))
	switch ext {
	case ".png":
		mimeType = "image/png"
	case ".jpg", ".jpeg":
		mimeType = "image/jpeg"
	case ".gif":
		mimeType = "image/gif"
	case ".webp":
		mimeType = "image/webp"
	case ".pdf":
		mimeType = "application/pdf"
	}
	
	// Extract metadata (all fields except path and filename)
	metadata := make(map[string]interface{})
	gjson.Parse(resultJSON).ForEach(func(key, value gjson.Result) bool {
		k := key.String()
		if k != "path" && k != "filename" {
			metadata[k] = value.Value()
		}
		return true
	})
	
	rt.logger.Debugf("[api] Creating FileAttachment event for: %s", pathStr)
	
	return coreevents.Event{
		Type:      coreevents.FileAttachment,
		Timestamp: time.Now(),
		SessionID: origEvent.SessionID,
		RequestID: origEvent.RequestID,
		Payload: coreevents.FileAttachmentPayload{
			ToolName:  payload.Name,
			FilePath:  pathStr,
			FileName:  filepath.Base(pathStr),
			MimeType:  mimeType,
			Type:      attType,
			Metadata:  metadata,
			ToolUseID: payload.ToolUseID,
		},
	}
}
