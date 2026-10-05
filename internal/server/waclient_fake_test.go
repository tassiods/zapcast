// SPDX-FileCopyrightText: 2025 ZapCast contributors
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"context"
	"errors"
	"sync"

	"go.mau.fi/whatsmeow"
	waProto "go.mau.fi/whatsmeow/binary/proto"
	"go.mau.fi/whatsmeow/types"
)

// fakeWA is a programmable WAClient stand-in for unit tests. Every observable
// effect — IsConnected return, Logout error, sequence of SendMessage results —
// is driven by exported fields the test populates before exercising the
// handler under test. SendMessage and Upload calls are captured so the test
// can assert what was actually sent.
type fakeWA struct {
	mu sync.Mutex

	connected      bool
	jid            *types.JID
	qrChan         chan whatsmeow.QRChannelItem
	qrChanErr      error
	connectErr     error
	logoutErr      error
	uploadResp     whatsmeow.UploadResponse
	uploadErr      error
	sendCalls      []sendCall
	sendResp       whatsmeow.SendResponse
	sendErrAtIndex map[int]error
	sendErrAll     error
	eventHandler   func(evt interface{})
	disconnectN    int
}

type sendCall struct {
	JID types.JID
	Msg *waProto.Message
}

func (f *fakeWA) IsConnected() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.connected
}

func (f *fakeWA) Connect() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.connectErr != nil {
		return f.connectErr
	}
	f.connected = true
	return nil
}

func (f *fakeWA) Disconnect() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.connected = false
	f.disconnectN++
}

func (f *fakeWA) Logout(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.logoutErr
}

func (f *fakeWA) GetQRChannel(ctx context.Context) (<-chan whatsmeow.QRChannelItem, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.qrChanErr != nil {
		return nil, f.qrChanErr
	}
	if f.qrChan == nil {
		f.qrChan = make(chan whatsmeow.QRChannelItem, 8)
	}
	return f.qrChan, nil
}

func (f *fakeWA) SendMessage(ctx context.Context, to types.JID, msg *waProto.Message) (whatsmeow.SendResponse, error) {
	if err := ctx.Err(); err != nil {
		return whatsmeow.SendResponse{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	idx := len(f.sendCalls)
	f.sendCalls = append(f.sendCalls, sendCall{JID: to, Msg: msg})
	if f.sendErrAll != nil {
		return whatsmeow.SendResponse{}, f.sendErrAll
	}
	if err, ok := f.sendErrAtIndex[idx]; ok {
		return whatsmeow.SendResponse{}, err
	}
	resp := f.sendResp
	if resp.ID == "" {
		resp.ID = "fake-msg-id"
	}
	return resp, nil
}

func (f *fakeWA) Upload(ctx context.Context, _ []byte, _ whatsmeow.MediaType) (whatsmeow.UploadResponse, error) {
	if err := ctx.Err(); err != nil {
		return whatsmeow.UploadResponse{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.uploadErr != nil {
		return whatsmeow.UploadResponse{}, f.uploadErr
	}
	return f.uploadResp, nil
}

func (f *fakeWA) AddEventHandler(fn func(evt interface{})) uint32 {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.eventHandler = fn
	return 1
}

func (f *fakeWA) JID() *types.JID {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.jid
}

// errBoom is a small reusable sentinel for tests that need a non-nil error.
var errBoom = errors.New("boom")
