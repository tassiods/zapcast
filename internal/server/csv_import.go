// SPDX-FileCopyrightText: 2025 ZapCast contributors
// SPDX-License-Identifier: Apache-2.0

package server

// CSV import endpoint. Understands Google Contacts, Outlook, and legacy
// ZapCast formats. Input may be UTF-8 or UTF-16; separator auto-detected.

import (
	"bytes"
	"encoding/binary"
	"encoding/csv"
	"fmt"
	"io"
	"net/http"
	"strings"
	"unicode/utf16"
)

// ImportResponse is the 200 body emitted by /api/import.
type ImportResponse struct {
	Created       int `json:"created"`
	Updated       int `json:"updated"`
	Skipped       int `json:"skipped"`
	GroupsCreated int `json:"groupsCreated"`
	Total         int `json:"total"`
}

// handleImport is the JSON or multipart endpoint that ingests a CSV body
// and writes contacts + groups into the database.
func (app *App) handleImport(w http.ResponseWriter, r *http.Request) {
	rows, err := readImportBody(w, r)
	if err != nil {
		// readImportBody has already written the error response.
		return
	}
	if len(rows) == 0 {
		jsonErrorLocalized(w, r, http.StatusBadRequest, "err_csv_no_contacts")
		return
	}

	created, updated, skipped, groupsCreated, err := app.contacts.ImportContacts(r.Context(), rows)
	if err != nil {
		jsonErrorLocalized(w, r, http.StatusInternalServerError, "err_csv_import", err.Error())
		return
	}

	jsonResponse(w, http.StatusOK, ImportResponse{
		Created:       created,
		Updated:       updated,
		Skipped:       skipped,
		GroupsCreated: groupsCreated,
		Total:         len(rows),
	})
}

// readImportBody handles multipart or JSON body submissions.
func readImportBody(w http.ResponseWriter, r *http.Request) ([]ImportRow, error) {
	contentType := r.Header.Get("Content-Type")
	if strings.Contains(contentType, "multipart/form-data") {
		const maxImportBody = 10 << 20 // 10MB
		r.Body = http.MaxBytesReader(w, r.Body, maxImportBody+(1<<20))
		// #nosec G120 -- MaxBytesReader applied on the previous line.
		if err := r.ParseMultipartForm(maxImportBody); err != nil {
			jsonErrorLocalized(w, r, http.StatusBadRequest, "err_upload_processing", err.Error())
			return nil, err
		}
		file, _, err := r.FormFile("file")
		if err != nil {
			jsonErrorLocalized(w, r, http.StatusBadRequest, "err_upload_none")
			return nil, err
		}
		defer file.Close()
		rows, err := parseCSVImport(file)
		if err != nil {
			jsonErrorLocalized(w, r, http.StatusBadRequest, "err_csv_read", err.Error())
			return nil, err
		}
		return rows, nil
	}

	var body struct {
		CSV string `json:"csv"`
	}
	if !decodeJSON(w, r, &body) {
		return nil, errImportDecode
	}
	rows, err := parseCSVImport(strings.NewReader(body.CSV))
	if err != nil {
		jsonErrorLocalized(w, r, http.StatusBadRequest, "err_csv_read", err.Error())
		return nil, err
	}
	return rows, nil
}

// errImportDecode is a sentinel returned by readImportBody when the JSON
// body fails to decode — decodeJSON already wrote the error.
var errImportDecode = fmt.Errorf("import: json decode failed")

func parseCSVImport(r io.Reader) ([]ImportRow, error) {
	decoded, sep, err := decodeCSV(r)
	if err != nil {
		return nil, err
	}

	reader := csv.NewReader(decoded)
	reader.Comma = sep
	reader.TrimLeadingSpace = true
	reader.LazyQuotes = true
	reader.FieldsPerRecord = -1 // exports are often ragged

	records, err := reader.ReadAll()
	if err != nil {
		return nil, newErrKey("err_csv_read", err.Error())
	}
	if len(records) == 0 {
		return nil, newErrKey("err_csv_empty")
	}

	header := records[0]
	switch detectCSVFormat(header) {
	case "google":
		return parseGoogleRows(records[1:], header), nil
	case "outlook":
		return parseOutlookRows(records[1:], header), nil
	default:
		return parseLegacyRows(records, header)
	}
}

// decodeCSV strips a UTF-8/UTF-16 BOM, converts UTF-16 to UTF-8, and
// sniffs the field separator (',' vs ';') from the first line.
func decodeCSV(r io.Reader) (io.Reader, rune, error) {
	raw, err := io.ReadAll(r)
	if err != nil {
		return nil, 0, err
	}
	switch {
	case bytes.HasPrefix(raw, []byte{0xEF, 0xBB, 0xBF}):
		raw = raw[3:]
	case bytes.HasPrefix(raw, []byte{0xFF, 0xFE}):
		raw = utf16ToUTF8(raw[2:], binary.LittleEndian)
	case bytes.HasPrefix(raw, []byte{0xFE, 0xFF}):
		raw = utf16ToUTF8(raw[2:], binary.BigEndian)
	}
	return bytes.NewReader(raw), sniffSeparator(raw), nil
}

func utf16ToUTF8(b []byte, order binary.ByteOrder) []byte {
	if len(b)%2 != 0 {
		b = b[:len(b)-1]
	}
	u16 := make([]uint16, len(b)/2)
	for i := range u16 {
		u16[i] = order.Uint16(b[i*2:])
	}
	return []byte(string(utf16.Decode(u16)))
}

// sniffSeparator walks the first line outside quotes and picks ';' if it
// outnumbers ','. Defaults to ','.
func sniffSeparator(raw []byte) rune {
	commas, semis := 0, 0
	inQuote := false
	for _, c := range raw {
		if c == '"' {
			inQuote = !inQuote
			continue
		}
		if inQuote {
			continue
		}
		if c == '\n' || c == '\r' {
			if commas > 0 || semis > 0 {
				break
			}
			continue
		}
		switch c {
		case ',':
			commas++
		case ';':
			semis++
		}
	}
	if semis > commas {
		return ';'
	}
	return ','
}

// detectCSVFormat classifies the header into "google", "outlook" or "legacy".
func detectCSVFormat(header []string) string {
	hasName, hasPhoneN := false, false
	for _, h := range header {
		l := strings.ToLower(strings.TrimSpace(h))
		if l == "name" || l == "nome" {
			hasName = true
		}
		if strings.HasPrefix(l, "phone ") && strings.HasSuffix(l, "- value") {
			hasPhoneN = true
		}
	}
	if hasName && hasPhoneN {
		return "google"
	}
	for _, h := range header {
		l := strings.ToLower(strings.TrimSpace(h))
		if l == "first name" || l == "mobile phone" || l == "categories" {
			return "outlook"
		}
	}
	return "legacy"
}

func headerIndex(header []string) map[string]int {
	m := make(map[string]int, len(header))
	for i, h := range header {
		m[strings.ToLower(strings.TrimSpace(h))] = i
	}
	return m
}

// googleSystemLabels are labels Google adds automatically that users never
// want as groups.
var googleSystemLabels = map[string]bool{
	"* mycontacts":       true,
	"starred in android": true,
}

// cellGetter returns a closure that pulls a trimmed cell by header name.
func cellGetter(idx map[string]int) func([]string, string) string {
	return func(rec []string, key string) string {
		if i, ok := idx[key]; ok && i < len(rec) {
			return strings.TrimSpace(rec[i])
		}
		return ""
	}
}

func parseGoogleRows(records [][]string, header []string) []ImportRow {
	idx := headerIndex(header)
	cell := cellGetter(idx)

	// Every Phone N - Value / Phone N - Type pair present in the header.
	type phoneCol struct{ value, typ int }
	var phoneCols []phoneCol
	for n := 1; ; n++ {
		vi, ok := idx[fmt.Sprintf("phone %d - value", n)]
		if !ok {
			break
		}
		ti, hasT := idx[fmt.Sprintf("phone %d - type", n)]
		if !hasT {
			ti = -1
		}
		phoneCols = append(phoneCols, phoneCol{vi, ti})
	}

	out := make([]ImportRow, 0, len(records))
	for _, rec := range records {
		name := cell(rec, "name")
		if name == "" {
			name = strings.TrimSpace(strings.Join(nonEmpty([]string{
				cell(rec, "given name"),
				cell(rec, "family name"),
			}), " "))
		}
		if name == "" {
			continue
		}

		var mobile, firstValid string
		for _, pc := range phoneCols {
			if pc.value >= len(rec) {
				continue
			}
			typ := ""
			if pc.typ >= 0 && pc.typ < len(rec) {
				typ = strings.ToLower(rec[pc.typ])
			}
			// A single cell may hold multiple numbers joined by " ::: ".
			for _, raw := range strings.Split(rec[pc.value], " ::: ") {
				v := strings.TrimSpace(raw)
				if v == "" {
					continue
				}
				if _, err := validatePhone(v); err != nil {
					continue
				}
				if (strings.Contains(typ, "mobile") || strings.Contains(typ, "cell")) && mobile == "" {
					mobile = v
				} else if firstValid == "" {
					firstValid = v
				}
			}
		}
		phone := mobile
		if phone == "" {
			phone = firstValid
		}
		if phone == "" {
			continue
		}

		var groups []string
		if labels := cell(rec, "labels"); labels != "" {
			for _, l := range strings.Split(labels, " ::: ") {
				l = strings.TrimSpace(l)
				if l == "" || googleSystemLabels[strings.ToLower(l)] {
					continue
				}
				groups = append(groups, l)
			}
		}
		out = append(out, ImportRow{Name: name, Phone: phone, Groups: groups})
	}
	return out
}

// outlookPhoneCols enumerates Outlook phone columns in order of preference.
var outlookPhoneCols = []string{
	"mobile phone", "primary phone", "business phone", "home phone", "other phone",
}

func parseOutlookRows(records [][]string, header []string) []ImportRow {
	idx := headerIndex(header)
	cell := cellGetter(idx)

	out := make([]ImportRow, 0, len(records))
	for _, rec := range records {
		name := cell(rec, "display name")
		if name == "" {
			name = strings.TrimSpace(strings.Join(nonEmpty([]string{
				cell(rec, "first name"),
				cell(rec, "middle name"),
				cell(rec, "last name"),
			}), " "))
		}
		if name == "" {
			continue
		}

		phone := ""
		for _, col := range outlookPhoneCols {
			v := cell(rec, col)
			if v == "" {
				continue
			}
			if _, err := validatePhone(v); err != nil {
				continue
			}
			phone = v
			break
		}
		if phone == "" {
			continue
		}

		var groups []string
		if cats := cell(rec, "categories"); cats != "" {
			for _, g := range strings.Split(cats, ";") {
				g = strings.TrimSpace(g)
				if g != "" {
					groups = append(groups, g)
				}
			}
		}
		out = append(out, ImportRow{Name: name, Phone: phone, Groups: groups})
	}
	return out
}

func parseLegacyRows(records [][]string, header []string) ([]ImportRow, error) {
	colName, colPhone, colGroup := -1, -1, -1
	for i, h := range header {
		switch strings.ToLower(strings.TrimSpace(h)) {
		case "name", "nome":
			colName = i
		case "phone", "telefone", "tel", "numero", "número":
			colPhone = i
		case "group", "grupo":
			colGroup = i
		}
	}

	startRow := 1
	if colName == -1 && colPhone == -1 {
		if len(header) < 2 {
			return nil, newErrKey("err_csv_min_columns")
		}
		colName = 0
		colPhone = 1
		if len(header) >= 3 {
			colGroup = 2
		}
		startRow = 0
	}
	if colName == -1 || colPhone == -1 {
		return nil, newErrKey("err_csv_header_required")
	}

	source := records
	if startRow == 1 {
		source = records[1:]
	}
	rows := make([]ImportRow, 0, len(source))
	for _, rec := range source {
		if colName >= len(rec) || colPhone >= len(rec) {
			continue
		}
		name := strings.TrimSpace(rec[colName])
		phone := strings.TrimSpace(rec[colPhone])
		if name == "" || phone == "" {
			continue
		}
		row := ImportRow{Name: name, Phone: phone}
		if colGroup >= 0 && colGroup < len(rec) {
			if g := strings.TrimSpace(rec[colGroup]); g != "" {
				row.Groups = []string{g}
			}
		}
		rows = append(rows, row)
	}
	return rows, nil
}
