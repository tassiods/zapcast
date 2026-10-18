// SPDX-FileCopyrightText: 2025 Tássio Dias <https://github.com/tassiods/zapcast>
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestGetLangDefault(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	if got := getLang(r); got != langEnUS {
		t.Errorf("default = %q, want en-US", got)
	}
}

func TestGetLangBasicMatching(t *testing.T) {
	cases := []struct {
		header string
		want   langKey
	}{
		{"pt-BR,pt;q=0.9", langPtBR},
		{"pt", langPtBR},
		{"es-ES,es;q=0.9", langEsES},
		{"es", langEsES},
		{"en-US", langEnUS},
		{"fr-FR", langEnUS}, // unsupported → default
		{"de-DE,fr;q=0.5", langEnUS},
		{"!!garbage!!", langEnUS}, // parse error path
	}
	for _, c := range cases {
		r := httptest.NewRequest("GET", "/", nil)
		r.Header.Set("Accept-Language", c.header)
		if got := getLang(r); got != c.want {
			t.Errorf("Accept-Language=%q → %q, want %q", c.header, got, c.want)
		}
	}
}

func TestTForLangKnownKey(t *testing.T) {
	got := TForLang(langPtBR, "err_invalid_json")
	if got == "" {
		t.Fatal("expected non-empty pt-BR translation")
	}
	if got == "err_invalid_json" {
		t.Errorf("translation missing — got the raw key back")
	}
}

func TestTForLangFallsBackToEnUS(t *testing.T) {
	// Unknown locale falls back to en-US catalog.
	got := TForLang(langKey("xx-XX"), "err_invalid_json")
	want := translations[langEnUS]["err_invalid_json"]
	if got != want {
		t.Errorf("unknown locale fallback wrong: %q vs %q", got, want)
	}
}

func TestTForLangUnknownKeyReturnsKey(t *testing.T) {
	got := TForLang(langEnUS, "no_such_key_anywhere")
	if got != "no_such_key_anywhere" {
		t.Errorf("expected raw key, got %q", got)
	}
}

func TestTForLangFormatsArgs(t *testing.T) {
	got := TForLang(langEnUS, "err_phone_too_short", 3)
	if !strings.Contains(got, "3") {
		t.Errorf("args not interpolated: %q", got)
	}
}

func TestTWrapper(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Set("Accept-Language", "pt-BR")
	got := T(r, "err_invalid_json")
	want := TForLang(langPtBR, "err_invalid_json")
	if got != want {
		t.Errorf("T() didn't route via getLang: %q vs %q", got, want)
	}
}

func TestJsonErrorLocalizedWritesStatus(t *testing.T) {
	rec := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Set("Accept-Language", "en-US")
	jsonErrorLocalized(rec, r, http.StatusBadRequest, "err_invalid_json")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status %d", rec.Code)
	}
	var out apiErrorEnvelope
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Error.Code != "err_invalid_json" {
		t.Errorf("expected machine code err_invalid_json, got %q", out.Error.Code)
	}
	if out.Error.Message == "" || out.Error.Message == "err_invalid_json" {
		t.Errorf("error not localized: %q", out.Error.Message)
	}
}

func TestJsonErrorLocalizedFormatsArgs(t *testing.T) {
	rec := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/", nil)
	jsonErrorLocalized(rec, r, http.StatusBadRequest, "err_phone_too_short", 4)
	var out apiErrorEnvelope
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if !strings.Contains(out.Error.Message, "4") {
		t.Errorf("expected '4' interpolated, got %q", out.Error.Message)
	}
}
