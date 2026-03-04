package api

import (
	"net/http"
	"os"
	"strings"
)

// DetectAttachmentMIME infers MIME type from file content.
func DetectAttachmentMIME(attachmentType, filePath string) string {
	mimeType := detectMIMEFromFile(filePath)
	return mimeType
}

func detectMIMEFromFile(filePath string) string {
	file, err := os.Open(filePath)
	if err != nil {
		return ""
	}
	defer file.Close()

	buf := make([]byte, 512)
	n, err := file.Read(buf)
	if err != nil || n == 0 {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(http.DetectContentType(buf[:n])))
}

