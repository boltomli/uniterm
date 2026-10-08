package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg" // registered for image.DecodeConfig
	_ "image/png"  // registered for image.DecodeConfig
	"strings"
)

// Attachment support for the AI assistant.
//
// uniTerm is a model proxy: it never performs OCR, image recognition or any
// other multimodal inference itself. Attachments are validated here and then
// forwarded verbatim to the upstream multimodal model, which does the actual
// understanding. The frontend builds Anthropic-format content blocks; this
// file is the backend half — the safety net that keeps a malformed or
// oversized payload from reaching (and being rejected by) the upstream API,
// plus the block translation the OpenAI / Responses converters need.
//
// The limits below are mirrored in frontend/src/services/attachments.ts
// (ATTACHMENT_LIMITS). Keep both in sync: the frontend owns the user-facing
// validation messages, this file owns the enforcement that cannot be bypassed
// from the renderer.
const (
	// AttachmentMaxImageBytes caps one image attachment (5 MiB decoded).
	AttachmentMaxImageBytes = 5 * 1024 * 1024
	// AttachmentMaxTextBytes caps one text-file attachment (5 MiB).
	AttachmentMaxTextBytes = 5 * 1024 * 1024
	// AttachmentMaxImageSide is the longest-edge pixel cap for one image.
	// Providers reject oversized frames independently of byte size —
	// Anthropic refuses anything above 8000 x 8000 px — and a tall or wide
	// screenshot can easily be under the byte cap yet over this one.
	AttachmentMaxImageSide = 8000
	// AttachmentMaxImagesPerMessage is the hard ceiling on images inside one
	// upstream message. The product rule is AttachmentImagesPerMessage (see
	// below), but a single API message can legitimately carry more: aiStore
	// merges consecutive user messages when an assistant turn produced nothing
	// (a failed request, a cancelled turn), so two pasted screenshots from two
	// turns become one message. This ceiling only stops a runaway payload; it
	// is deliberately above the product rule so that legitimate history is
	// never rejected.
	AttachmentMaxImagesPerMessage = 8
	// AttachmentMaxTextFilesPerMessage caps text files per user message.
	AttachmentMaxTextFilesPerMessage = 3
)

// Sentinel errors surfaced to the frontend. The renderer matches on these
// prefixes and renders a localized, human-readable message — upstream 400
// bodies are never shown to the user for attachment failures.
var (
	errAttachmentTooLarge        = errors.New("AI_ATTACHMENT_TOO_LARGE")
	errAttachmentUnsupportedType = errors.New("AI_ATTACHMENT_UNSUPPORTED_TYPE")
	errAttachmentTooMany         = errors.New("AI_ATTACHMENT_TOO_MANY")
	errAttachmentEmpty           = errors.New("AI_ATTACHMENT_EMPTY")
	errAttachmentInvalid         = errors.New("AI_ATTACHMENT_INVALID")
	// errAttachmentTooLargePixels is the pixel-side counterpart of
	// errAttachmentTooLarge: the bytes fit but the frame does not.
	errAttachmentTooLargePixels = errors.New("AI_ATTACHMENT_TOO_LARGE_PIXELS")
	// errAttachmentUnsupportedByModel is returned instead of the raw upstream
	// body when a request carrying images is rejected in a way that indicates
	// the target model / endpoint has no vision support.
	errAttachmentUnsupportedByModel = errors.New("AI_ATTACHMENT_UNSUPPORTED_BY_MODEL")
)

// attachmentImageMediaTypes is the image whitelist. Mirrors
// ATTACHMENT_IMAGE_TYPES in the frontend service.
var attachmentImageMediaTypes = map[string]bool{
	"image/png":  true,
	"image/jpeg": true,
	"image/jpg":  true,
}

// anthropicImageBlockToDataURL renders an Anthropic image content block as a
// data URL and enforces the image whitelist / size cap.
//
//	{"type":"image","source":{"type":"base64","media_type":"image/png","data":"..."}}
//	  -> "data:image/png;base64,..."
//
// Only base64 sources are accepted: the renderer always inlines clipboard and
// file bytes, so a remote URL source would mean the model fetches an
// arbitrary address on the user's behalf.
func anthropicImageBlockToDataURL(block map[string]interface{}) (string, error) {
	source, ok := block["source"].(map[string]interface{})
	if !ok {
		return "", errAttachmentInvalid
	}
	if srcType, _ := source["type"].(string); srcType != "base64" {
		return "", errAttachmentUnsupportedType
	}

	mediaType := strings.ToLower(strings.TrimSpace(stringField(source, "media_type")))
	if !attachmentImageMediaTypes[mediaType] {
		return "", errAttachmentUnsupportedType
	}

	data := strings.TrimSpace(stringField(source, "data"))
	if data == "" {
		return "", errAttachmentEmpty
	}

	// Decode to measure the *real* byte count: base64 length alone is a poor
	// proxy (padding would push a file that is exactly at the limit over it).
	// This also rejects payloads that are not valid base64 before they reach
	// the upstream API.
	decoded, err := base64.StdEncoding.DecodeString(data)
	if err != nil {
		return "", errAttachmentInvalid
	}
	if len(decoded) == 0 {
		return "", errAttachmentEmpty
	}
	if len(decoded) > AttachmentMaxImageBytes {
		return "", errAttachmentTooLarge
	}

	// Byte size is only half of the upstream contract: providers also cap the
	// frame itself (Anthropic: 8000 x 8000 px), and a long screenshot is often
	// tiny in bytes yet thousands of pixels tall. DecodeConfig reads just the
	// image header, so this stays cheap. The whitelist has already pinned the
	// media type to png/jpeg, so the registered decoders cover every payload
	// that can get here; an undecodable header means the "image" is corrupt
	// and would be rejected upstream anyway.
	cfg, _, err := image.DecodeConfig(bytes.NewReader(decoded))
	if err != nil {
		return "", errAttachmentInvalid
	}
	if cfg.Width > AttachmentMaxImageSide || cfg.Height > AttachmentMaxImageSide {
		return "", errAttachmentTooLargePixels
	}

	return "data:" + mediaType + ";base64," + data, nil
}

func stringField(m map[string]interface{}, key string) string {
	s, _ := m[key].(string)
	return s
}

// validateRequestAttachments walks every message in an Anthropic-format
// request and enforces the per-message attachment limits. Runs before any
// upstream call so the user gets a readable, localized message instead of a
// provider 400.
func validateRequestAttachments(reqBody map[string]interface{}) error {
	msgs, ok := reqBody["messages"].([]interface{})
	if !ok {
		return nil
	}
	for _, m := range msgs {
		msg, ok := m.(map[string]interface{})
		if !ok {
			continue
		}
		blocks, ok := msg["content"].([]interface{})
		if !ok {
			continue
		}

		images := 0
		for _, raw := range blocks {
			block, ok := raw.(map[string]interface{})
			if !ok {
				continue
			}
			if bType, _ := block["type"].(string); bType != "image" {
				continue
			}
			images++
			if images > AttachmentMaxImagesPerMessage {
				return errAttachmentTooMany
			}
			if _, err := anthropicImageBlockToDataURL(block); err != nil {
				return err
			}
		}
	}
	return nil
}

// requestHasImageAttachment reports whether any message carries an image
// block. Used to decide how to phrase an upstream rejection.
func requestHasImageAttachment(reqBody map[string]interface{}) bool {
	msgs, ok := reqBody["messages"].([]interface{})
	if !ok {
		return false
	}
	for _, m := range msgs {
		msg, ok := m.(map[string]interface{})
		if !ok {
			continue
		}
		blocks, ok := msg["content"].([]interface{})
		if !ok {
			continue
		}
		for _, raw := range blocks {
			if block, ok := raw.(map[string]interface{}); ok {
				if bType, _ := block["type"].(string); bType == "image" {
					return true
				}
			}
		}
	}
	return false
}

// visionRejectionPhrases are the ways providers say "I can't take an image".
var (
	visionSubjectWords = []string{"image", "vision", "multimodal", "content type"}
	visionRejectionPhrases = []string{
		"not support", "unsupported", "not allowed", "invalid", "unknown", "unrecognized",
	}
	// sizeRejectionPhrases are the ways providers say "this image is too
	// big" — in pixels, since bytes are already capped before dispatch.
	// Both halves must match: an image subject and a size phrase, so a
	// "max_tokens is too large" error is never misclassified.
	sizeRejectionPhrases = []string{
		"exceed", "too large", "dimension", "resolution", "width", "height", "pixel",
	}
)

// isImageSizeRejection reports whether an upstream error message means "the
// image frame is over the provider's pixel limits" rather than "no vision".
// The local pre-flight uses the Anthropic limits, but other providers cap
// tighter, so a frame that passes locally can still be refused upstream.
func isImageSizeRejection(detail string) bool {
	lower := strings.ToLower(detail)
	if !isVisionSubject(lower) {
		return false
	}
	for _, phrase := range sizeRejectionPhrases {
		if strings.Contains(lower, phrase) {
			return true
		}
	}
	return false
}

// isVisionSubject reports whether the error text is about images at all.
func isVisionSubject(lower string) bool {
	for _, word := range visionSubjectWords {
		if strings.Contains(lower, word) {
			return true
		}
	}
	return false
}

// isVisionRejection reports whether an upstream error message means "this model
// cannot take images" rather than a genuine request bug. Providers word this
// both ways round ("does not support image input", "invalid image_url"), so the
// check is order-independent.
func isVisionRejection(detail string) bool {
	lower := strings.ToLower(detail)
	if !isVisionSubject(lower) {
		return false
	}
	for _, phrase := range visionRejectionPhrases {
		if strings.Contains(lower, phrase) {
			return true
		}
	}
	return false
}

// upstreamError turns a non-200 upstream response into an error the UI can
// render. Attachment-bearing requests that fail with a vision-related 4xx are
// reported as errAttachmentUnsupportedByModel (plus the upstream's own
// human-readable `error.message`, never the raw JSON/HTML envelope); a
// pixel-limit refusal gets its own sentinel so the UI can suggest downscaling.
// Everything else keeps the pre-existing "HTTP <code>: <body>" behaviour.
func upstreamError(status int, body []byte, hasImages bool) error {
	if hasImages && status >= 400 && status < 500 {
		detail := upstreamErrorDetail(body)
		if isVisionRejection(detail) {
			return fmt.Errorf("%w: %s", errAttachmentUnsupportedByModel, detail)
		}
		if isImageSizeRejection(detail) {
			return fmt.Errorf("%w: %s", errAttachmentTooLargePixels, detail)
		}
	}
	return fmt.Errorf("HTTP %d: %s", status, string(body))
}

// upstreamErrorDetail extracts the human-readable message from an OpenAI- or
// Anthropic-shaped error body, collapsing whitespace and truncating so a
// hostile or HTML-returning gateway cannot flood the chat panel.
func upstreamErrorDetail(body []byte) string {
	var parsed struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
		Message string `json:"message"`
	}
	detail := ""
	if json.Unmarshal(body, &parsed) == nil {
		switch {
		case parsed.Error.Message != "":
			detail = parsed.Error.Message
		case parsed.Message != "":
			detail = parsed.Message
		}
	}
	detail = strings.Join(strings.Fields(detail), " ")
	if len(detail) > 300 {
		detail = detail[:300] + "…"
	}
	return detail
}
