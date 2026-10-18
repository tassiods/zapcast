// SPDX-FileCopyrightText: 2025 Tássio Dias <https://github.com/tassiods/zapcast>
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"bytes"
	"context"
	"database/sql"
	"os"
	"strings"
	"testing"
	"unicode/utf16"
)

// ctx is shared across the test package as a stand-in for the context that
// production code threads through every store call. Tests don't exercise
// cancellation paths, so Background is fine.
var ctx = context.Background()

// newTestStore creates a temporary in-memory ContactStore for testing.
func newTestStore(t *testing.T) *ContactStore {
	t.Helper()
	tmpFile, err := os.CreateTemp("", "zapcast_test_*.db")
	if err != nil {
		t.Fatalf("erro ao criar arquivo temporário: %v", err)
	}
	_ = tmpFile.Close()
	t.Cleanup(func() { _ = os.Remove(tmpFile.Name()) })

	store, err := NewContactStore(context.Background(), testLogger, tmpFile.Name())
	if err != nil {
		t.Fatalf("erro ao criar ContactStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

// --- cleanPhone ---

func TestCleanPhone(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"+5511999998888", "5511999998888"},
		{"55 11 99999-8888", "5511999998888"},
		{"(55) 11-99999.8888", "5511999998888"},
		{"+1 (555) 123-4567", "15551234567"},
		{"", ""},
		{"abc", ""},
		{"123", "123"},
	}
	for _, tt := range tests {
		got := cleanPhone(tt.input)
		if got != tt.want {
			t.Errorf("cleanPhone(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

// --- validatePhone ---

func TestValidatePhone(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    string
		wantErr bool
	}{
		{"E.164 válido Brasil", "+5511999998888", "5511999998888", false},
		{"com formatação", "(55) 11 99999-8888", "5511999998888", false},
		{"E.164 US", "+1 (555) 123-4567", "15551234567", false},
		{"mínimo 7 dígitos", "1234567", "1234567", false},
		{"vazio", "", "", true},
		{"só letras", "abc", "", true},
		{"muito curto", "12345", "", true},
		{"muito longo (16)", "1234567890123456", "", true},
		{"começa com 0", "05511999998888", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := validatePhone(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("validatePhone(%q): err = %v, wantErr = %v", tt.input, err, tt.wantErr)
				return
			}
			if !tt.wantErr && got != tt.want {
				t.Errorf("validatePhone(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

// --- Contacts CRUD ---

func TestAddAndGetContacts(t *testing.T) {
	store := newTestStore(t)

	c, err := store.AddContact(ctx, "João", "5511999998888", nil)
	if err != nil {
		t.Fatalf("AddContact falhou: %v", err)
	}
	if c.Name != "João" || c.Phone != "5511999998888" {
		t.Errorf("contato retornado inesperado: %+v", c)
	}

	contacts, err := store.GetContacts(ctx)
	if err != nil {
		t.Fatalf("GetContacts falhou: %v", err)
	}
	if len(contacts) != 1 {
		t.Fatalf("esperava 1 contato, obteve %d", len(contacts))
	}
	if contacts[0].Name != "João" {
		t.Errorf("nome esperado 'João', obteve '%s'", contacts[0].Name)
	}
}

func TestAddContactDuplicate(t *testing.T) {
	store := newTestStore(t)

	_, err := store.AddContact(ctx, "João", "5511999998888", nil)
	if err != nil {
		t.Fatalf("primeiro AddContact falhou: %v", err)
	}
	_, err = store.AddContact(ctx, "Maria", "5511999998888", nil)
	if err == nil {
		t.Error("esperava erro ao adicionar contato duplicado")
	}
}

func TestAddContactEmptyPhone(t *testing.T) {
	store := newTestStore(t)

	_, err := store.AddContact(ctx, "João", "", nil)
	if err == nil {
		t.Error("esperava erro ao adicionar contato sem telefone")
	}
}

func TestAddContactInvalidPhone(t *testing.T) {
	store := newTestStore(t)

	// Muito curto
	if _, err := store.AddContact(ctx, "João", "123", nil); err == nil {
		t.Error("esperava erro para telefone muito curto")
	}
	// Começa com 0
	if _, err := store.AddContact(ctx, "João", "05511999998888", nil); err == nil {
		t.Error("esperava erro para telefone iniciando com 0")
	}
}

func TestUpdateContact(t *testing.T) {
	store := newTestStore(t)

	_, err := store.AddContact(ctx, "João", "5511999998888", nil)
	if err != nil {
		t.Fatalf("AddContact falhou: %v", err)
	}

	updated, err := store.UpdateContact(ctx, "5511999998888", "João Silva", "", nil)
	if err != nil {
		t.Fatalf("UpdateContact falhou: %v", err)
	}
	if updated.Name != "João Silva" {
		t.Errorf("nome esperado 'João Silva', obteve '%s'", updated.Name)
	}
}

func TestUpdateContactNotFound(t *testing.T) {
	store := newTestStore(t)

	_, err := store.UpdateContact(ctx, "0000", "Nome", "", nil)
	if err == nil {
		t.Error("esperava erro ao atualizar contato inexistente")
	}
}

func TestDeleteContact(t *testing.T) {
	store := newTestStore(t)

	_, err := store.AddContact(ctx, "João", "5511999998888", nil)
	if err != nil {
		t.Fatalf("AddContact falhou: %v", err)
	}

	err = store.DeleteContact(ctx, "5511999998888")
	if err != nil {
		t.Fatalf("DeleteContact falhou: %v", err)
	}

	contacts, err := store.GetContacts(ctx)
	if err != nil {
		t.Fatalf("GetContacts falhou: %v", err)
	}
	if len(contacts) != 0 {
		t.Errorf("esperava 0 contatos, obteve %d", len(contacts))
	}
}

// --- Groups CRUD ---

func TestAddAndGetGroups(t *testing.T) {
	store := newTestStore(t)

	// Add a contact first (needed for group membership)
	_, err := store.AddContact(ctx, "João", "5511999998888", nil)
	if err != nil {
		t.Fatalf("AddContact falhou: %v", err)
	}

	g, err := store.AddGroup(ctx, "Clientes", []string{"5511999998888"})
	if err != nil {
		t.Fatalf("AddGroup falhou: %v", err)
	}
	if g.Name != "Clientes" || g.Count != 1 {
		t.Errorf("grupo inesperado: %+v", g)
	}

	groups, err := store.GetGroups(ctx)
	if err != nil {
		t.Fatalf("GetGroups falhou: %v", err)
	}
	if len(groups) != 1 {
		t.Fatalf("esperava 1 grupo, obteve %d", len(groups))
	}
	if len(groups[0].Contacts) != 1 {
		t.Errorf("esperava 1 membro no grupo, obteve %d", len(groups[0].Contacts))
	}
}

func TestAddGroupDuplicate(t *testing.T) {
	store := newTestStore(t)

	_, err := store.AddGroup(ctx, "Clientes", nil)
	if err != nil {
		t.Fatalf("primeiro AddGroup falhou: %v", err)
	}
	_, err = store.AddGroup(ctx, "Clientes", nil)
	if err == nil {
		t.Error("esperava erro ao adicionar grupo duplicado")
	}
}

func TestDeleteContactNotFound(t *testing.T) {
	store := newTestStore(t)
	err := store.DeleteContact(ctx, "5511000000000")
	if err == nil {
		t.Fatal("expected an error for missing contact, got nil")
	}
	ek, ok := err.(*errKey)
	if !ok {
		t.Fatalf("expected *errKey, got %T (%v)", err, err)
	}
	if ek.key != "err_contact_not_found" {
		t.Errorf("expected key err_contact_not_found, got %q", ek.key)
	}

	// Deleting an existing contact succeeds and a follow-up returns not found.
	if _, err := store.AddContact(ctx, "João", "5511999998888", nil); err != nil {
		t.Fatalf("AddContact falhou: %v", err)
	}
	if err := store.DeleteContact(ctx, "5511999998888"); err != nil {
		t.Fatalf("DeleteContact existing: %v", err)
	}
	if err := store.DeleteContact(ctx, "5511999998888"); err == nil {
		t.Error("second delete should return not_found")
	}
}

func TestMigrationIdempotent(t *testing.T) {
	store := newTestStore(t)
	// Running migrations again on an already-migrated DB must not error
	// (every applied version is recorded in schema_migrations and skipped).
	if err := runMigrations(context.Background(), testLogger, store.db); err != nil {
		t.Fatalf("second migrate failed: %v", err)
	}
	// Confirm the media columns landed via PRAGMA table_info.
	rows, err := store.db.Query("PRAGMA table_info(broadcasts)")
	if err != nil {
		t.Fatalf("PRAGMA failed: %v", err)
	}
	defer rows.Close()
	cols := map[string]bool{}
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull, pk int
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			t.Fatalf("scan: %v", err)
		}
		cols[name] = true
	}
	for _, want := range []string{"media_type", "media_name"} {
		if !cols[want] {
			t.Errorf("missing column %s on broadcasts table", want)
		}
	}

	// CreateBroadcast roundtrips media fields through ListBroadcasts.
	id, err := store.CreateBroadcast(ctx, "Group", "hi", 1, 0, 0, "image", "foo.jpg")
	if err != nil {
		t.Fatalf("CreateBroadcast: %v", err)
	}
	items, err := store.ListBroadcasts(ctx, 10, 0)
	if err != nil {
		t.Fatalf("ListBroadcasts: %v", err)
	}
	var found *BroadcastSummary
	for i := range items {
		if items[i].ID == id {
			found = &items[i]
			break
		}
	}
	if found == nil {
		t.Fatal("did not find inserted broadcast in list")
	}
	if found.MediaType != "image" || found.MediaName != "foo.jpg" {
		t.Errorf("media fields didn't roundtrip: got %q/%q", found.MediaType, found.MediaName)
	}
}

func TestUpdateGroup(t *testing.T) {
	store := newTestStore(t)

	_, err := store.AddContact(ctx, "João", "5511999998888", nil)
	if err != nil {
		t.Fatalf("AddContact falhou: %v", err)
	}
	_, err = store.AddGroup(ctx, "Clientes", nil)
	if err != nil {
		t.Fatalf("AddGroup falhou: %v", err)
	}

	updated, err := store.UpdateGroup(ctx, "Clientes", "VIP", []string{"5511999998888"})
	if err != nil {
		t.Fatalf("UpdateGroup falhou: %v", err)
	}
	if updated.Name != "VIP" || len(updated.Contacts) != 1 {
		t.Errorf("grupo atualizado inesperado: %+v", updated)
	}
}

// Regression: UpdateGroup must keep the FK-violation INSERTs as silent skips
// (INSERT OR IGNORE) rather than aborting the transaction. After the strict
// error checks added on contacts.go DELETE/INSERT in UpdateGroup, the happy
// path with one valid and one missing phone should still commit, with the
// missing phone dropped from the membership list.
func TestUpdateGroupSkipsMissingPhone(t *testing.T) {
	store := newTestStore(t)

	if _, err := store.AddContact(ctx, "João", "5511999998888", nil); err != nil {
		t.Fatalf("AddContact falhou: %v", err)
	}
	if _, err := store.AddGroup(ctx, "Clientes", nil); err != nil {
		t.Fatalf("AddGroup falhou: %v", err)
	}

	// Mix one real phone with one that does not exist in `contacts`.
	updated, err := store.UpdateGroup(ctx, "Clientes", "Clientes", []string{
		"5511999998888",
		"5511000000000", // FK miss → silently dropped
	})
	if err != nil {
		t.Fatalf("UpdateGroup falhou: %v", err)
	}

	// Both the return value and the persisted membership reflect what was
	// actually inserted (the missing phone is dropped on FK violation).
	if len(updated.Contacts) != 1 || updated.Contacts[0] != "5511999998888" {
		t.Errorf("retorno deveria conter apenas a phone válida, obteve %v", updated.Contacts)
	}

	groups, err := store.GetGroups(ctx)
	if err != nil {
		t.Fatalf("GetGroups falhou: %v", err)
	}
	var stored Group
	for _, g := range groups {
		if g.Name == "Clientes" {
			stored = g
			break
		}
	}
	if len(stored.Contacts) != 1 || stored.Contacts[0] != "5511999998888" {
		t.Errorf("membros persistidos inesperados: %+v", stored.Contacts)
	}
}

func TestDeleteGroup(t *testing.T) {
	store := newTestStore(t)

	_, err := store.AddGroup(ctx, "Clientes", nil)
	if err != nil {
		t.Fatalf("AddGroup falhou: %v", err)
	}
	err = store.DeleteGroup(ctx, "Clientes")
	if err != nil {
		t.Fatalf("DeleteGroup falhou: %v", err)
	}
	groups, err := store.GetGroups(ctx)
	if err != nil {
		t.Fatalf("GetGroups falhou: %v", err)
	}
	if len(groups) != 0 {
		t.Errorf("esperava 0 grupos, obteve %d", len(groups))
	}
}

// --- Contact with Groups ---

func TestAddContactWithGroups(t *testing.T) {
	store := newTestStore(t)

	c, err := store.AddContact(ctx, "João", "5511999998888", []string{"Clientes", "VIP"})
	if err != nil {
		t.Fatalf("AddContact com grupos falhou: %v", err)
	}
	if len(c.Groups) != 2 {
		t.Errorf("esperava 2 grupos, obteve %d", len(c.Groups))
	}

	// Verify groups were auto-created
	groups, err := store.GetGroups(ctx)
	if err != nil {
		t.Fatalf("GetGroups falhou: %v", err)
	}
	if len(groups) != 2 {
		t.Errorf("esperava 2 grupos auto-criados, obteve %d", len(groups))
	}
}

func TestGetGroupContacts(t *testing.T) {
	store := newTestStore(t)

	_, _ = store.AddContact(ctx, "João", "5511999998888", []string{"Clientes"})
	_, _ = store.AddContact(ctx, "Maria", "5511888887777", []string{"Clientes"})
	_, _ = store.AddContact(ctx, "Pedro", "5511777776666", []string{"VIP"})

	contacts, err := store.GetGroupContacts(ctx, "Clientes")
	if err != nil {
		t.Fatalf("GetGroupContacts falhou: %v", err)
	}
	if len(contacts) != 2 {
		t.Errorf("esperava 2 contatos no grupo Clientes, obteve %d", len(contacts))
	}
}

// --- Template Variables ---

func TestTemplateVars(t *testing.T) {
	store := newTestStore(t)

	err := store.SetTemplateVar(ctx, "empresa", "ZapCast")
	if err != nil {
		t.Fatalf("SetTemplateVar falhou: %v", err)
	}

	vars, err := store.GetTemplateVars(ctx)
	if err != nil {
		t.Fatalf("GetTemplateVars falhou: %v", err)
	}
	if vars["empresa"] != "ZapCast" {
		t.Errorf("esperava 'ZapCast', obteve '%s'", vars["empresa"])
	}

	// Upsert
	err = store.SetTemplateVar(ctx, "empresa", "ZapCast 2.0")
	if err != nil {
		t.Fatalf("SetTemplateVar (upsert) falhou: %v", err)
	}
	vars, err = store.GetTemplateVars(ctx)
	if err != nil {
		t.Fatalf("GetTemplateVars falhou: %v", err)
	}
	if vars["empresa"] != "ZapCast 2.0" {
		t.Errorf("esperava 'ZapCast 2.0', obteve '%s'", vars["empresa"])
	}

	// Delete
	err = store.DeleteTemplateVar(ctx, "empresa")
	if err != nil {
		t.Fatalf("DeleteTemplateVar falhou: %v", err)
	}
	vars, err = store.GetTemplateVars(ctx)
	if err != nil {
		t.Fatalf("GetTemplateVars falhou: %v", err)
	}
	if _, ok := vars["empresa"]; ok {
		t.Error("variável 'empresa' deveria ter sido removida")
	}
}

// --- renderTemplate ---

func TestRenderTemplate(t *testing.T) {
	contact := Contact{Name: "João", Phone: "5511999998888"}
	customVars := map[string]string{
		"empresa": "ZapCast",
		"cupom":   "DESC10",
	}

	tests := []struct {
		tpl  string
		want string
	}{
		{"Olá {nome}!", "Olá João!"},
		{"Hi {name}, call {phone}", "Hi João, call 5511999998888"},
		{"Tel: {telefone}", "Tel: 5511999998888"},
		{"{empresa} - cupom {cupom}", "ZapCast - cupom DESC10"},
		{"Olá {nome}, da {empresa}!", "Olá João, da ZapCast!"},
		{"Sem variáveis", "Sem variáveis"},
	}
	for _, tt := range tests {
		got := renderTemplate(tt.tpl, contact, customVars)
		if got != tt.want {
			t.Errorf("renderTemplate(%q) = %q, want %q", tt.tpl, got, tt.want)
		}
	}
}

// --- Import ---

func TestImportContacts(t *testing.T) {
	store := newTestStore(t)

	rows := []ImportRow{
		{Name: "João", Phone: "5511999998888", Groups: []string{"Clientes"}},
		{Name: "Maria", Phone: "5511888887777", Groups: []string{"Clientes"}},
		{Name: "", Phone: "5511777776666"}, // should be skipped (no name)
		{Name: "Pedro", Phone: ""},         // should be skipped (no phone)
	}

	created, updated, skipped, groups, err := store.ImportContacts(ctx, rows)
	if err != nil {
		t.Fatalf("ImportContacts falhou: %v", err)
	}
	if created != 2 {
		t.Errorf("esperava 2 criados, obteve %d", created)
	}
	if updated != 0 {
		t.Errorf("esperava 0 atualizados, obteve %d", updated)
	}
	if skipped != 2 {
		t.Errorf("esperava 2 pulados, obteve %d", skipped)
	}
	if groups != 1 {
		t.Errorf("esperava 1 grupo criado, obteve %d", groups)
	}

	contacts, _ := store.GetContacts(ctx)
	if len(contacts) != 2 {
		t.Errorf("esperava 2 contatos no banco, obteve %d", len(contacts))
	}
}

func TestImportContactsUpsert(t *testing.T) {
	store := newTestStore(t)

	// First import
	rows1 := []ImportRow{{Name: "João", Phone: "5511999998888"}}
	if _, _, _, _, err := store.ImportContacts(ctx, rows1); err != nil {
		t.Fatalf("first import: %v", err)
	}

	// Second import with same phone, different name -> should update, not create
	rows2 := []ImportRow{{Name: "João Silva", Phone: "5511999998888"}}
	created, updated, _, _, err := store.ImportContacts(ctx, rows2)
	if err != nil {
		t.Fatalf("ImportContacts (upsert) falhou: %v", err)
	}
	if created != 0 {
		t.Errorf("esperava 0 criados (upsert), obteve %d", created)
	}
	if updated != 1 {
		t.Errorf("esperava 1 atualizado (upsert), obteve %d", updated)
	}

	contacts, _ := store.GetContacts(ctx)
	if len(contacts) != 1 {
		t.Fatalf("esperava 1 contato, obteve %d", len(contacts))
	}
	if contacts[0].Name != "João Silva" {
		t.Errorf("esperava nome atualizado 'João Silva', obteve '%s'", contacts[0].Name)
	}
}

// --- CSV format detection + parsers ---

func TestParseCSV_Legacy(t *testing.T) {
	input := `Nome,Telefone,Grupo
João,5511999998888,Clientes
Maria,5511888887777,Clientes
`
	rows, err := parseCSVImport(strings.NewReader(input))
	if err != nil {
		t.Fatalf("parse falhou: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("esperava 2 linhas, obteve %d", len(rows))
	}
	if rows[0].Name != "João" || rows[0].Phone != "5511999998888" {
		t.Errorf("linha 0 inesperada: %+v", rows[0])
	}
	if len(rows[0].Groups) != 1 || rows[0].Groups[0] != "Clientes" {
		t.Errorf("grupos legacy inesperados: %+v", rows[0].Groups)
	}
}

func TestParseCSV_LegacyHeaderless(t *testing.T) {
	input := "João,5511999998888\nMaria,5511888887777\n"
	rows, err := parseCSVImport(strings.NewReader(input))
	if err != nil {
		t.Fatalf("parse falhou: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("esperava 2 linhas, obteve %d", len(rows))
	}
}

func TestParseCSV_Google(t *testing.T) {
	// Uses real Google header names. Second phone is Mobile — should win over Home.
	input := `Name,Given Name,Family Name,Phone 1 - Type,Phone 1 - Value,Phone 2 - Type,Phone 2 - Value,Labels
João Silva,João,Silva,Home,1188887777,Mobile,+55 11 99999-8888,* myContacts ::: Clientes ::: VIP
Maria,Maria,,Cell,5511777776666,,,Amigos
`
	rows, err := parseCSVImport(strings.NewReader(input))
	if err != nil {
		t.Fatalf("parse falhou: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("esperava 2 linhas, obteve %d", len(rows))
	}

	// João: Mobile preferido
	if rows[0].Name != "João Silva" {
		t.Errorf("nome esperado 'João Silva', obteve %q", rows[0].Name)
	}
	if rows[0].Phone != "+55 11 99999-8888" {
		t.Errorf("esperava telefone Mobile bruto, obteve %q", rows[0].Phone)
	}
	// System label '* myContacts' deve ser filtrado
	if len(rows[0].Groups) != 2 || rows[0].Groups[0] != "Clientes" || rows[0].Groups[1] != "VIP" {
		t.Errorf("grupos Google inesperados: %+v", rows[0].Groups)
	}

	// Maria: sem Name, monta de Given + Family
	if rows[1].Name != "Maria" {
		t.Errorf("esperava nome 'Maria', obteve %q", rows[1].Name)
	}
	if rows[1].Phone != "5511777776666" {
		t.Errorf("esperava telefone Cell, obteve %q", rows[1].Phone)
	}
}

func TestParseCSV_OutlookUTF16(t *testing.T) {
	// Outlook legado: UTF-16 LE com BOM, separador ';', Categories separado por ';'
	// dentro de campo com aspas.
	content := `First Name;Last Name;Mobile Phone;Business Phone;Categories
João;Silva;+5511999998888;1133334444;"Amigos; Trabalho"
Maria;;5511888887777;;Clientes
`
	encoded := encodeUTF16LE(content)

	rows, err := parseCSVImport(bytes.NewReader(encoded))
	if err != nil {
		t.Fatalf("parse falhou: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("esperava 2 linhas, obteve %d", len(rows))
	}

	if rows[0].Name != "João Silva" {
		t.Errorf("nome esperado 'João Silva', obteve %q", rows[0].Name)
	}
	if rows[0].Phone != "+5511999998888" {
		t.Errorf("esperava Mobile Phone, obteve %q", rows[0].Phone)
	}
	if len(rows[0].Groups) != 2 || rows[0].Groups[0] != "Amigos" || rows[0].Groups[1] != "Trabalho" {
		t.Errorf("grupos Outlook inesperados: %+v", rows[0].Groups)
	}

	// Maria só tem First Name
	if rows[1].Name != "Maria" {
		t.Errorf("esperava 'Maria', obteve %q", rows[1].Name)
	}
}

func TestGetContactsByPhones(t *testing.T) {
	store := newTestStore(t)

	_, _ = store.AddContact(ctx, "João", "5511999998888", nil)
	_, _ = store.AddContact(ctx, "Maria", "5511888887777", nil)
	_, _ = store.AddContact(ctx, "Pedro", "5511777776666", nil)

	// Pede dois conhecidos + um phone que não existe + uma duplicata.
	phones := []string{"5511888887777", "5511999998888", "0000000000", "5511999998888"}
	got, err := store.GetContactsByPhones(ctx, phones)
	if err != nil {
		t.Fatalf("GetContactsByPhones: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("esperava 2 contatos (dedup + miss filtrado), obteve %d", len(got))
	}
	// Ordenado por nome case-insensitive: João, Maria.
	if got[0].Name != "João" || got[1].Name != "Maria" {
		t.Errorf("ordem inesperada: %+v", got)
	}
	// Phones sem match não quebram nada.
	empty, err := store.GetContactsByPhones(ctx, []string{"0000"})
	if err != nil || len(empty) != 0 {
		t.Errorf("esperava vazio, obteve %+v (err=%v)", empty, err)
	}
	// Slice vazio retorna slice vazio sem ir ao banco.
	none, err := store.GetContactsByPhones(ctx, nil)
	if err != nil || none == nil || len(none) != 0 {
		t.Errorf("nil input deve retornar []Contact{} sem erro, got %+v err=%v", none, err)
	}
}

func TestImportContacts_MultiGroup(t *testing.T) {
	store := newTestStore(t)

	rows := []ImportRow{
		{Name: "João", Phone: "5511999998888", Groups: []string{"Clientes", "VIP", "Natal 2024"}},
	}
	created, _, _, groupsCreated, err := store.ImportContacts(ctx, rows)
	if err != nil {
		t.Fatalf("ImportContacts: %v", err)
	}
	if created != 1 {
		t.Fatalf("esperava 1 criado, obteve %d", created)
	}
	if groupsCreated != 3 {
		t.Fatalf("esperava 3 grupos novos, obteve %d", groupsCreated)
	}

	groups, _ := store.GetGroups(ctx)
	if len(groups) != 3 {
		t.Fatalf("esperava 3 grupos, obteve %d", len(groups))
	}
	for _, g := range groups {
		if len(g.Contacts) != 1 || g.Contacts[0] != "5511999998888" {
			t.Errorf("grupo %q sem contato esperado: %+v", g.Name, g.Contacts)
		}
	}
}

// Re-importing rows that reference an existing group must not bump the
// groupsCreated counter — only first sight of a group_ row counts.
func TestImportContacts_GroupsCreatedDedup(t *testing.T) {
	store := newTestStore(t)

	first := []ImportRow{{Name: "João", Phone: "5511999998888", Groups: []string{"VIP"}}}
	if _, _, _, gc, err := store.ImportContacts(ctx, first); err != nil || gc != 1 {
		t.Fatalf("primeira importação: gc=%d err=%v (esperado gc=1)", gc, err)
	}

	second := []ImportRow{
		{Name: "Maria", Phone: "5511888887777", Groups: []string{"VIP"}},     // grupo já existia
		{Name: "Pedro", Phone: "5511777776666", Groups: []string{"Atacado"}}, // grupo novo
	}
	_, _, _, gc, err := store.ImportContacts(ctx, second)
	if err != nil {
		t.Fatalf("segunda importação: %v", err)
	}
	if gc != 1 {
		t.Errorf("esperava 1 grupo novo (apenas Atacado), obteve %d", gc)
	}
}

// encodeUTF16LE produces BOM + UTF-16 LE bytes, mimicking an Outlook legacy export.
func encodeUTF16LE(s string) []byte {
	u16 := utf16.Encode([]rune(s))
	out := []byte{0xFF, 0xFE}
	for _, c := range u16 {
		out = append(out, byte(c), byte(c>>8))
	}
	return out
}

// --- SearchContacts (SQL-side filter + pagination) ---

func TestSearchContactsPagesAndFilters(t *testing.T) {
	store := newTestStore(t)
	seed := []struct{ name, phone string }{
		{"Ana Silva", "5511111111111"},
		{"Ana Souza", "5511222222222"},
		{"Bruno", "5511333333333"},
	}
	for _, s := range seed {
		if _, err := store.AddContact(ctx, s.name, s.phone, nil); err != nil {
			t.Fatalf("AddContact: %v", err)
		}
	}

	// Filter narrows in SQL; total reflects all matches, not the page.
	items, total, err := store.SearchContacts(ctx, "ana", 1, 0)
	if err != nil {
		t.Fatalf("SearchContacts: %v", err)
	}
	if total != 2 || len(items) != 1 {
		t.Errorf("expected total=2 page-of-1, got total=%d len=%d", total, len(items))
	}

	// Second page picks up the remaining match.
	items, _, err = store.SearchContacts(ctx, "ana", 1, 1)
	if err != nil || len(items) != 1 {
		t.Fatalf("page 2: %v (%d items)", err, len(items))
	}

	// Phone fragment matches too.
	_, total, err = store.SearchContacts(ctx, "333", 50, 0)
	if err != nil || total != 1 {
		t.Errorf("phone search: %v total=%d", err, total)
	}

	// Empty q returns everyone.
	_, total, err = store.SearchContacts(ctx, "", 50, 0)
	if err != nil || total != 3 {
		t.Errorf("empty q: %v total=%d", err, total)
	}

	// LIKE wildcards in user input are literals, not match-all.
	_, total, err = store.SearchContacts(ctx, "%", 50, 0)
	if err != nil || total != 0 {
		t.Errorf("wildcard must be literal: %v total=%d", err, total)
	}
}
