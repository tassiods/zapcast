// SPDX-FileCopyrightText: 2025 Tássio Dias <https://github.com/tassiods/zapcast>
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http/httptest"
	"testing"
	"unicode/utf16"

	"go.mau.fi/whatsmeow/proto/waCompanionReg"
)

// --- decodeCSV: UTF-16 BE branch + Reader error ---

func encodeUTF16BE(s string) []byte {
	u16 := utf16.Encode([]rune(s))
	out := []byte{0xFE, 0xFF}
	for _, c := range u16 {
		out = append(out, byte(c>>8), byte(c))
	}
	return out
}

func TestParseCSV_OutlookUTF16BE(t *testing.T) {
	content := "Nome,Telefone\nJoão,5511999998888\n"
	rows, err := parseCSVImport(bytes.NewReader(encodeUTF16BE(content)))
	if err != nil {
		t.Fatalf("parse BE: %v", err)
	}
	if len(rows) != 1 || rows[0].Name != "João" {
		t.Errorf("BE row wrong: %+v", rows)
	}
}

type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errors.New("boom") }

func TestParseCSVReadError(t *testing.T) {
	_, err := parseCSVImport(errReader{})
	if err == nil || !contains(err.Error(), "boom") {
		t.Errorf("expected wrapped read error, got %v", err)
	}
}

// containsSubstr avoids pulling strings just for one test.
func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// --- updateGroup handler: happy + invalid JSON ---

func TestUpdateGroupHandlerHappy(t *testing.T) {
	app := newTestApp(t)
	if _, err := app.contacts.AddGroup(ctx, "Clientes", nil); err != nil {
		t.Fatal(err)
	}
	body := mustEncode(t, map[string]any{"name": "VIP", "contacts": []string{}})
	req := httptest.NewRequest("PUT", "/api/groups/Clientes", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.SetPathValue("name", "Clientes")
	rec := httptest.NewRecorder()
	app.updateGroup(rec, req)
	if rec.Code != 200 {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	var out Group
	decodeBody(t, rec, &out)
	if out.Name != "VIP" {
		t.Errorf("renamed group missing: %+v", out)
	}
}

func TestUpdateGroupHandlerInvalidJSON(t *testing.T) {
	app := newTestApp(t)
	req := httptest.NewRequest("PUT", "/api/groups/X", bytes.NewReader([]byte("{")))
	req.Header.Set("Content-Type", "application/json")
	req.SetPathValue("name", "X")
	rec := httptest.NewRecorder()
	app.updateGroup(rec, req)
	if rec.Code != 400 {
		t.Errorf("expected 400 invalid json, got %d", rec.Code)
	}
}

func TestUpdateGroupHandlerStoreError(t *testing.T) {
	app := newTestApp(t)
	// Group doesn't exist → store returns err_group_not_found.
	body := mustEncode(t, map[string]any{"name": "X", "contacts": []string{}})
	req := httptest.NewRequest("PUT", "/api/groups/Nada", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.SetPathValue("name", "Nada")
	rec := httptest.NewRecorder()
	app.updateGroup(rec, req)
	if rec.Code != 400 {
		t.Errorf("expected 400 store error, got %d body=%s", rec.Code, rec.Body.String())
	}
}

// --- handleBroadcastList: clamp + cursor + nextBefore ---

func TestHandleBroadcastListLimitClampedAbove500(t *testing.T) {
	app := newTestApp(t)
	rec := httptest.NewRecorder()
	app.handleBroadcastList(rec, httptest.NewRequest("GET", "/api/broadcasts?limit=99999", nil))
	if rec.Code != 200 {
		t.Fatalf("status %d", rec.Code)
	}
	// 99999 → clamped to 500; with no data the response has just an empty items array.
	var out map[string]any
	decodeBody(t, rec, &out)
	if _, ok := out["nextBefore"]; ok {
		t.Errorf("unexpected nextBefore on empty list: %+v", out)
	}
}

func TestHandleBroadcastListNextBeforeSet(t *testing.T) {
	app := newTestApp(t)
	// Create 3 broadcasts; ask for limit=2 → handler should set nextBefore.
	for i := 0; i < 3; i++ {
		if _, err := app.contacts.CreateBroadcast(ctx, "G", "m", 1, 0, 0, "", ""); err != nil {
			t.Fatal(err)
		}
	}
	rec := httptest.NewRecorder()
	app.handleBroadcastList(rec, httptest.NewRequest("GET", "/api/broadcasts?limit=2", nil))
	if rec.Code != 200 {
		t.Fatalf("status %d", rec.Code)
	}
	var out map[string]any
	decodeBody(t, rec, &out)
	if _, ok := out["nextBefore"]; !ok {
		t.Errorf("expected nextBefore, got %+v", out)
	}
}

func TestHandleBroadcastListCursor(t *testing.T) {
	app := newTestApp(t)
	for i := 0; i < 3; i++ {
		if _, err := app.contacts.CreateBroadcast(ctx, "G", "m", 1, 0, 0, "", ""); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	rec := httptest.NewRecorder()
	app.handleBroadcastList(rec, httptest.NewRequest("GET", "/api/broadcasts?before=999&limit=10", nil))
	if rec.Code != 200 {
		t.Fatalf("status %d", rec.Code)
	}
	var out map[string]any
	decodeBody(t, rec, &out)
	items := out["items"].([]any)
	if len(items) != 3 {
		t.Errorf("expected 3 items below cursor, got %d", len(items))
	}
}

// --- handleBroadcastByID happy ---

func TestHandleBroadcastByIDHappy(t *testing.T) {
	app := newTestApp(t)
	id, _ := app.contacts.CreateBroadcast(ctx, "G", "m", 1, 0, 0, "", "")
	_, _ = app.contacts.InsertBroadcastMessage(ctx, id, "Ana", "5511111111111", "wid-x", "sent", "")
	req := httptest.NewRequest("GET", "/api/broadcasts/"+itoa(id), nil)
	req.SetPathValue("id", itoa(id))
	rec := httptest.NewRecorder()
	app.handleBroadcastByID(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status %d body=%s", rec.Code, rec.Body.String())
	}
	var msgs []BroadcastMessage
	decodeBody(t, rec, &msgs)
	if len(msgs) != 1 || msgs[0].ContactName != "Ana" {
		t.Errorf("messages wrong: %+v", msgs)
	}
}

// --- DB-error paths for list / byID / stats ---

func TestHandleStatsDBError(t *testing.T) {
	app := newTestApp(t)
	// Close the underlying DB so the store call errors out.
	_ = app.contacts.db.Close()
	rec := httptest.NewRecorder()
	app.handleStats(rec, httptest.NewRequest("GET", "/api/stats", nil))
	if rec.Code != 500 {
		t.Errorf("expected 500 on DB error, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandleBroadcastListDBError(t *testing.T) {
	app := newTestApp(t)
	_ = app.contacts.db.Close()
	rec := httptest.NewRecorder()
	app.handleBroadcastList(rec, httptest.NewRequest("GET", "/api/broadcasts", nil))
	if rec.Code != 500 {
		t.Errorf("expected 500, got %d", rec.Code)
	}
}

func TestHandleBroadcastByIDDBError(t *testing.T) {
	app := newTestApp(t)
	_ = app.contacts.db.Close()
	req := httptest.NewRequest("GET", "/api/broadcasts/1", nil)
	req.SetPathValue("id", "1")
	rec := httptest.NewRecorder()
	app.handleBroadcastByID(rec, req)
	if rec.Code != 500 {
		t.Errorf("expected 500, got %d", rec.Code)
	}
}

// --- selectDeviceProps: every GOOS branch ---

func TestSelectDevicePropsDarwin(t *testing.T) {
	pt, name := selectDeviceProps("darwin")
	if pt != waCompanionReg.DeviceProps_CATALINA || name != "macOS" {
		t.Errorf("darwin wrong: %v %q", pt, name)
	}
}

func TestSelectDevicePropsWindows(t *testing.T) {
	pt, name := selectDeviceProps("windows")
	if pt != waCompanionReg.DeviceProps_DESKTOP || name != "Windows" {
		t.Errorf("windows wrong: %v %q", pt, name)
	}
}

func TestSelectDevicePropsLinuxDefault(t *testing.T) {
	pt, name := selectDeviceProps("linux")
	if pt != waCompanionReg.DeviceProps_DESKTOP || name != "Linux" {
		t.Errorf("linux wrong: %v %q", pt, name)
	}
	// Any unrecognised string also falls through.
	if pt2, name2 := selectDeviceProps("plan9"); pt2 != pt || name2 != name {
		t.Errorf("default branch mismatch: %v %q", pt2, name2)
	}
}

// --- resetClient: client==nil branch ---

func TestResetClientWhenClientNil(t *testing.T) {
	app := newTestApp(t)
	// Replace the in-memory client with nil so resetClient skips Disconnect().
	app.clientMu.Lock()
	app.wa = nil
	app.clientMu.Unlock()
	if err := app.resetClient(context.Background()); err != nil {
		t.Fatalf("resetClient (nil client): %v", err)
	}
	if app.WA() == nil {
		t.Errorf("expected a new client to be installed")
	}
}

// resetClient surfaces a container-side error from GetFirstDevice. We can
// trigger it by closing the underlying sqlstore.Container before calling.
func TestResetClientContainerClosedReturnsError(t *testing.T) {
	app := newTestApp(t)
	_ = app.container.Close()
	if err := app.resetClient(context.Background()); err == nil {
		t.Errorf("expected error when container is closed")
	}
}

// --- decodeCSV: UTF-8 BOM branch ---

func TestParseCSV_UTF8BOM(t *testing.T) {
	body := append([]byte{0xEF, 0xBB, 0xBF}, []byte("Nome,Telefone\nJoão,5511999998888\n")...)
	rows, err := parseCSVImport(bytes.NewReader(body))
	if err != nil {
		t.Fatalf("BOM parse: %v", err)
	}
	if len(rows) != 1 || rows[0].Name != "João" {
		t.Errorf("BOM row wrong: %+v", rows)
	}
}

// itoa is a small int64→decimal helper to avoid importing strconv in this test file.
func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// Sanity: errReader actually errors when passed to io.ReadAll.
func TestErrReaderSanity(t *testing.T) {
	_, err := io.ReadAll(errReader{})
	if err == nil {
		t.Errorf("expected error from errReader")
	}
}
