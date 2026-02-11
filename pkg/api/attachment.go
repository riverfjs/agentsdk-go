package api

// ResponseAttachment represents a file (image, document, etc.) that should be
// sent to the user as part of the agent's response.
//
// This is populated automatically by the runtime when tools return structured
// data containing file paths (e.g., screenshot tool returns {"path": "..."}).
//
// The channel layer is responsible for reading the file and sending it in the
// appropriate format (e.g., Telegram bot API, base64 for web, etc.).
type ResponseAttachment struct {
	// Type indicates the attachment category: "image", "file", "video", etc.
	Type string `json:"type"`

	// Path is the local filesystem path to the attachment.
	// The channel layer reads this file and sends it appropriately.
	Path string `json:"path"`

	// MimeType is the MIME type of the file (e.g., "image/png", "application/pdf").
	MimeType string `json:"mime_type,omitempty"`

	// Source identifies which tool generated this attachment (e.g., "browser/screenshot").
	// This is informational and can be used for logging or filtering.
	Source string `json:"source,omitempty"`

	// Metadata holds additional tool-specific information (e.g., {"fullPage": true}).
	Metadata map[string]interface{} `json:"metadata,omitempty"`
}

// AttachmentType constants for common attachment types.
const (
	AttachmentTypeImage    = "image"
	AttachmentTypeFile     = "file"
	AttachmentTypeVideo    = "video"
	AttachmentTypeAudio    = "audio"
	AttachmentTypeDocument = "document"
)

