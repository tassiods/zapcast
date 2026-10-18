// SPDX-FileCopyrightText: 2025 Tássio Dias <https://github.com/tassiods/zapcast>
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"context"
	"testing"

	"go.mau.fi/whatsmeow"
	waProto "go.mau.fi/whatsmeow/binary/proto"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types"
	waLog "go.mau.fi/whatsmeow/util/log"
)

// TestWhatsmeowWAWrapperSmoke calls every wrapper method against a real
// disconnected *whatsmeow.Client to exercise the one-line delegations.
// Most methods return errors (no connection, not logged in) — we only
// assert that nothing panics and the return shape is sane. This guards
// against typos or wrong signatures in `whatsmeowWA` without needing a
// real WhatsApp session.
func TestWhatsmeowWAWrapperSmoke(t *testing.T) {
	container, err := sqlstore.New(context.Background(), "sqlite3",
		"file:wasmoketest?mode=memory&cache=shared&_foreign_keys=on", waLog.Noop)
	if err != nil {
		t.Fatalf("sqlstore.New: %v", err)
	}
	t.Cleanup(func() { _ = container.Close() })
	dev, err := container.GetFirstDevice(context.Background())
	if err != nil {
		t.Fatalf("GetFirstDevice: %v", err)
	}
	c := whatsmeow.NewClient(dev, waLog.Noop)
	wa := NewWAClientFromWhatsmeow(c)

	// IsConnected: false on fresh client.
	if wa.IsConnected() {
		t.Errorf("expected disconnected client")
	}
	// JID: nil before pairing.
	if wa.JID() != nil {
		t.Errorf("expected nil JID before pairing")
	}
	// AddEventHandler: returns a non-zero handler id.
	if id := wa.AddEventHandler(func(interface{}) {}); id == 0 {
		t.Errorf("AddEventHandler returned 0")
	}
	// Disconnect: safe on a not-yet-connected client.
	wa.Disconnect()

	// Logout: errors out because there's no logged-in session.
	if err := wa.Logout(context.Background()); err == nil {
		t.Errorf("expected Logout error on unpaired client")
	}
	// Connect: may error (no network from test), but must not panic.
	_ = wa.Connect()
	wa.Disconnect()

	// GetQRChannel: returns either chan + nil or nil + err. Either path is
	// acceptable; we just want the wrapper line executed.
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // pre-cancel so any internal loop tears down quickly
	_, _ = wa.GetQRChannel(ctx)

	// SendMessage / Upload: error out without a connection. Exercise once
	// to cover the delegation; ignore returned error.
	_, _ = wa.SendMessage(context.Background(), types.JID{User: "x", Server: types.DefaultUserServer}, &waProto.Message{})
	_, _ = wa.Upload(context.Background(), []byte("data"), whatsmeow.MediaImage)
}
