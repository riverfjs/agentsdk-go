package api

import (
	"path/filepath"
	"strings"
)

type attachmentExtInfo struct {
	IsAudio   bool
	AudioMIME string
	ImageMIME string
}

var attachmentExtMeta = map[string]attachmentExtInfo{
	".wav":  {IsAudio: true, AudioMIME: "audio/wav"},
	".mp3":  {IsAudio: true, AudioMIME: "audio/mpeg"},
	".ogg":  {IsAudio: true, AudioMIME: "audio/ogg"},
	".opus": {IsAudio: true, AudioMIME: "audio/ogg"},
	".m4a":  {IsAudio: true, AudioMIME: "audio/mp4"},
	".aac":  {IsAudio: true, AudioMIME: "audio/aac"},
	".flac": {IsAudio: true, AudioMIME: "audio/flac"},
	".jpg":  {ImageMIME: "image/jpeg"},
	".jpeg": {ImageMIME: "image/jpeg"},
	".png":  {ImageMIME: "image/png"},
	".gif":  {ImageMIME: "image/gif"},
	".webp": {ImageMIME: "image/webp"},
}

// DetectAttachmentTypeFromPath infers attachment type by file extension.
func DetectAttachmentTypeFromPath(filePath string) string {
	if meta, ok := attachmentExtMeta[strings.ToLower(filepath.Ext(filePath))]; ok && meta.IsAudio {
		return "audio"
	}
	return "image"
}

// DetectAttachmentType infers attachment type from explicit type/mime/path.
func DetectAttachmentType(rawType, mimeType, filePath string) string {
	normalized := strings.ToLower(strings.TrimSpace(rawType))
	switch normalized {
	case "image", "audio", "file":
		return normalized
	}
	lowerMIME := strings.ToLower(strings.TrimSpace(mimeType))
	if strings.HasPrefix(lowerMIME, "audio/") {
		return "audio"
	}
	if strings.HasPrefix(lowerMIME, "image/") {
		return "image"
	}
	return DetectAttachmentTypeFromPath(filePath)
}

// DetectAttachmentMIME infers MIME type from type and file extension.
func DetectAttachmentMIME(attachmentType, filePath string) string {
	kind := strings.ToLower(strings.TrimSpace(attachmentType))
	if kind == "" || kind == "file" {
		kind = DetectAttachmentTypeFromPath(filePath)
	}
	ext := strings.ToLower(filepath.Ext(filePath))
	if meta, ok := attachmentExtMeta[ext]; ok {
		if kind == "audio" {
			if meta.AudioMIME != "" {
				return meta.AudioMIME
			}
			return "audio/wav"
		}
		if meta.ImageMIME != "" {
			return meta.ImageMIME
		}
	}
	if kind == "audio" {
		return "audio/wav"
	}
	return "image/jpeg"
}
