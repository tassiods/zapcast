// SPDX-FileCopyrightText: 2025 ZapCast contributors
// SPDX-License-Identifier: Apache-2.0

package server

// Template variables CRUD + the renderer that broadcast.go calls per
// recipient. Built-in variables ({name}, {nome}, {phone}, {telefone}) are
// always handled by renderTemplate; the user can't redefine them.

import (
	"net/http"
	"strings"
)

// reservedTemplateKeys are built-in variables that renderTemplate handles
// directly; users cannot redefine them as custom template_vars rows.
var reservedTemplateKeys = map[string]bool{
	"nome": true, "name": true, "telefone": true, "phone": true,
}

// renderTemplate replaces {name}/{nome}, {phone}/{telefone}, and custom
// {variables}. Short-circuits when the template has no `{` markers so
// messages without any placeholder skip the Replacer allocation. Otherwise
// builds a single NewReplacer covering builtins + customs — one pass
// instead of N ReplaceAll calls, which matters when broadcasting to
// thousands of contacts.
func renderTemplate(tpl string, c Contact, customVars map[string]string) string {
	if !strings.ContainsRune(tpl, '{') {
		return tpl
	}
	pairs := make([]string, 0, 8+len(customVars)*2)
	pairs = append(pairs,
		"{nome}", c.Name,
		"{name}", c.Name,
		"{telefone}", c.Phone,
		"{phone}", c.Phone,
	)
	for k, v := range customVars {
		pairs = append(pairs, "{"+k+"}", v)
	}
	return strings.NewReplacer(pairs...).Replace(tpl)
}

func (app *App) listTemplates(w http.ResponseWriter, r *http.Request) {
	vars, err := app.contacts.GetTemplateVars(r.Context())
	if err != nil {
		handleError(w, r, http.StatusInternalServerError, err)
		return
	}
	if vars == nil {
		vars = make(map[string]string)
	}
	jsonResponse(w, http.StatusOK, vars)
}

func (app *App) upsertTemplate(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Key   string `json:"key"`
		Value string `json:"value"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	body.Key = strings.TrimSpace(body.Key)
	body.Key = strings.Trim(body.Key, "{}")
	if body.Key == "" {
		jsonErrorLocalized(w, r, http.StatusBadRequest, "err_key_required")
		return
	}
	if reservedTemplateKeys[strings.ToLower(body.Key)] {
		jsonErrorLocalized(w, r, http.StatusBadRequest, "err_reserved_var", body.Key)
		return
	}
	if err := app.contacts.SetTemplateVar(r.Context(), body.Key, body.Value); err != nil {
		handleError(w, r, http.StatusInternalServerError, err)
		return
	}
	jsonResponse(w, http.StatusOK, map[string]string{"key": body.Key, "value": body.Value})
}

func (app *App) deleteTemplate(w http.ResponseWriter, r *http.Request) {
	key := pathParam(r, "key")
	if key == "" {
		jsonErrorLocalized(w, r, http.StatusBadRequest, "err_key_required")
		return
	}
	if err := app.contacts.DeleteTemplateVar(r.Context(), key); err != nil {
		handleError(w, r, http.StatusInternalServerError, err)
		return
	}
	jsonResponse(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleTranslations returns the UI translation dictionary for the
// request's language (Accept-Language header). Falls back to en-US for
// unknown locales.
//
// The catalog is immutable for the lifetime of the binary, so we serve it
// from an ETag-keyed cache: clients hitting /api/translations again on a
// language switch get a 304 unless the binary itself was redeployed.
func (app *App) handleTranslations(w http.ResponseWriter, r *http.Request) {
	lang := getLang(r)
	cached := translationsForLang(lang)

	w.Header().Set("ETag", cached.etag)
	w.Header().Set("Cache-Control", "no-cache, must-revalidate")

	if r.Header.Get("If-None-Match") == cached.etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(cached.body)
}
