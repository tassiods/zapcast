// SPDX-FileCopyrightText: 2025 Tássio Dias <https://github.com/tassiods/zapcast>
// SPDX-License-Identifier: Apache-2.0

package server

// Sanity checks that the WAClient fake honours ctx cancellation. The
// production *whatsmeow.Client surfaces a wrapped ctx error too — these
// tests pin the fake's behaviour so future cancellation-path tests don't
// silently degrade into happy-path tests.

import (
	"context"
	"errors"
	"testing"

	"go.mau.fi/whatsmeow"
	waProto "go.mau.fi/whatsmeow/binary/proto"
	"go.mau.fi/whatsmeow/types"
)

func TestFakeWASendMessageRespectsContext(t *testing.T) {
	wa := &fakeWA{connected: true}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := wa.SendMessage(ctx, types.NewJID("5511999998888", types.DefaultUserServer), &waProto.Message{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
	if got := len(wa.sendCalls); got != 0 {
		t.Errorf("cancelled SendMessage must not record a call; got %d", got)
	}
}

func TestFakeWAUploadRespectsContext(t *testing.T) {
	wa := &fakeWA{connected: true}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := wa.Upload(ctx, []byte("x"), whatsmeow.MediaImage)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

func TestFakeWAGetQRChannelRespectsContext(t *testing.T) {
	wa := &fakeWA{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := wa.GetQRChannel(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

func TestFakeWALogoutRespectsContext(t *testing.T) {
	wa := &fakeWA{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := wa.Logout(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}
