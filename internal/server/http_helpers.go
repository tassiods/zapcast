// SPDX-FileCopyrightText: 2025 ZapCast contributors
// SPDX-License-Identifier: Apache-2.0

package server

// Cross-cutting HTTP helpers: JSON decode, path params, pagination.

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
)

// maxBodyBytes caps any JSON request body. CSV imports and broadcast media
// uploads use a separate 10MB / 16MB multipart limit and override this.
const maxBodyBytes = 1 << 20 // 1 MiB

// decodeJSON reads the request body into v. Writes 400 on failure.
func decodeJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	defer func() { _ = r.Body.Close() }()
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		rid, _ := r.Context().Value(requestIDKey).(string)
		slog.Debug("decodeJSON rejected payload", "err", err, "request_id", rid, "path", r.URL.Path)
		jsonErrorLocalized(w, r, http.StatusBadRequest, "err_invalid_json")
		return false
	}
	return true
}

// pathParam returns the URL-decoded value of a path wildcard captured by
// the Go 1.22+ ServeMux. Wildcards come in percent-encoded form; decode
// once here so handlers can treat them as plain strings.
func pathParam(r *http.Request, name string) string {
	v, _ := url.PathUnescape(r.PathValue(name))
	return v
}

// parsePagination reads `page` and `limit` query params. Returns
// (page, limit, paginated). If paginated is false, the caller should
// return the full list (backwards-compatible behaviour).
func parsePagination(r *http.Request) (page, limit int, paginated bool) {
	p := r.URL.Query().Get("page")
	l := r.URL.Query().Get("limit")
	if p == "" && l == "" {
		return 0, 0, false
	}
	page, _ = strconv.Atoi(p)
	if page < 1 {
		page = 1
	}
	limit, _ = strconv.Atoi(l)
	if limit < 1 {
		limit = 50
	}
	if limit > 500 {
		limit = 500
	}
	return page, limit, true
}

// paginate slices items and wraps them in a standard envelope.
func paginate[T any](items []T, page, limit int) map[string]any {
	total := len(items)
	start := (page - 1) * limit
	end := start + limit
	if start > total {
		start = total
	}
	if end > total {
		end = total
	}
	return map[string]any{
		"items": items[start:end],
		"total": total,
		"page":  page,
		"limit": limit,
	}
}

// nonEmpty filters out empty strings, returning a freshly-sliced subset of in.
// Used by the CSV parsers to assemble names from multi-column inputs.
func nonEmpty(in []string) []string {
	out := in[:0]
	for _, s := range in {
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}
