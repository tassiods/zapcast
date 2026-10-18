// SPDX-FileCopyrightText: 2025 Tássio Dias <https://github.com/tassiods/zapcast>
// SPDX-License-Identifier: Apache-2.0

package server

// Groups CRUD endpoints.

import "net/http"

type groupRequest struct {
	Name     string   `json:"name"`
	Contacts []string `json:"contacts"`
}

func (app *App) listGroups(w http.ResponseWriter, r *http.Request) {
	groups, err := app.contacts.GetGroups(r.Context())
	if err != nil {
		handleError(w, r, http.StatusInternalServerError, err)
		return
	}
	if groups == nil {
		groups = []Group{}
	}
	if page, limit, paginated := parsePagination(r); paginated {
		jsonResponse(w, http.StatusOK, paginate(groups, page, limit))
		return
	}
	jsonResponse(w, http.StatusOK, groups)
}

func (app *App) createGroup(w http.ResponseWriter, r *http.Request) {
	var body groupRequest
	if !decodeJSON(w, r, &body) {
		return
	}
	if body.Name == "" {
		jsonErrorLocalized(w, r, http.StatusBadRequest, "err_name_required")
		return
	}
	group, err := app.contacts.AddGroup(r.Context(), body.Name, body.Contacts)
	if err != nil {
		handleError(w, r, http.StatusBadRequest, err)
		return
	}
	jsonResponse(w, http.StatusCreated, group)
}

func (app *App) updateGroup(w http.ResponseWriter, r *http.Request) {
	groupName := pathParam(r, "name")
	if groupName == "" {
		jsonErrorLocalized(w, r, http.StatusBadRequest, "err_group_name_required")
		return
	}
	var body groupRequest
	if !decodeJSON(w, r, &body) {
		return
	}
	group, err := app.contacts.UpdateGroup(r.Context(), groupName, body.Name, body.Contacts)
	if err != nil {
		handleError(w, r, http.StatusBadRequest, err)
		return
	}
	jsonResponse(w, http.StatusOK, group)
}

func (app *App) deleteGroup(w http.ResponseWriter, r *http.Request) {
	groupName := pathParam(r, "name")
	if groupName == "" {
		jsonErrorLocalized(w, r, http.StatusBadRequest, "err_group_name_required")
		return
	}
	if err := app.contacts.DeleteGroup(r.Context(), groupName); err != nil {
		handleError(w, r, http.StatusInternalServerError, err)
		return
	}
	jsonResponse(w, http.StatusOK, map[string]bool{"ok": true})
}
