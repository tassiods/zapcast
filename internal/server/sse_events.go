// SPDX-FileCopyrightText: 2025 ZapCast contributors
// SPDX-License-Identifier: Apache-2.0

package server

// Typed payloads for every SSE event the backend emits. Defining one struct
// per Type keeps producers honest (you can't forget a field) and gives the
// frontend a single place to read the on-the-wire contract from. The JSON
// envelope stays {"type":"...", "data":{...}}.

// BroadcastProgressData is emitted once per recipient as the broadcast
// goroutine works through the list.
type BroadcastProgressData struct {
	BroadcastID int64  `json:"broadcastId"`
	Current     int    `json:"current"`
	Total       int    `json:"total"`
	OK          int    `json:"ok"`
	Fail        int    `json:"fail"`
	Contact     string `json:"contact"`
	Success     bool   `json:"success"`
}

// BroadcastDoneData closes a broadcast. AbortedRemaining > 0 when the
// goroutine bailed out early (WhatsApp disconnect / SIGTERM); Error is
// non-empty when the upload itself failed.
type BroadcastDoneData struct {
	BroadcastID      int64  `json:"broadcastId"`
	Label            string `json:"label"`
	Total            int    `json:"total"`
	OK               int    `json:"ok"`
	Fail             int    `json:"fail"`
	Aborted          bool   `json:"aborted"`
	AbortedRemaining int    `json:"abortedRemaining"`
	Error            string `json:"error,omitempty"`
}

// BroadcastPausedData fires when the rolling-hour cap forces a pause.
type BroadcastPausedData struct {
	BroadcastID int64  `json:"broadcastId"`
	Reason      string `json:"reason"`
	ResumeAt    string `json:"resumeAt"`
	WaitSeconds int    `json:"waitSeconds"`
}

// ReceiptData is the delivery/read receipt forwarded from whatsmeow.
type ReceiptData struct {
	MessageID string `json:"messageId"`
	Status    string `json:"status"` // delivered | read
	From      string `json:"from"`
}

// newBroadcastDoneEvent assembles the SSE payload emitted when a broadcast
// goroutine finishes (normal, upload failure or cancellation).
func newBroadcastDoneEvent(broadcastID int64, label string, total, ok, fail, abortedRemaining int, aborted bool, errMsg string) Event {
	return Event{
		Type: "broadcast_done",
		Data: BroadcastDoneData{
			BroadcastID:      broadcastID,
			Label:            label,
			Total:            total,
			OK:               ok,
			Fail:             fail,
			Aborted:          aborted,
			AbortedRemaining: abortedRemaining,
			Error:            errMsg,
		},
	}
}
