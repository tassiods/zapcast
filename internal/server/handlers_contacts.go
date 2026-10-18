// SPDX-FileCopyrightText: 2025 Tássio Dias <https://github.com/tassiods/zapcast>
// SPDX-License-Identifier: Apache-2.0

package server

// Contacts CRUD endpoints. Phone is the identity key; the path wildcard is
// named `{phone}` even though the frontend treats it as an opaque id.

import (
	"net/http"
	"strings"
)

type contactRequest struct {
	Name   string   `json:"name"`
	Phone  string   `json:"phone"`
	Groups []string `json:"groups"`
}

func (app *App) listContacts(w http.ResponseWriter, r *http.Request) {
	q := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))

	// Paginated callers filter + slice in SQL — the table never has to fit
	// in memory to serve one page.
	if page, limit, paginated := parsePagination(r); paginated {
		items, total, err := app.contacts.SearchContacts(r.Context(), q, limit, (page-1)*limit)
		if err != nil {
			handleError(w, r, http.StatusInternalServerError, err)
			return
		}
		jsonResponse(w, http.StatusOK, map[string]any{
			"items": items,
			"total": total,
			"page":  page,
			"limit": limit,
		})
		return
	}

	// Unpaginated path keeps the full-list contract the SPA and CSV export
	// rely on.
	contacts, err := app.contacts.GetContacts(r.Context())
	if err != nil {
		handleError(w, r, http.StatusInternalServerError, err)
		return
	}
	if contacts == nil {
		contacts = []Contact{}
	}
	if q != "" {
		filtered := make([]Contact, 0, len(contacts))
		for _, c := range contacts {
			if strings.Contains(strings.ToLower(c.Name), q) || strings.Contains(c.Phone, q) {
				filtered = append(filtered, c)
			}
		}
		contacts = filtered
	}
	jsonResponse(w, http.StatusOK, contacts)
}

func (app *App) createContact(w http.ResponseWriter, r *http.Request) {
	var body contactRequest
	if !decodeJSON(w, r, &body) {
		return
	}
	if body.Name == "" || body.Phone == "" {
		jsonErrorLocalized(w, r, http.StatusBadRequest, "err_name_phone_required")
		return
	}
	contact, err := app.contacts.AddContact(r.Context(), body.Name, body.Phone, body.Groups)
	if err != nil {
		handleError(w, r, http.StatusBadRequest, err)
		return
	}
	jsonResponse(w, http.StatusCreated, contact)
}

func (app *App) updateContact(w http.ResponseWriter, r *http.Request) {
	phone := pathParam(r, "phone")
	if phone == "" {
		jsonErrorLocalized(w, r, http.StatusBadRequest, "err_phone_required")
		return
	}
	var body contactRequest
	if !decodeJSON(w, r, &body) {
		return
	}
	contact, err := app.contacts.UpdateContact(r.Context(), phone, body.Name, body.Phone, body.Groups)
	if err != nil {
		handleError(w, r, http.StatusNotFound, err)
		return
	}
	jsonResponse(w, http.StatusOK, contact)
}

func (app *App) deleteContact(w http.ResponseWriter, r *http.Request) {
	phone := pathParam(r, "phone")
	if phone == "" {
		jsonErrorLocalized(w, r, http.StatusBadRequest, "err_phone_required")
		return
	}
	if err := app.contacts.DeleteContact(r.Context(), phone); err != nil {
		status := http.StatusInternalServerError
		if le, ok := asLocalized(err); ok && le.LocalizedKey() == "err_contact_not_found" {
			status = http.StatusNotFound
		}
		handleError(w, r, status, err)
		return
	}
	jsonResponse(w, http.StatusOK, map[string]bool{"ok": true})
}
