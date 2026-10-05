// SPDX-FileCopyrightText: 2025 ZapCast contributors
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"context"

	"go.mau.fi/whatsmeow"
	waProto "go.mau.fi/whatsmeow/binary/proto"
	"go.mau.fi/whatsmeow/types"
)

// WAClient is the narrow interface zapcast uses against a WhatsApp
// connection. The production binary wires to a real *whatsmeow.Client;
// tests inject a stub.
type WAClient interface {
	IsConnected() bool
	Connect() error
	Disconnect()
	Logout(ctx context.Context) error
	GetQRChannel(ctx context.Context) (<-chan whatsmeow.QRChannelItem, error)
	SendMessage(ctx context.Context, to types.JID, msg *waProto.Message) (whatsmeow.SendResponse, error)
	Upload(ctx context.Context, data []byte, mt whatsmeow.MediaType) (whatsmeow.UploadResponse, error)
	AddEventHandler(fn func(evt any)) uint32
	// JID returns the device's logged-in JID, or nil when unpaired.
	// Wraps the read of client.Store.ID, which whatsmeow guarantees is
	// write-once on pairing — no extra locking needed here.
	JID() *types.JID
}

// whatsmeowWA delegates every WAClient method to a real *whatsmeow.Client.
type whatsmeowWA struct{ c *whatsmeow.Client }

// NewWAClientFromWhatsmeow wraps a live *whatsmeow.Client so it satisfies
// WAClient. Used by main() and resetClient() — never by tests.
func NewWAClientFromWhatsmeow(c *whatsmeow.Client) WAClient {
	return &whatsmeowWA{c: c}
}

func (w *whatsmeowWA) IsConnected() bool { return w.c.IsConnected() }
func (w *whatsmeowWA) Connect() error    { return w.c.Connect() }
func (w *whatsmeowWA) Disconnect()       { w.c.Disconnect() }

func (w *whatsmeowWA) Logout(ctx context.Context) error { return w.c.Logout(ctx) }

func (w *whatsmeowWA) GetQRChannel(ctx context.Context) (<-chan whatsmeow.QRChannelItem, error) {
	return w.c.GetQRChannel(ctx)
}

// SendMessage drops the *whatsmeow.SendRequestExtra variadic — zapcast never
// passes one, and exposing it would force fakes to mirror an unused knob.
func (w *whatsmeowWA) SendMessage(ctx context.Context, to types.JID, msg *waProto.Message) (whatsmeow.SendResponse, error) {
	return w.c.SendMessage(ctx, to, msg)
}

func (w *whatsmeowWA) Upload(ctx context.Context, data []byte, mt whatsmeow.MediaType) (whatsmeow.UploadResponse, error) {
	return w.c.Upload(ctx, data, mt)
}

func (w *whatsmeowWA) AddEventHandler(fn func(evt any)) uint32 {
	return w.c.AddEventHandler(whatsmeow.EventHandler(fn))
}

func (w *whatsmeowWA) JID() *types.JID { return w.c.Store.ID }
