// SPDX-FileCopyrightText: 2025 ZapCast contributors
// SPDX-License-Identifier: Apache-2.0

package server

// Broadcast history + aggregate stats endpoints.

import (
	"net/http"
	"strconv"
)

// BroadcastStatsResponse is the typed body of /api/stats.
type BroadcastStatsResponse struct {
	Broadcasts int `json:"broadcasts"`
	Total      int `json:"total"`
	OK         int `json:"ok"`
	Fail       int `json:"fail"`
	Delivered  int `json:"delivered"`
	Read       int `json:"read"`
}

// historyPage wraps the broadcasts list with a cursor for the next page.
// `nextBefore` is omitted when the current page didn't fill its limit.
type historyPage struct {
	Items      []BroadcastSummary `json:"items"`
	NextBefore int64              `json:"nextBefore,omitempty"`
}

func (app *App) handleBroadcastList(w http.ResponseWriter, r *http.Request) {
	limit := 100
	if l, _ := strconv.Atoi(r.URL.Query().Get("limit")); l > 0 {
		if l > 500 {
			l = 500
		}
		limit = l
	}
	// Optional cursor pagination by id (DESC). `?before=<id>` returns the
	// page older than that id; missing/0 returns the newest page.
	var before int64
	if b, err := strconv.ParseInt(r.URL.Query().Get("before"), 10, 64); err == nil && b > 0 {
		before = b
	}
	items, err := app.contacts.ListBroadcasts(r.Context(), limit, before)
	if err != nil {
		handleError(w, r, http.StatusInternalServerError, err)
		return
	}
	resp := historyPage{Items: items}
	if len(items) == limit {
		resp.NextBefore = items[len(items)-1].ID
	}
	jsonResponse(w, http.StatusOK, resp)
}

func (app *App) handleBroadcastByID(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		jsonErrorLocalized(w, r, http.StatusBadRequest, "err_invalid_id")
		return
	}
	msgs, err := app.contacts.GetBroadcastMessages(r.Context(), id)
	if err != nil {
		handleError(w, r, http.StatusInternalServerError, err)
		return
	}
	jsonResponse(w, http.StatusOK, msgs)
}

func (app *App) handleStats(w http.ResponseWriter, r *http.Request) {
	stats, err := app.contacts.GetBroadcastStats(r.Context())
	if err != nil {
		handleError(w, r, http.StatusInternalServerError, err)
		return
	}
	jsonResponse(w, http.StatusOK, BroadcastStatsResponse{
		Broadcasts: stats["broadcasts"],
		Total:      stats["total"],
		OK:         stats["ok"],
		Fail:       stats["fail"],
		Delivered:  stats["delivered"],
		Read:       stats["read"],
	})
}
