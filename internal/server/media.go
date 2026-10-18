// SPDX-FileCopyrightText: 2025 Tássio Dias <https://github.com/tassiods/zapcast>
// SPDX-License-Identifier: Apache-2.0

package server

// Media classification + waProto wrapping for broadcast attachments.

import (
	"strings"

	"go.mau.fi/whatsmeow"
	waProto "go.mau.fi/whatsmeow/binary/proto"
	"google.golang.org/protobuf/proto"
)

// classifyMedia maps a MIME type to whatsmeow.MediaType + the short kind
// used in DB rows / SSE events / history chips. Unknown types fall through
// to MediaDocument so the recipient still gets the file. Returns ok=false
// only for empty input.
func classifyMedia(mime string) (mediaType whatsmeow.MediaType, kind string, ok bool) {
	if mime == "" {
		return "", "", false
	}
	switch strings.SplitN(mime, "/", 2)[0] {
	case "image":
		return whatsmeow.MediaImage, "image", true
	case "video":
		return whatsmeow.MediaVideo, "video", true
	case "audio":
		return whatsmeow.MediaAudio, "audio", true
	default:
		return whatsmeow.MediaDocument, "document", true
	}
}

// buildMediaMessage wraps an UploadResponse into the proper waProto.Message
// variant for `kind`. The text is used as Caption for image/video/document;
// audio drops the caption (WhatsApp clients don't render captions on PTT/audio).
// For text-only sends the caller bypasses this helper and builds a
// Conversation message directly.
func buildMediaMessage(text, kind string, resp whatsmeow.UploadResponse, mime, filename string) *waProto.Message {
	fileLen := resp.FileLength
	switch kind {
	case "image":
		return &waProto.Message{ImageMessage: &waProto.ImageMessage{
			URL:           proto.String(resp.URL),
			DirectPath:    proto.String(resp.DirectPath),
			MediaKey:      resp.MediaKey,
			FileEncSHA256: resp.FileEncSHA256,
			FileSHA256:    resp.FileSHA256,
			FileLength:    &fileLen,
			Mimetype:      proto.String(mime),
			Caption:       optionalCaption(text),
		}}
	case "video":
		return &waProto.Message{VideoMessage: &waProto.VideoMessage{
			URL:           proto.String(resp.URL),
			DirectPath:    proto.String(resp.DirectPath),
			MediaKey:      resp.MediaKey,
			FileEncSHA256: resp.FileEncSHA256,
			FileSHA256:    resp.FileSHA256,
			FileLength:    &fileLen,
			Mimetype:      proto.String(mime),
			Caption:       optionalCaption(text),
		}}
	case "audio":
		return &waProto.Message{AudioMessage: &waProto.AudioMessage{
			URL:           proto.String(resp.URL),
			DirectPath:    proto.String(resp.DirectPath),
			MediaKey:      resp.MediaKey,
			FileEncSHA256: resp.FileEncSHA256,
			FileSHA256:    resp.FileSHA256,
			FileLength:    &fileLen,
			Mimetype:      proto.String(mime),
		}}
	default: // document
		return &waProto.Message{DocumentMessage: &waProto.DocumentMessage{
			URL:           proto.String(resp.URL),
			DirectPath:    proto.String(resp.DirectPath),
			MediaKey:      resp.MediaKey,
			FileEncSHA256: resp.FileEncSHA256,
			FileSHA256:    resp.FileSHA256,
			FileLength:    &fileLen,
			Mimetype:      proto.String(mime),
			FileName:      proto.String(filename),
			Caption:       optionalCaption(text),
		}}
	}
}

// optionalCaption returns a *string only when s is non-empty. Captions
// default to nil so encoding/proto omits the field for empty messages.
func optionalCaption(s string) *string {
	if s == "" {
		return nil
	}
	return proto.String(s)
}
