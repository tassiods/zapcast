// SPDX-FileCopyrightText: 2025 Tássio Dias <https://github.com/tassiods/zapcast>
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types"
	waLog "go.mau.fi/whatsmeow/util/log"

	_ "github.com/mattn/go-sqlite3"
)

// --- Test helpers ---

// newTestApp builds a minimal App with a real ContactStore + a disconnected
// whatsmeow client over an in-memory sqlstore. Suitable for HTTP handler
// tests that don't need an active WhatsApp connection: any code path gated
// on client.IsConnected() will take the false branch.
//
// For tests that want richer behaviour (programmed Upload / SendMessage /
// QR channel responses), use newTestAppWithWA and pass a *fakeWA.
func newTestApp(t *testing.T) *App {
	t.Helper()
	container, err := sqlstore.New(context.Background(), "sqlite3",
		"file:zapcasttest_"+t.Name()+"?mode=memory&cache=shared&_foreign_keys=on", waLog.Noop)
	if err != nil {
		t.Fatalf("sqlstore.New: %v", err)
	}
	t.Cleanup(func() { _ = container.Close() })
	deviceStore, err := container.GetFirstDevice(context.Background())
	if err != nil {
		t.Fatalf("GetFirstDevice: %v", err)
	}
	client := whatsmeow.NewClient(deviceStore, waLog.Noop)
	shutdownCtx, shutdownStop := context.WithCancel(context.Background())
	t.Cleanup(shutdownStop)
	return &App{
		logger:       testLogger,
		container:    container,
		wa:           NewWAClientFromWhatsmeow(client),
		contacts:     newTestStore(t),
		hub:          NewSSEHub(testLogger),
		broadcaster:  NewBroadcaster(testLogger, 10*time.Millisecond, 0, 0),
		shutdownCtx:  shutdownCtx,
		shutdownStop: shutdownStop,
	}
}

// newTestAppWithWA builds an App backed by an arbitrary WAClient. Skips the
// whatsmeow sqlstore setup entirely — the only WhatsApp surface area in the
// resulting App is whatever the supplied WAClient exposes.
func newTestAppWithWA(t *testing.T, wa WAClient) *App {
	t.Helper()
	shutdownCtx, shutdownStop := context.WithCancel(context.Background())
	t.Cleanup(shutdownStop)
	return &App{
		logger:       testLogger,
		wa:           wa,
		contacts:     newTestStore(t),
		hub:          NewSSEHub(testLogger),
		broadcaster:  NewBroadcaster(testLogger, 0, 0, 0),
		shutdownCtx:  shutdownCtx,
		shutdownStop: shutdownStop,
	}
}

func mustEncode(t *testing.T, v interface{}) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

func decodeBody(t *testing.T, rec *httptest.ResponseRecorder, into interface{}) {
	t.Helper()
	if err := json.Unmarshal(rec.Body.Bytes(), into); err != nil {
		t.Fatalf("decode body (%s): %v", rec.Body.String(), err)
	}
}

// --- buildBroadcastDoneEvent ---

func TestBuildBroadcastDoneEventSuccess(t *testing.T) {
	ev := buildBroadcastDoneEvent(7, "Clientes", 5, 4, 1, 0, false, "")
	if ev.Type != "broadcast_done" {
		t.Fatalf("type = %q", ev.Type)
	}
	d := ev.Data.(BroadcastDoneData)
	if d.BroadcastID != 7 || d.Label != "Clientes" ||
		d.Total != 5 || d.OK != 4 || d.Fail != 1 ||
		d.AbortedRemaining != 0 || d.Aborted {
		t.Errorf("payload mismatch: %+v", d)
	}
	if d.Error != "" {
		t.Errorf("error field must be empty on success, got %q", d.Error)
	}
}

func TestBuildBroadcastDoneEventWithError(t *testing.T) {
	ev := buildBroadcastDoneEvent(9, "VIP", 3, 0, 3, 0, false, "upload_failed")
	d := ev.Data.(BroadcastDoneData)
	if d.Error != "upload_failed" {
		t.Errorf("error = %q, want upload_failed", d.Error)
	}
}

func TestBuildBroadcastDoneEventAborted(t *testing.T) {
	ev := buildBroadcastDoneEvent(2, "Friends", 10, 4, 0, 6, true, "")
	d := ev.Data.(BroadcastDoneData)
	if !d.Aborted || d.AbortedRemaining != 6 {
		t.Errorf("aborted flags wrong: %+v", d)
	}
}

// --- optionalCaption + buildMediaMessage ---

func TestOptionalCaption(t *testing.T) {
	if optionalCaption("") != nil {
		t.Errorf("empty should return nil")
	}
	got := optionalCaption("hello")
	if got == nil || *got != "hello" {
		t.Errorf("non-empty should return pointer to string")
	}
}

func TestBuildMediaMessageImage(t *testing.T) {
	resp := whatsmeow.UploadResponse{
		URL:           "https://mmg.whatsapp.net/x",
		DirectPath:    "/d/path",
		MediaKey:      []byte{1, 2, 3},
		FileEncSHA256: []byte{4, 5},
		FileSHA256:    []byte{6, 7},
		FileLength:    1234,
	}
	msg := buildMediaMessage("hi", "image", resp, "image/png", "pic.png")
	if msg.ImageMessage == nil {
		t.Fatal("ImageMessage missing")
	}
	if msg.ImageMessage.GetURL() != resp.URL {
		t.Errorf("URL wrong: %q", msg.ImageMessage.GetURL())
	}
	if msg.ImageMessage.GetCaption() != "hi" {
		t.Errorf("caption wrong: %q", msg.ImageMessage.GetCaption())
	}
	if msg.ImageMessage.GetFileLength() != 1234 {
		t.Errorf("file length wrong: %d", msg.ImageMessage.GetFileLength())
	}
}

func TestBuildMediaMessageAudioDropsCaption(t *testing.T) {
	resp := whatsmeow.UploadResponse{URL: "u", FileLength: 10}
	msg := buildMediaMessage("ignored", "audio", resp, "audio/ogg", "voice.ogg")
	if msg.AudioMessage == nil {
		t.Fatal("AudioMessage missing")
	}
	// AudioMessage doesn't expose Caption — but we should not panic and URL must hold.
	if msg.AudioMessage.GetURL() != "u" {
		t.Errorf("URL wrong")
	}
}

func TestBuildMediaMessageDocumentCarriesFilename(t *testing.T) {
	resp := whatsmeow.UploadResponse{URL: "u", FileLength: 99}
	msg := buildMediaMessage("desc", "document", resp, "application/pdf", "file.pdf")
	if msg.DocumentMessage == nil {
		t.Fatal("DocumentMessage missing")
	}
	if msg.DocumentMessage.GetFileName() != "file.pdf" {
		t.Errorf("filename wrong: %q", msg.DocumentMessage.GetFileName())
	}
	if msg.DocumentMessage.GetCaption() != "desc" {
		t.Errorf("caption wrong: %q", msg.DocumentMessage.GetCaption())
	}
}

func TestBuildMediaMessageVideoUnknownFallsToDoc(t *testing.T) {
	resp := whatsmeow.UploadResponse{URL: "u"}
	if msg := buildMediaMessage("v", "video", resp, "video/mp4", ""); msg.VideoMessage == nil {
		t.Error("video case missing VideoMessage")
	}
	if msg := buildMediaMessage("x", "unknown-kind", resp, "x/x", "f.bin"); msg.DocumentMessage == nil {
		t.Error("unknown kind should fall back to DocumentMessage")
	}
}

// --- Contacts handlers ---

func newRequest(method, target string, body []byte) *http.Request {
	var rb io.Reader
	if body != nil {
		rb = bytes.NewReader(body)
	}
	req := httptest.NewRequest(method, target, rb)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return req
}

func TestCreateContactHappy(t *testing.T) {
	app := newTestApp(t)
	body := mustEncode(t, map[string]any{"name": "João", "phone": "5511999998888"})
	req := newRequest("POST", "/api/contacts", body)
	rec := httptest.NewRecorder()
	app.createContact(rec, req)
	if rec.Code != 201 {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body.String())
	}
	var c Contact
	decodeBody(t, rec, &c)
	if c.Name != "João" || c.Phone != "5511999998888" {
		t.Errorf("contact wrong: %+v", c)
	}
}

func TestCreateContactValidationMissingPhone(t *testing.T) {
	app := newTestApp(t)
	body := mustEncode(t, map[string]any{"name": "João"})
	req := newRequest("POST", "/api/contacts", body)
	rec := httptest.NewRecorder()
	app.createContact(rec, req)
	if rec.Code != 400 {
		t.Errorf("expected 400, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestCreateContactInvalidJSON(t *testing.T) {
	app := newTestApp(t)
	req := newRequest("POST", "/api/contacts", []byte("{not json"))
	rec := httptest.NewRecorder()
	app.createContact(rec, req)
	if rec.Code != 400 {
		t.Errorf("expected 400, got %d", rec.Code)
	}
}

func TestCreateContactDuplicateReturns400(t *testing.T) {
	app := newTestApp(t)
	if _, err := app.contacts.AddContact(ctx, "João", "5511999998888", nil); err != nil {
		t.Fatal(err)
	}
	body := mustEncode(t, map[string]any{"name": "João", "phone": "5511999998888"})
	rec := httptest.NewRecorder()
	app.createContact(rec, newRequest("POST", "/api/contacts", body))
	if rec.Code != 400 {
		t.Errorf("expected 400 dup, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestListContactsEmpty(t *testing.T) {
	app := newTestApp(t)
	rec := httptest.NewRecorder()
	app.listContacts(rec, httptest.NewRequest("GET", "/api/contacts", nil))
	if rec.Code != 200 {
		t.Fatalf("status %d", rec.Code)
	}
	var out []Contact
	decodeBody(t, rec, &out)
	if len(out) != 0 {
		t.Errorf("expected empty, got %+v", out)
	}
}

func TestListContactsSearchAndPagination(t *testing.T) {
	app := newTestApp(t)
	_, _ = app.contacts.AddContact(ctx, "Ana", "5511111111111", nil)
	_, _ = app.contacts.AddContact(ctx, "Bruno", "5511222222222", nil)
	_, _ = app.contacts.AddContact(ctx, "Carlos", "5511333333333", nil)

	// Search filter
	rec := httptest.NewRecorder()
	app.listContacts(rec, httptest.NewRequest("GET", "/api/contacts?q=ana", nil))
	if rec.Code != 200 {
		t.Fatalf("status %d", rec.Code)
	}
	var out []Contact
	decodeBody(t, rec, &out)
	if len(out) != 1 || out[0].Name != "Ana" {
		t.Errorf("search wrong: %+v", out)
	}

	// Pagination envelope
	rec = httptest.NewRecorder()
	app.listContacts(rec, httptest.NewRequest("GET", "/api/contacts?page=1&limit=2", nil))
	if rec.Code != 200 {
		t.Fatalf("status %d", rec.Code)
	}
	var env map[string]any
	decodeBody(t, rec, &env)
	if env["total"].(float64) != 3 || env["page"].(float64) != 1 || env["limit"].(float64) != 2 {
		t.Errorf("envelope wrong: %+v", env)
	}
	items := env["items"].([]any)
	if len(items) != 2 {
		t.Errorf("expected 2 items in first page, got %d", len(items))
	}
}

func TestUpdateContactHandlerNotFound(t *testing.T) {
	app := newTestApp(t)
	body := mustEncode(t, map[string]any{"name": "X"})
	req := httptest.NewRequest("PUT", "/api/contacts/5511000000000", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.SetPathValue("phone", "5511000000000")
	rec := httptest.NewRecorder()
	app.updateContact(rec, req)
	if rec.Code != 404 {
		t.Errorf("expected 404, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestUpdateContactMissingPath(t *testing.T) {
	app := newTestApp(t)
	req := httptest.NewRequest("PUT", "/api/contacts/", nil)
	req.SetPathValue("phone", "")
	rec := httptest.NewRecorder()
	app.updateContact(rec, req)
	if rec.Code != 400 {
		t.Errorf("expected 400, got %d", rec.Code)
	}
}

func TestDeleteContactNotFoundReturns404(t *testing.T) {
	app := newTestApp(t)
	req := httptest.NewRequest("DELETE", "/api/contacts/5511000000000", nil)
	req.SetPathValue("phone", "5511000000000")
	rec := httptest.NewRecorder()
	app.deleteContact(rec, req)
	if rec.Code != 404 {
		t.Errorf("expected 404 for missing contact, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestDeleteContactMissingPath(t *testing.T) {
	app := newTestApp(t)
	req := httptest.NewRequest("DELETE", "/api/contacts/", nil)
	req.SetPathValue("phone", "")
	rec := httptest.NewRecorder()
	app.deleteContact(rec, req)
	if rec.Code != 400 {
		t.Errorf("expected 400 for missing path, got %d", rec.Code)
	}
}

func TestDeleteContactHappy(t *testing.T) {
	app := newTestApp(t)
	_, _ = app.contacts.AddContact(ctx, "João", "5511999998888", nil)
	req := httptest.NewRequest("DELETE", "/api/contacts/5511999998888", nil)
	req.SetPathValue("phone", "5511999998888")
	rec := httptest.NewRecorder()
	app.deleteContact(rec, req)
	if rec.Code != 200 {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
}

// --- Groups handlers ---

func TestCreateGroupHappy(t *testing.T) {
	app := newTestApp(t)
	body := mustEncode(t, map[string]any{"name": "Clientes"})
	rec := httptest.NewRecorder()
	app.createGroup(rec, newRequest("POST", "/api/groups", body))
	if rec.Code != 201 {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
}

func TestCreateGroupMissingName(t *testing.T) {
	app := newTestApp(t)
	rec := httptest.NewRecorder()
	app.createGroup(rec, newRequest("POST", "/api/groups", mustEncode(t, map[string]any{})))
	if rec.Code != 400 {
		t.Errorf("expected 400, got %d", rec.Code)
	}
}

func TestUpdateGroupMissingPath(t *testing.T) {
	app := newTestApp(t)
	req := httptest.NewRequest("PUT", "/api/groups/", nil)
	req.SetPathValue("name", "")
	rec := httptest.NewRecorder()
	app.updateGroup(rec, req)
	if rec.Code != 400 {
		t.Errorf("expected 400, got %d", rec.Code)
	}
}

func TestDeleteGroupHappy(t *testing.T) {
	app := newTestApp(t)
	_, _ = app.contacts.AddGroup(ctx, "Clientes", nil)
	req := httptest.NewRequest("DELETE", "/api/groups/Clientes", nil)
	req.SetPathValue("name", "Clientes")
	rec := httptest.NewRecorder()
	app.deleteGroup(rec, req)
	if rec.Code != 200 {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestListGroupsPagination(t *testing.T) {
	app := newTestApp(t)
	_, _ = app.contacts.AddGroup(ctx, "A", nil)
	_, _ = app.contacts.AddGroup(ctx, "B", nil)
	rec := httptest.NewRecorder()
	app.listGroups(rec, httptest.NewRequest("GET", "/api/groups?page=1&limit=1", nil))
	if rec.Code != 200 {
		t.Fatalf("status %d", rec.Code)
	}
	var env map[string]any
	decodeBody(t, rec, &env)
	if env["total"].(float64) != 2 {
		t.Errorf("total wrong: %+v", env)
	}
}

// --- Templates handlers ---

func TestListTemplatesEmpty(t *testing.T) {
	app := newTestApp(t)
	rec := httptest.NewRecorder()
	app.listTemplates(rec, httptest.NewRequest("GET", "/api/templates", nil))
	if rec.Code != 200 {
		t.Fatalf("status %d", rec.Code)
	}
}

func TestUpsertTemplate(t *testing.T) {
	app := newTestApp(t)
	body := mustEncode(t, map[string]any{"key": "empresa", "value": "ZapCast"})
	rec := httptest.NewRecorder()
	app.upsertTemplate(rec, newRequest("POST", "/api/templates", body))
	if rec.Code != 200 {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	vars, _ := app.contacts.GetTemplateVars(ctx)
	if vars["empresa"] != "ZapCast" {
		t.Errorf("var not persisted: %+v", vars)
	}
}

func TestUpsertTemplateRejectsReservedKey(t *testing.T) {
	app := newTestApp(t)
	body := mustEncode(t, map[string]any{"key": "name", "value": "x"})
	rec := httptest.NewRecorder()
	app.upsertTemplate(rec, newRequest("POST", "/api/templates", body))
	if rec.Code != 400 {
		t.Errorf("expected 400 for reserved key, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestUpsertTemplateMissingKey(t *testing.T) {
	app := newTestApp(t)
	rec := httptest.NewRecorder()
	app.upsertTemplate(rec, newRequest("POST", "/api/templates", mustEncode(t, map[string]any{"value": "x"})))
	if rec.Code != 400 {
		t.Errorf("expected 400, got %d", rec.Code)
	}
}

func TestUpsertTemplateStripsBraces(t *testing.T) {
	app := newTestApp(t)
	body := mustEncode(t, map[string]any{"key": "{cupom}", "value": "DESC10"})
	rec := httptest.NewRecorder()
	app.upsertTemplate(rec, newRequest("POST", "/api/templates", body))
	if rec.Code != 200 {
		t.Fatalf("status %d body=%s", rec.Code, rec.Body.String())
	}
	var out map[string]string
	decodeBody(t, rec, &out)
	if out["key"] != "cupom" {
		t.Errorf("braces not stripped: %q", out["key"])
	}
}

func TestDeleteTemplate(t *testing.T) {
	app := newTestApp(t)
	_ = app.contacts.SetTemplateVar(ctx, "cupom", "DESC10")
	req := httptest.NewRequest("DELETE", "/api/templates/cupom", nil)
	req.SetPathValue("key", "cupom")
	rec := httptest.NewRecorder()
	app.deleteTemplate(rec, req)
	if rec.Code != 200 {
		t.Errorf("status %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestDeleteTemplateMissingPath(t *testing.T) {
	app := newTestApp(t)
	req := httptest.NewRequest("DELETE", "/api/templates/", nil)
	req.SetPathValue("key", "")
	rec := httptest.NewRecorder()
	app.deleteTemplate(rec, req)
	if rec.Code != 400 {
		t.Errorf("expected 400, got %d", rec.Code)
	}
}

// --- Export ---

func TestExportContactsHeadersAndBOM(t *testing.T) {
	app := newTestApp(t)
	_, _ = app.contacts.AddContact(ctx, "Ana", "5511111111111", nil)
	rec := httptest.NewRecorder()
	app.exportContacts(rec, httptest.NewRequest("GET", "/api/contacts/export", nil))
	if rec.Code != 200 {
		t.Fatalf("status %d body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); got != "text/csv; charset=utf-8" {
		t.Errorf("Content-Type = %q", got)
	}
	cd := rec.Header().Get("Content-Disposition")
	if !strings.HasPrefix(cd, `attachment; filename="zapcast-contacts-`) {
		t.Errorf("Content-Disposition = %q", cd)
	}
	body := rec.Body.Bytes()
	if len(body) < 3 || body[0] != 0xEF || body[1] != 0xBB || body[2] != 0xBF {
		t.Errorf("expected UTF-8 BOM, got % x", body[:3])
	}
	csv := string(body[3:])
	if !strings.HasPrefix(csv, "Name,Phone,Group\n") {
		t.Errorf("missing header row: %q", csv)
	}
	if !strings.Contains(csv, "Ana,5511111111111,") {
		t.Errorf("contact row missing: %q", csv)
	}
}

func TestExportContactsMultiGroupSplitsRows(t *testing.T) {
	app := newTestApp(t)
	_, _ = app.contacts.AddContact(ctx, "Ana", "5511111111111", []string{"Clientes", "VIP"})
	_, _ = app.contacts.AddContact(ctx, "Bob", "5511222222222", nil)
	rec := httptest.NewRecorder()
	app.exportContacts(rec, httptest.NewRequest("GET", "/api/contacts/export", nil))
	if rec.Code != 200 {
		t.Fatalf("status %d", rec.Code)
	}
	csv := string(rec.Body.Bytes()[3:]) // strip BOM
	lines := strings.Split(strings.TrimRight(csv, "\n"), "\n")
	// Expect: header + 2 Ana rows (one per group) + 1 Bob row (empty group) = 4.
	if len(lines) != 4 {
		t.Errorf("expected 4 lines, got %d: %q", len(lines), csv)
	}
	if !strings.Contains(csv, "Ana,5511111111111,Clientes") || !strings.Contains(csv, "Ana,5511111111111,VIP") {
		t.Errorf("missing group rows: %q", csv)
	}
	if !strings.Contains(csv, "Bob,5511222222222,") {
		t.Errorf("missing empty-group row: %q", csv)
	}
}

func TestExportContactsEmptyEmitsHeaderOnly(t *testing.T) {
	app := newTestApp(t)
	rec := httptest.NewRecorder()
	app.exportContacts(rec, httptest.NewRequest("GET", "/api/contacts/export", nil))
	if rec.Code != 200 {
		t.Fatalf("status %d", rec.Code)
	}
	csv := string(rec.Body.Bytes()[3:])
	if csv != "Name,Phone,Group\n" {
		t.Errorf("unexpected body for empty store: %q", csv)
	}
}

func TestExportContactsDBError(t *testing.T) {
	app := newTestApp(t)
	_ = app.contacts.db.Close()
	rec := httptest.NewRecorder()
	app.exportContacts(rec, httptest.NewRequest("GET", "/api/contacts/export", nil))
	if rec.Code != 500 {
		t.Errorf("expected 500 on DB error, got %d", rec.Code)
	}
}

// TestSanitizeCSVCell covers the formula-injection escape directly. The unit
// test guards against regression of the helper independently of the export
// handler.
func TestSanitizeCSVCell(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", ""},
		{"Ana", "Ana"},
		{"=cmd|'/c calc'!A0", "'=cmd|'/c calc'!A0"},
		{"+SUM(A1:A2)", "'+SUM(A1:A2)"},
		{"-1+1", "'-1+1"},
		{"@SUM", "'@SUM"},
		{"\tDROP", "'\tDROP"},
		{"\rPAYLOAD", "'\rPAYLOAD"},
		{"normal name", "normal name"},
	}
	for _, c := range cases {
		if got := sanitizeCSVCell(c.in); got != c.want {
			t.Errorf("sanitizeCSVCell(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestExportContactsCSVInjectionEscaped is an end-to-end check: a contact
// whose name starts with `=` must be exported with a leading `'` so opening
// the file in Excel doesn't execute the cell as a formula. Verifies the
// handler wires sanitizeCSVCell to the row writer.
func TestExportContactsCSVInjectionEscaped(t *testing.T) {
	app := newTestApp(t)
	if _, err := app.contacts.AddContact(ctx, "=cmd|'/c calc'!A0", "5511111111111", []string{"=evil"}); err != nil {
		t.Fatalf("seed failed: %v", err)
	}
	rec := httptest.NewRecorder()
	app.exportContacts(rec, httptest.NewRequest("GET", "/api/contacts/export", nil))
	if rec.Code != 200 {
		t.Fatalf("status %d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if strings.Contains(body, "\n=cmd") {
		t.Errorf("unescaped formula in CSV body: %q", body)
	}
	if !strings.Contains(body, "'=cmd|'/c calc'!A0") {
		t.Errorf("missing escaped name in CSV body: %q", body)
	}
	if !strings.Contains(body, "'=evil") {
		t.Errorf("missing escaped group in CSV body: %q", body)
	}
}

func TestExportImportRoundtrip(t *testing.T) {
	app := newTestApp(t)
	_, _ = app.contacts.AddContact(ctx, "Ana", "5511111111111", []string{"VIP"})
	_, _ = app.contacts.AddContact(ctx, "Bob", "5511222222222", []string{"Clientes", "VIP"})

	// Export
	rec := httptest.NewRecorder()
	app.exportContacts(rec, httptest.NewRequest("GET", "/api/contacts/export", nil))
	exported := rec.Body.String()

	// Wipe and re-import
	if err := app.contacts.DeleteContact(ctx, "5511111111111"); err != nil {
		t.Fatal(err)
	}
	if err := app.contacts.DeleteContact(ctx, "5511222222222"); err != nil {
		t.Fatal(err)
	}
	body := mustEncode(t, map[string]string{"csv": exported})
	rec = httptest.NewRecorder()
	app.handleImport(rec, newRequest("POST", "/api/import", body))
	if rec.Code != 200 {
		t.Fatalf("import after export failed: %d body=%s", rec.Code, rec.Body.String())
	}

	// Verify contacts and group memberships were restored.
	contacts, _ := app.contacts.GetContacts(ctx)
	if len(contacts) != 2 {
		t.Fatalf("expected 2 contacts after roundtrip, got %d", len(contacts))
	}
	groups, _ := app.contacts.GetGroups(ctx)
	if len(groups) != 2 {
		t.Fatalf("expected 2 groups, got %d (%+v)", len(groups), groups)
	}
}

// --- Import ---

func TestHandleImportJSONHappy(t *testing.T) {
	app := newTestApp(t)
	csv := "Nome,Telefone\nJoão,5511999998888\nMaria,5511888887777\n"
	body := mustEncode(t, map[string]string{"csv": csv})
	rec := httptest.NewRecorder()
	app.handleImport(rec, newRequest("POST", "/api/import", body))
	if rec.Code != 200 {
		t.Fatalf("status %d body=%s", rec.Code, rec.Body.String())
	}
	var out map[string]any
	decodeBody(t, rec, &out)
	if out["created"].(float64) != 2 {
		t.Errorf("created wrong: %+v", out)
	}
}

func TestHandleImportEmptyCSV(t *testing.T) {
	app := newTestApp(t)
	body := mustEncode(t, map[string]string{"csv": ""})
	rec := httptest.NewRecorder()
	app.handleImport(rec, newRequest("POST", "/api/import", body))
	if rec.Code != 400 {
		t.Errorf("expected 400 for empty CSV, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandleImportMultipart(t *testing.T) {
	app := newTestApp(t)
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	w, err := mw.CreateFormFile("file", "list.csv")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = w.Write([]byte("Nome,Telefone\nAna,5511111112222\n"))
	_ = mw.Close()
	req := httptest.NewRequest("POST", "/api/import", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	rec := httptest.NewRecorder()
	app.handleImport(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandleImportMultipartMissingFile(t *testing.T) {
	app := newTestApp(t)
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	_ = mw.Close()
	req := httptest.NewRequest("POST", "/api/import", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	rec := httptest.NewRecorder()
	app.handleImport(rec, req)
	if rec.Code != 400 {
		t.Errorf("expected 400 for missing file, got %d", rec.Code)
	}
}

// --- Broadcast handler validations (no WhatsApp connection required) ---

func TestHandleBroadcastNotConnected(t *testing.T) {
	app := newTestApp(t)
	body := mustEncode(t, BroadcastRequest{Message: "hi", GroupID: "x"})
	rec := httptest.NewRecorder()
	app.handleBroadcast(rec, newRequest("POST", "/api/broadcast", body))
	if rec.Code != 400 {
		t.Errorf("expected 400 (not connected), got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandleBroadcastListEmpty(t *testing.T) {
	app := newTestApp(t)
	rec := httptest.NewRecorder()
	app.handleBroadcastList(rec, httptest.NewRequest("GET", "/api/broadcasts", nil))
	if rec.Code != 200 {
		t.Fatalf("status %d", rec.Code)
	}
	var out map[string]any
	decodeBody(t, rec, &out)
	items, _ := out["items"].([]any)
	if len(items) != 0 {
		t.Errorf("expected empty items, got %+v", items)
	}
}

func TestHandleBroadcastByIDInvalid(t *testing.T) {
	app := newTestApp(t)
	req := httptest.NewRequest("GET", "/api/broadcasts/abc", nil)
	req.SetPathValue("id", "abc")
	rec := httptest.NewRecorder()
	app.handleBroadcastByID(rec, req)
	if rec.Code != 400 {
		t.Errorf("expected 400 for invalid id, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandleBroadcastByIDZero(t *testing.T) {
	app := newTestApp(t)
	req := httptest.NewRequest("GET", "/api/broadcasts/0", nil)
	req.SetPathValue("id", "0")
	rec := httptest.NewRecorder()
	app.handleBroadcastByID(rec, req)
	if rec.Code != 400 {
		t.Errorf("expected 400 for id=0, got %d", rec.Code)
	}
}

func TestHandleStatsEmpty(t *testing.T) {
	app := newTestApp(t)
	rec := httptest.NewRecorder()
	app.handleStats(rec, httptest.NewRequest("GET", "/api/stats", nil))
	if rec.Code != 200 {
		t.Fatalf("status %d", rec.Code)
	}
	var out map[string]int
	decodeBody(t, rec, &out)
	if out["broadcasts"] != 0 {
		t.Errorf("expected 0 broadcasts, got %+v", out)
	}
}

// --- Status / Translations / AuthRequired / Health ---

func TestHandleAuthRequired(t *testing.T) {
	app := newTestApp(t)
	rec := httptest.NewRecorder()
	app.handleAuthRequired(rec, httptest.NewRequest("GET", "/api/auth/required", nil))
	if rec.Code != 200 {
		t.Fatalf("status %d", rec.Code)
	}
	var out AuthRequiredResponse
	decodeBody(t, rec, &out)
	if out.Required {
		t.Errorf("expected required=false when authToken empty, got %+v", out)
	}

	app.authToken = "secret"
	rec = httptest.NewRecorder()
	app.handleAuthRequired(rec, httptest.NewRequest("GET", "/api/auth/required", nil))
	decodeBody(t, rec, &out)
	if !out.Required {
		t.Errorf("expected required=true with authToken set, got %+v", out)
	}
}

func TestHandleStatusDisconnected(t *testing.T) {
	app := newTestApp(t)
	rec := httptest.NewRecorder()
	app.handleStatus(rec, httptest.NewRequest("GET", "/api/status", nil))
	if rec.Code != 200 {
		t.Fatalf("status %d", rec.Code)
	}
	var out map[string]any
	decodeBody(t, rec, &out)
	if out["connected"].(bool) || out["loggedIn"].(bool) {
		t.Errorf("expected disconnected + not logged in, got %+v", out)
	}
	if out["version"].(string) != Version() {
		t.Errorf("version mismatch: %v", out["version"])
	}
}

func TestHandleHealthz(t *testing.T) {
	app := newTestApp(t)
	rec := httptest.NewRecorder()
	app.handleHealthz(rec, httptest.NewRequest("GET", "/healthz", nil))
	if rec.Code != 200 || rec.Body.String() != "ok" {
		t.Errorf("healthz wrong: %d %q", rec.Code, rec.Body.String())
	}
}

func TestHandleReadyz(t *testing.T) {
	app := newTestApp(t)
	rec := httptest.NewRecorder()
	app.handleReadyz(rec, httptest.NewRequest("GET", "/readyz", nil))
	if rec.Code != 200 {
		t.Errorf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	var out ReadyzResponse
	decodeBody(t, rec, &out)
	// WhatsApp disconnected is informational — never gates readiness
	// (operator needs the UI up to re-pair via QR).
	if !out.Ready || out.Checks["wa"] != "disconnected" {
		t.Errorf("expected ready with wa=disconnected, got %+v", out)
	}
}

func TestHandleReadyzDBDown(t *testing.T) {
	app := newTestApp(t)
	_ = app.contacts.db.Close()
	rec := httptest.NewRecorder()
	app.handleReadyz(rec, httptest.NewRequest("GET", "/readyz", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 with DB closed, got %d", rec.Code)
	}
	var out ReadyzResponse
	decodeBody(t, rec, &out)
	if out.Ready || out.Checks["db"] == "ok" {
		t.Errorf("expected unready with db failure, got %+v", out)
	}
}

func TestHandleTranslations(t *testing.T) {
	app := newTestApp(t)
	req := httptest.NewRequest("GET", "/api/translations", nil)
	req.Header.Set("Accept-Language", "pt-BR")
	rec := httptest.NewRecorder()
	app.handleTranslations(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status %d", rec.Code)
	}
	var out map[string]string
	decodeBody(t, rec, &out)
	// Use a stable key known to exist in pt-BR translations.
	if out["err_invalid_json"] == "" {
		t.Errorf("expected localized translation for err_invalid_json, got empty")
	}
}

// --- jsonResponse / jsonError plumbing ---

func TestJsonResponseSetsContentType(t *testing.T) {
	rec := httptest.NewRecorder()
	jsonResponse(rec, 201, map[string]int{"n": 7})
	if rec.Code != 201 {
		t.Errorf("status %d", rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Errorf("content-type %q", got)
	}
	body := strings.TrimSpace(rec.Body.String())
	if body != `{"n":7}` {
		t.Errorf("body %q", body)
	}
}

func TestJsonErrorPayload(t *testing.T) {
	rec := httptest.NewRecorder()
	jsonError(rec, 418, "teapot", "short and stout")
	if rec.Code != 418 {
		t.Errorf("status %d", rec.Code)
	}
	var out apiErrorEnvelope
	decodeBody(t, rec, &out)
	if out.Error.Code != "teapot" || out.Error.Message != "short and stout" {
		t.Errorf("payload %+v", out)
	}
}

// --- Broadcast lock + cooldown paths ---

func TestHandleBroadcastBusyLockReturns429(t *testing.T) {
	// Wire a connected fakeWA so the IsConnected gate clears and the
	// handler actually reaches the broadcaster lock check — the old
	// version of this test ran against the default disconnected client
	// and self-skipped on every run, never exercising the 429 branch.
	jid := types.NewJID("5511999998888", types.DefaultUserServer)
	app := newTestAppWithWA(t, &fakeWA{connected: true, jid: &jid})

	// Need an existing group so the lookup before the lock check resolves.
	if _, err := app.contacts.AddGroup(ctx, "x", nil); err != nil {
		t.Fatalf("seed group: %v", err)
	}

	// Hold the broadcast lock — simulates an in-flight broadcast.
	app.broadcaster.mu.Lock()
	defer app.broadcaster.mu.Unlock()

	body := mustEncode(t, BroadcastRequest{Message: "hi", GroupID: "x"})
	rec := httptest.NewRecorder()
	app.handleBroadcast(rec, newRequest("POST", "/api/broadcast", body))
	if rec.Code != 429 {
		t.Errorf("expected 429, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandleBroadcastMissingMessage(t *testing.T) {
	app := newTestApp(t)
	body := mustEncode(t, BroadcastRequest{GroupID: "x"})
	rec := httptest.NewRecorder()
	app.handleBroadcast(rec, newRequest("POST", "/api/broadcast", body))
	if rec.Code != 400 {
		t.Errorf("expected 400, got %d", rec.Code)
	}
}

func TestHandleBroadcastCancelIdle(t *testing.T) {
	app := newTestApp(t)
	rec := httptest.NewRecorder()
	app.handleBroadcastCancel(rec, httptest.NewRequest("POST", "/api/broadcast/cancel", nil))
	if rec.Code != 200 {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var out map[string]bool
	decodeBody(t, rec, &out)
	if out["cancelled"] {
		t.Errorf("expected cancelled=false with nothing running, got %+v", out)
	}
}

func TestHandleBroadcastCancelRunning(t *testing.T) {
	app := newTestApp(t)
	// Simulate an in-flight broadcast: Start stores the cancel func.
	handle, err := app.broadcaster.Start(context.Background(), 0)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer handle.Abandon()

	rec := httptest.NewRecorder()
	app.handleBroadcastCancel(rec, httptest.NewRequest("POST", "/api/broadcast/cancel", nil))
	if rec.Code != 200 {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var out map[string]bool
	decodeBody(t, rec, &out)
	if !out["cancelled"] {
		t.Errorf("expected cancelled=true while running, got %+v", out)
	}
	if handle.Ctx.Err() == nil {
		t.Errorf("expected broadcast ctx cancelled after operator cancel")
	}
}

// --- parsePagination ---

func TestParsePaginationDefaults(t *testing.T) {
	r := httptest.NewRequest("GET", "/?page=&limit=", nil)
	_, _, paginated := parsePagination(r)
	if paginated {
		t.Errorf("expected paginated=false when no params")
	}
}

func TestParsePaginationClamps(t *testing.T) {
	r := httptest.NewRequest("GET", "/?page=0&limit=99999", nil)
	p, l, ok := parsePagination(r)
	if !ok || p != 1 || l != 500 {
		t.Errorf("clamp wrong: page=%d limit=%d ok=%v", p, l, ok)
	}
	r = httptest.NewRequest("GET", "/?page=2&limit=0", nil)
	p, l, _ = parsePagination(r)
	if p != 2 || l != 50 {
		t.Errorf("default limit wrong: %d", l)
	}
}

func TestPaginateWindow(t *testing.T) {
	items := []int{1, 2, 3, 4, 5}
	env := paginate(items, 2, 2)
	got := env["items"].([]int)
	if len(got) != 2 || got[0] != 3 || got[1] != 4 {
		t.Errorf("window wrong: %+v", got)
	}
	// Out-of-range page returns empty slice without panic.
	env = paginate(items, 99, 2)
	got = env["items"].([]int)
	if len(got) != 0 {
		t.Errorf("expected empty oob, got %+v", got)
	}
}

// --- maxBodyBytes guard ---

func TestDecodeJSONRejectsUnknownFields(t *testing.T) {
	rec := httptest.NewRecorder()
	body := []byte(`{"name":"x","phone":"5511999998888","oops":"unknown"}`)
	r := httptest.NewRequest("POST", "/", bytes.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	var v struct {
		Name  string `json:"name"`
		Phone string `json:"phone"`
	}
	if ok := decodeJSON(rec, r, &v); ok {
		t.Errorf("expected decode to fail on unknown field")
	}
	if rec.Code != 400 {
		t.Errorf("expected 400, got %d", rec.Code)
	}
}
