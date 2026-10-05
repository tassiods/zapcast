// SPDX-FileCopyrightText: 2025 ZapCast contributors
// SPDX-License-Identifier: Apache-2.0

package server

// CSV export. One row per (contact, group) pair so multi-group contacts
// round-trip through the importer. Cells are defused against formula injection.

import (
	"encoding/csv"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

// contentDispositionAttachment builds an RFC 6266 Content-Disposition value
// that carries both the legacy `filename=` (ASCII fallback) and the RFC 5987
// `filename*=UTF-8”…` form. The encoded form is the one browsers prefer
// when the name has non-ASCII characters or quote-significant punctuation.
func contentDispositionAttachment(filename string) string {
	return fmt.Sprintf(`attachment; filename=%q; filename*=UTF-8''%s`,
		filename, url.PathEscape(filename))
}

// sanitizeCSVCell defuses CSV formula injection by prefixing cells whose
// first character is interpreted by Excel/LibreOffice/Numbers as the start
// of a formula or shell-style command (=, +, -, @, TAB, CR). OWASP
// recommends prepending a single quote.
func sanitizeCSVCell(s string) string {
	if len(s) == 0 {
		return s
	}
	switch s[0] {
	case '=', '+', '-', '@', '\t', '\r':
		return "'" + s
	}
	return s
}

// exportContacts streams every contact as a CSV download. UTF-8 BOM is
// emitted up front so Excel detects the encoding without manual config.
func (app *App) exportContacts(w http.ResponseWriter, r *http.Request) {
	contacts, err := app.contacts.GetContacts(r.Context())
	if err != nil {
		handleError(w, r, http.StatusInternalServerError, err)
		return
	}
	filename := fmt.Sprintf("zapcast-contacts-%s.csv", time.Now().Format("2006-01-02"))
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", contentDispositionAttachment(filename))
	w.Header().Set("Cache-Control", "no-store")

	if _, werr := w.Write([]byte{0xEF, 0xBB, 0xBF}); werr != nil {
		app.logger.Warn("csv export: failed to write BOM", "err", werr)
		return
	}

	cw := csv.NewWriter(w)
	defer cw.Flush()

	if werr := cw.Write([]string{"Name", "Phone", "Group"}); werr != nil {
		app.logger.Warn("csv export: failed to write header", "err", werr)
		return
	}
	for _, c := range contacts {
		name := sanitizeCSVCell(c.Name)
		phone := sanitizeCSVCell(c.Phone)
		if len(c.Groups) == 0 {
			if werr := cw.Write([]string{name, phone, ""}); werr != nil {
				app.logger.Warn("csv export: failed to write row", "err", werr, "phone", maskPhone(c.Phone))
				return
			}
			continue
		}
		for _, g := range c.Groups {
			if werr := cw.Write([]string{name, phone, sanitizeCSVCell(g)}); werr != nil {
				app.logger.Warn("csv export: failed to write row", "err", werr, "phone", maskPhone(c.Phone))
				return
			}
		}
	}
}
