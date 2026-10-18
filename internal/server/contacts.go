// SPDX-FileCopyrightText: 2025 Tássio Dias <https://github.com/tassiods/zapcast>
// SPDX-License-Identifier: Apache-2.0

package server

// SQLite-backed store for contacts, groups, broadcasts and template variables.
// Every method takes a context.Context so callers can propagate cancellation.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

// errKey is a translation key masquerading as an error.
type errKey struct {
	key  string
	args []any
}

func (k *errKey) Error() string                 { return k.key }
func (k *errKey) LocalizedKey() string          { return k.key }
func (k *errKey) LocalizedArgs() []any          { return k.args }
func (k *errKey) Translate(lang langKey) string { return TForLang(lang, k.key, k.args...) }

func newErrKey(key string, args ...any) *errKey {
	return &errKey{key: key, args: args}
}

// Contact is an address-book entry with group memberships.
type Contact struct {
	ID     string   `json:"id"`
	Name   string   `json:"name"`
	Phone  string   `json:"phone"`
	Groups []string `json:"groups,omitempty"`
}

// Group is a named bucket of phone numbers to broadcast to.
type Group struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	Contacts []string `json:"contacts"`
	Count    int      `json:"count"`
}

// ImportRow represents a row from CSV import. One contact may land in
// multiple groups (Google "Labels" / Outlook "Categories" are multi-valued).
type ImportRow struct {
	Name   string
	Phone  string
	Groups []string
}

// ContactStore wraps the *sql.DB handle. SetMaxOpenConns(1) serialises
// ALL access — readers queue on the pool behind writers, so WAL's
// reader/writer concurrency never engages. The trade-off is deliberate:
// one connection sidesteps SQLITE_BUSY handling entirely, and queries
// here are short. Revisit (separate read pool) if read latency under
// long broadcasts ever matters.
type ContactStore struct {
	db     *sql.DB
	logger *slog.Logger
}

// NewContactStore opens (or creates) the SQLite database and applies pending
// migrations. PRAGMAs are embedded in the DSN so every pooled connection
// picks them up.
func NewContactStore(ctx context.Context, lg *slog.Logger, dbPath string) (*ContactStore, error) {
	dsn := dbPath +
		"?_foreign_keys=on" +
		"&_journal_mode=WAL" +
		"&_busy_timeout=5000" +
		"&_synchronous=FULL" +
		"&_cache_size=-8000"
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, fmt.Errorf("err_db_open: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(0)

	if err := runMigrations(ctx, lg, db); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("err_migration: %w", err)
	}

	return &ContactStore{db: db, logger: lg}, nil
}

func (s *ContactStore) Close() error {
	return s.db.Close()
}

// WipeAll deletes every contact, group, membership and broadcast row.
// The order respects foreign keys.
func (s *ContactStore) WipeAll(ctx context.Context) error {
	stmts := []string{
		"DELETE FROM broadcast_messages",
		"DELETE FROM broadcasts",
		"DELETE FROM group_members",
		"DELETE FROM contact_groups",
		"DELETE FROM contacts",
		"DELETE FROM template_vars",
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for _, q := range stmts {
		if _, err := tx.ExecContext(ctx, q); err != nil {
			return fmt.Errorf("wipe %q: %w", q, err)
		}
	}
	return tx.Commit()
}

// --- Internal tx helpers ---
// ensureGroupID returns the group id, creating it if missing.
func ensureGroupID(ctx context.Context, tx *sql.Tx, name string) (int64, error) {
	if _, err := tx.ExecContext(ctx, "INSERT OR IGNORE INTO contact_groups (name) VALUES (?)", name); err != nil {
		return 0, err
	}
	var id int64
	if err := tx.QueryRowContext(ctx, "SELECT id FROM contact_groups WHERE name = ?", name).Scan(&id); err != nil {
		return 0, err
	}
	return id, nil
}

// addMember inserts a (group_id, phone) row. Duplicates are silently ignored.
// Returns whether a new row was added.
func addMember(ctx context.Context, tx *sql.Tx, groupID int64, phone string) (bool, error) {
	r, err := tx.ExecContext(ctx, "INSERT OR IGNORE INTO group_members (group_id, phone) VALUES (?, ?)", groupID, phone)
	if err != nil {
		return false, err
	}
	n, _ := r.RowsAffected()
	return n > 0, nil
}

// assignGroups walks groupNames, creating groups and membership rows.
// Empty names and per-name errors are dropped.
func assignGroups(ctx context.Context, tx *sql.Tx, phone string, groupNames []string) []string {
	out := make([]string, 0, len(groupNames))
	for _, gn := range groupNames {
		gn = strings.TrimSpace(gn)
		if gn == "" {
			continue
		}
		gid, err := ensureGroupID(ctx, tx, gn)
		if err != nil {
			continue
		}
		if _, err := addMember(ctx, tx, gid, phone); err == nil {
			out = append(out, gn)
		}
	}
	return out
}

// --- Template Variables ---

func (s *ContactStore) GetTemplateVars(ctx context.Context) (map[string]string, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT key, value FROM template_vars ORDER BY key")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	vars := make(map[string]string)
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		vars[k] = v
	}
	return vars, rows.Err()
}

func (s *ContactStore) SetTemplateVar(ctx context.Context, key, value string) error {
	_, err := s.db.ExecContext(ctx,
		"INSERT INTO template_vars (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value",
		key, value,
	)
	return err
}

func (s *ContactStore) DeleteTemplateVar(ctx context.Context, key string) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM template_vars WHERE key = ?", key)
	return err
}

// --- Contacts ---

// GetContacts returns all contacts with their group memberships.
func (s *ContactStore) GetContacts(ctx context.Context) ([]Contact, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT c.phone, c.name,
			COALESCE(GROUP_CONCAT(g.name, '|||'), '')
		FROM contacts c
		LEFT JOIN group_members gm ON gm.phone = c.phone
		LEFT JOIN contact_groups g ON g.id = gm.group_id
		GROUP BY c.phone
		ORDER BY c.name COLLATE NOCASE
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var contacts []Contact
	for rows.Next() {
		var c Contact
		var groupsStr string
		if err := rows.Scan(&c.Phone, &c.Name, &groupsStr); err != nil {
			return nil, err
		}
		c.ID = c.Phone
		if groupsStr != "" {
			c.Groups = strings.Split(groupsStr, "|||")
		} else {
			c.Groups = []string{}
		}
		contacts = append(contacts, c)
	}
	return contacts, rows.Err()
}

// escapeLike neutralises LIKE wildcards in user input. Used with ESCAPE '\'.
func escapeLike(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, "%", `\%`)
	return strings.ReplaceAll(s, "_", `\_`)
}

// SearchContacts returns one page of contacts matching q (name or phone,
// case-insensitive; empty q matches all) plus the total match count.
// Filter + slice happen in SQL so a large table never materialises in
// memory just to serve one page.
func (s *ContactStore) SearchContacts(ctx context.Context, q string, limit, offset int) ([]Contact, int, error) {
	like := "%" + escapeLike(strings.ToLower(q)) + "%"

	var total int
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM contacts
		 WHERE ? = '' OR lower(name) LIKE ? ESCAPE '\' OR phone LIKE ? ESCAPE '\'`,
		q, like, like,
	).Scan(&total); err != nil {
		return nil, 0, err
	}

	rows, err := s.db.QueryContext(ctx, `
		SELECT c.phone, c.name,
			COALESCE(GROUP_CONCAT(g.name, '|||'), '')
		FROM contacts c
		LEFT JOIN group_members gm ON gm.phone = c.phone
		LEFT JOIN contact_groups g ON g.id = gm.group_id
		WHERE ? = '' OR lower(c.name) LIKE ? ESCAPE '\' OR c.phone LIKE ? ESCAPE '\'
		GROUP BY c.phone
		ORDER BY c.name COLLATE NOCASE
		LIMIT ? OFFSET ?`,
		q, like, like, limit, offset,
	)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	contacts := []Contact{}
	for rows.Next() {
		var c Contact
		var groupsStr string
		if err := rows.Scan(&c.Phone, &c.Name, &groupsStr); err != nil {
			return nil, 0, err
		}
		c.ID = c.Phone
		if groupsStr != "" {
			c.Groups = strings.Split(groupsStr, "|||")
		} else {
			c.Groups = []string{}
		}
		contacts = append(contacts, c)
	}
	return contacts, total, rows.Err()
}

func (s *ContactStore) AddContact(ctx context.Context, name, phone string, groupNames []string) (Contact, error) {
	cleaned, err := validatePhone(phone)
	if err != nil {
		return Contact{}, err
	}
	phone = cleaned

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Contact{}, err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, "INSERT INTO contacts (phone, name) VALUES (?, ?)", phone, name); err != nil {
		return Contact{}, newErrKey("err_contact_exists", phone)
	}

	assignedGroups := assignGroups(ctx, tx, phone, groupNames)

	if err := tx.Commit(); err != nil {
		return Contact{}, err
	}
	return Contact{ID: phone, Name: name, Phone: phone, Groups: assignedGroups}, nil
}

func (s *ContactStore) UpdateContact(ctx context.Context, oldPhone, newName, newPhone string, groupNames []string) (Contact, error) {
	if newPhone != "" {
		cleaned, err := validatePhone(newPhone)
		if err != nil {
			return Contact{}, err
		}
		newPhone = cleaned
	} else {
		newPhone = oldPhone
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Contact{}, err
	}
	defer func() { _ = tx.Rollback() }()

	if newName == "" {
		if err := tx.QueryRowContext(ctx, "SELECT name FROM contacts WHERE phone = ?", oldPhone).Scan(&newName); err != nil {
			return Contact{}, newErrKey("err_contact_not_found")
		}
	}

	res, err := tx.ExecContext(ctx, "UPDATE contacts SET phone = ?, name = ? WHERE phone = ?", newPhone, newName, oldPhone)
	if err != nil {
		return Contact{}, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return Contact{}, newErrKey("err_contact_not_found")
	}

	// Update group memberships if provided (nil = don't change, empty = remove all)
	if groupNames != nil {
		if _, err := tx.ExecContext(ctx, "DELETE FROM group_members WHERE phone = ?", newPhone); err != nil {
			return Contact{}, err
		}
		assignGroups(ctx, tx, newPhone, groupNames)
	}

	if err := tx.Commit(); err != nil {
		return Contact{}, err
	}

	finalGroups := groupNames
	if finalGroups == nil {
		finalGroups = []string{}
	}
	return Contact{ID: newPhone, Name: newName, Phone: newPhone, Groups: finalGroups}, nil
}

// DeleteContact removes a contact and all its group memberships.
// Returns err_contact_not_found when no row matched.
func (s *ContactStore) DeleteContact(ctx context.Context, phone string) error {
	res, err := s.db.ExecContext(ctx, "DELETE FROM contacts WHERE phone = ?", phone)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return newErrKey("err_contact_not_found")
	}
	return nil
}

// --- Groups ---

// GetGroups returns groups with member counts and phone lists.
func (s *ContactStore) GetGroups(ctx context.Context) ([]Group, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT g.name,
			COUNT(gm.phone) AS cnt,
			COALESCE(GROUP_CONCAT(gm.phone, '|||'), '') AS phones
		FROM contact_groups g
		LEFT JOIN group_members gm ON gm.group_id = g.id
		GROUP BY g.id
		ORDER BY g.name COLLATE NOCASE
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var groups []Group
	for rows.Next() {
		var g Group
		var phones string
		if err := rows.Scan(&g.Name, &g.Count, &phones); err != nil {
			return nil, err
		}
		g.ID = g.Name
		if phones == "" {
			g.Contacts = []string{}
		} else {
			g.Contacts = strings.Split(phones, "|||")
		}
		groups = append(groups, g)
	}
	return groups, rows.Err()
}

func (s *ContactStore) AddGroup(ctx context.Context, name string, phones []string) (Group, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Group{}, err
	}
	defer func() { _ = tx.Rollback() }()

	res, err := tx.ExecContext(ctx, "INSERT INTO contact_groups (name) VALUES (?)", name)
	if err != nil {
		return Group{}, newErrKey("err_group_exists")
	}
	groupID, _ := res.LastInsertId()

	addedPhones := make([]string, 0, len(phones))
	for _, phone := range phones {
		if ok, err := addMember(ctx, tx, groupID, phone); err == nil && ok {
			addedPhones = append(addedPhones, phone)
		}
	}

	if err := tx.Commit(); err != nil {
		return Group{}, err
	}
	return Group{ID: name, Name: name, Contacts: addedPhones, Count: len(addedPhones)}, nil
}

// UpdateGroup updates group name and replaces members.
// addMember best-effort: FK/duplicate violations are silently dropped.
func (s *ContactStore) UpdateGroup(ctx context.Context, oldName, newName string, phones []string) (Group, error) {
	if newName == "" {
		newName = oldName
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Group{}, err
	}
	defer func() { _ = tx.Rollback() }()

	var groupID int64
	if err := tx.QueryRowContext(ctx, "SELECT id FROM contact_groups WHERE name = ?", oldName).Scan(&groupID); err != nil {
		return Group{}, newErrKey("err_group_not_found")
	}

	if newName != oldName {
		if _, err := tx.ExecContext(ctx, "UPDATE contact_groups SET name = ? WHERE id = ?", newName, groupID); err != nil {
			return Group{}, err
		}
	}

	// Replace members. addMember best-effort: FK/duplicate violations are
	// silently dropped (matches AddGroup); other errors abort the update.
	if _, err := tx.ExecContext(ctx, "DELETE FROM group_members WHERE group_id = ?", groupID); err != nil {
		return Group{}, err
	}
	addedPhones := make([]string, 0, len(phones))
	for _, phone := range phones {
		if ok, err := addMember(ctx, tx, groupID, phone); err == nil && ok {
			addedPhones = append(addedPhones, phone)
		}
	}

	if err := tx.Commit(); err != nil {
		return Group{}, err
	}
	return Group{ID: newName, Name: newName, Contacts: addedPhones, Count: len(addedPhones)}, nil
}

// DeleteGroup removes a group (cascade deletes memberships).
func (s *ContactStore) DeleteGroup(ctx context.Context, name string) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM contact_groups WHERE name = ?", name)
	return err
}

// GetContactsByPhones returns contact rows matching any of the given phone
// numbers, ordered by name.
func (s *ContactStore) GetContactsByPhones(ctx context.Context, phones []string) ([]Contact, error) {
	if len(phones) == 0 {
		return []Contact{}, nil
	}

	payload, err := json.Marshal(phones)
	if err != nil {
		return nil, fmt.Errorf("encode phones: %w", err)
	}

	rows, err := s.db.QueryContext(ctx, `
		SELECT c.phone, c.name
		FROM contacts c
		JOIN json_each(?) j ON j.value = c.phone
	`, string(payload))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	seen := make(map[string]Contact, len(phones))
	for rows.Next() {
		var c Contact
		if err := rows.Scan(&c.Phone, &c.Name); err != nil {
			return nil, err
		}
		c.ID = c.Phone
		seen[c.Phone] = c
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	out := make([]Contact, 0, len(seen))
	for _, c := range seen {
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool {
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	return out, nil
}

// GetGroupContacts returns full contact info for a group.
func (s *ContactStore) GetGroupContacts(ctx context.Context, groupName string) ([]Contact, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT c.phone, c.name
		FROM contacts c
		JOIN group_members gm ON gm.phone = c.phone
		JOIN contact_groups g ON g.id = gm.group_id
		WHERE g.name = ?
		ORDER BY c.name COLLATE NOCASE
	`, groupName)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var contacts []Contact
	for rows.Next() {
		var c Contact
		if err := rows.Scan(&c.Phone, &c.Name); err != nil {
			return nil, err
		}
		c.ID = c.Phone
		contacts = append(contacts, c)
	}
	return contacts, rows.Err()
}

// --- Import ---

// ImportContacts bulk-imports contacts from CSV rows.
func (s *ContactStore) ImportContacts(ctx context.Context, rows []ImportRow) (created, updated, skipped, groupsCreated int, err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, 0, 0, 0, err
	}
	defer func() { _ = tx.Rollback() }()

	groupCache := make(map[string]int64) // name -> id

	for _, r := range rows {
		phone, phoneErr := validatePhone(r.Phone)
		name := strings.TrimSpace(r.Name)
		if phoneErr != nil || name == "" {
			skipped++
			continue
		}

		// Upsert contact: try INSERT first, fall back to UPDATE so we know
		// whether this row was new or just refreshed.
		res, err := tx.ExecContext(ctx, "INSERT OR IGNORE INTO contacts (phone, name) VALUES (?, ?)", phone, name)
		if err != nil {
			skipped++
			continue
		}
		if n, _ := res.RowsAffected(); n > 0 {
			created++
		} else {
			if _, err := tx.ExecContext(ctx, "UPDATE contacts SET name = ? WHERE phone = ?", name, phone); err != nil {
				skipped++
				continue
			}
			updated++
		}

		// Assign contact to every listed group; DB N:N handles dedup.
		for _, gn := range r.Groups {
			gn = strings.TrimSpace(gn)
			if gn == "" {
				continue
			}
			gid, ok := groupCache[gn]
			if !ok {
				// Inline what ensureGroupID does so we can detect whether
				// this call truly created a new contact_groups row.
				ins, ierr := tx.ExecContext(ctx, "INSERT OR IGNORE INTO contact_groups (name) VALUES (?)", gn)
				if ierr != nil {
					continue
				}
				if n, _ := ins.RowsAffected(); n > 0 {
					groupsCreated++
				}
				if qerr := tx.QueryRowContext(ctx, "SELECT id FROM contact_groups WHERE name = ?", gn).Scan(&gid); qerr != nil {
					continue
				}
				groupCache[gn] = gid
			}
			if _, merr := addMember(ctx, tx, gid, phone); merr != nil {
				return 0, 0, 0, 0, merr
			}
		}
	}

	if err := tx.Commit(); err != nil {
		return 0, 0, 0, 0, err
	}
	return created, updated, skipped, groupsCreated, nil
}

// --- Broadcast history ---

// BroadcastSummary is one entry in the broadcast history list.
type BroadcastSummary struct {
	Status     string `json:"status"`
	Pending    int    `json:"pending"`
	Uncertain  int    `json:"uncertain"`
	Cancelled  int    `json:"cancelled"`
	Unresolved int    `json:"unresolved"`
	ID         int64  `json:"id"`
	GroupName  string `json:"groupName"`
	Message    string `json:"message"`
	Total      int    `json:"total"`
	OK         int    `json:"ok"`
	Fail       int    `json:"fail"`
	SentAt     string `json:"sentAt"`
	MediaType  string `json:"mediaType,omitempty"`
	MediaName  string `json:"mediaName,omitempty"`
}

// BroadcastMessage is a per-recipient delivery record.
type BroadcastMessage struct {
	ID          int64  `json:"id"`
	BroadcastID int64  `json:"broadcastId"`
	ContactName string `json:"contactName"`
	Phone       string `json:"phone"`
	MessageID   string `json:"messageId,omitempty"`
	Status      string `json:"status"`
	Error       string `json:"error,omitempty"`
	SentAt      string `json:"sentAt"`
	DeliveredAt string `json:"deliveredAt,omitempty"`
	ReadAt      string `json:"readAt,omitempty"`
}

func (s *ContactStore) UpdateBroadcastCounts(ctx context.Context, id int64, ok, fail int) error {
	var state string
	if err := s.db.QueryRowContext(ctx, `SELECT delivery_state FROM broadcasts WHERE id=?`, id).Scan(&state); err != nil {
		return err
	}
	if state != "legacy" {
		return s.reconcileDelivery(ctx, id)
	}
	_, err := s.db.ExecContext(ctx, "UPDATE broadcasts SET ok_count = ?, fail_count = ? WHERE id = ?", ok, fail, id)
	return err
}

// LastBroadcastTime returns the timestamp of the most recent broadcast,
// used to restore the cooldown across restarts. Returns the zero time when
// no broadcasts exist yet.
func (s *ContactStore) LastBroadcastTime(ctx context.Context) (time.Time, error) {
	var raw sql.NullString
	err := s.db.QueryRowContext(ctx, "SELECT MAX(sent_at) FROM broadcasts").Scan(&raw)
	if err != nil || !raw.Valid || raw.String == "" {
		return time.Time{}, err
	}
	// SQLite CURRENT_TIMESTAMP stores "YYYY-MM-DD HH:MM:SS" in UTC.
	if t, perr := time.Parse("2006-01-02 15:04:05", raw.String); perr == nil {
		return t, nil
	}
	return time.Time{}, nil
}

func (s *ContactStore) CreateBroadcast(ctx context.Context, groupName, message string, total, ok, fail int, mediaType, mediaName string) (int64, error) {
	res, err := s.db.ExecContext(ctx,
		"INSERT INTO broadcasts (group_name, message, total, ok_count, fail_count, media_type, media_name) VALUES (?, ?, ?, ?, ?, ?, ?)",
		groupName, message, total, ok, fail, nullIfEmpty(mediaType), nullIfEmpty(mediaName),
	)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// InsertBroadcastMessage records one per-recipient outcome and returns the
// new row id so callers (the outbox enqueue) can reference it precisely.
func (s *ContactStore) InsertBroadcastMessage(ctx context.Context, broadcastID int64, name, phone, messageID, status, errMsg string) (int64, error) {
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO broadcast_messages (broadcast_id, contact_name, phone, message_id, status, error)
		VALUES (?, ?, ?, ?, ?, ?)`,
		broadcastID, name, phone, nullIfEmpty(messageID), status, nullIfEmpty(errMsg),
	)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// UpdateMessageStatus updates delivery/read timestamps by whatsmeow message
// ID. Status only moves forward (sent → delivered → read).
func (s *ContactStore) UpdateMessageStatus(ctx context.Context, messageID, status string) error {
	var query string
	switch status {
	case "delivered":
		// Don't downgrade 'read' to 'delivered'. Always backfill delivered_at if null.
		query = `UPDATE broadcast_messages
		         SET status = CASE WHEN status = 'read' THEN status ELSE ? END,
		             delivered_at = COALESCE(delivered_at, CURRENT_TIMESTAMP)
		         WHERE message_id = ?`
	case "read":
		// Always promote to 'read'. Backfill delivered_at if it never arrived.
		query = `UPDATE broadcast_messages
		         SET status = ?,
		             delivered_at = COALESCE(delivered_at, CURRENT_TIMESTAMP),
		             read_at = CURRENT_TIMESTAMP
		         WHERE message_id = ?`
	default:
		return newErrKey("err_invalid_status")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `INSERT INTO delivery_receipts(message_id,status,read_at) VALUES(?,?,CASE WHEN ?='read' THEN CURRENT_TIMESTAMP ELSE NULL END)
 ON CONFLICT(message_id) DO UPDATE SET status=CASE WHEN delivery_receipts.status='read' THEN 'read' ELSE excluded.status END,read_at=COALESCE(delivery_receipts.read_at,excluded.read_at)`, messageID, status, status); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, query, status, messageID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM outbox WHERE status<>'inflight' AND broadcast_message_id IN (SELECT id FROM broadcast_messages WHERE message_id=? AND status IN ('delivered','read'))`, messageID); err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx, `SELECT DISTINCT broadcast_id FROM broadcast_messages WHERE message_id=?`, messageID)
	if err != nil {
		return err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, id := range ids {
		if _, err = tx.ExecContext(ctx, reconcileDeliverySQL, id, id, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ListBroadcasts returns the broadcast summary list (most recent first).
// When before > 0, only rows with id < before are returned (cursor pagination).
func (s *ContactStore) ListBroadcasts(ctx context.Context, limit int, before int64) ([]BroadcastSummary, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	query := `SELECT id, group_name, message, total, ok_count, fail_count, sent_at,
			COALESCE(media_type, ''), COALESCE(media_name, ''), delivery_state,
 (SELECT COUNT(*) FROM broadcast_messages WHERE broadcast_id=broadcasts.id AND status IN ('pending','inflight')),
 (SELECT COUNT(*) FROM outbox WHERE broadcast_id=broadcasts.id AND status='uncertain') +
 (SELECT COUNT(*) FROM broadcast_messages m WHERE m.broadcast_id=broadcasts.id AND m.status='uncertain' AND NOT EXISTS(SELECT 1 FROM outbox o WHERE o.broadcast_message_id=m.id AND o.status='uncertain')),
 (SELECT COUNT(*) FROM broadcast_messages WHERE broadcast_id=broadcasts.id AND status='cancelled'),
 MAX(0,total-(SELECT COUNT(*) FROM broadcast_messages WHERE broadcast_id=broadcasts.id))
		FROM broadcasts`
	args := []any{}
	if before > 0 {
		query += ` WHERE id < ?`
		args = append(args, before)
	}
	query += ` ORDER BY id DESC LIMIT ?`
	args = append(args, limit)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []BroadcastSummary{}
	for rows.Next() {
		var b BroadcastSummary
		if err := rows.Scan(&b.ID, &b.GroupName, &b.Message, &b.Total, &b.OK, &b.Fail, &b.SentAt, &b.MediaType, &b.MediaName, &b.Status, &b.Pending, &b.Uncertain, &b.Cancelled, &b.Unresolved); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// GetBroadcastMessages returns per-recipient messages for a given broadcast.
func (s *ContactStore) GetBroadcastMessages(ctx context.Context, broadcastID int64) ([]BroadcastMessage, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, broadcast_id, contact_name, phone,
			COALESCE(message_id, ''), status,
			COALESCE(error, ''), sent_at,
			COALESCE(delivered_at, ''), COALESCE(read_at, '')
		FROM broadcast_messages WHERE broadcast_id = ? ORDER BY id`, broadcastID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []BroadcastMessage{}
	for rows.Next() {
		var m BroadcastMessage
		if err := rows.Scan(&m.ID, &m.BroadcastID, &m.ContactName, &m.Phone,
			&m.MessageID, &m.Status, &m.Error, &m.SentAt, &m.DeliveredAt, &m.ReadAt); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// BroadcastWindow is the persisted rolling-hour cap state for one broadcast.
type BroadcastWindow struct {
	BroadcastID  int64
	WindowStart  time.Time
	SentInWindow int
}

// LoadBroadcastWindow returns the persisted window, or a zero-value
// window with the current time when no row exists yet.
func (s *ContactStore) LoadBroadcastWindow(ctx context.Context, broadcastID int64) (BroadcastWindow, error) {
	var raw sql.NullString
	var sent int
	err := s.db.QueryRowContext(ctx,
		"SELECT window_start, sent_in_window FROM broadcast_window WHERE broadcast_id = ?",
		broadcastID,
	).Scan(&raw, &sent)
	if errors.Is(err, sql.ErrNoRows) {
		return BroadcastWindow{BroadcastID: broadcastID, WindowStart: time.Now().UTC(), SentInWindow: 0}, nil
	}
	if err != nil {
		return BroadcastWindow{}, err
	}
	t, _ := time.Parse("2006-01-02 15:04:05", raw.String)
	return BroadcastWindow{BroadcastID: broadcastID, WindowStart: t, SentInWindow: sent}, nil
}

// SaveBroadcastWindow upserts the window row for broadcastID.
func (s *ContactStore) SaveBroadcastWindow(ctx context.Context, w BroadcastWindow) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO broadcast_window (broadcast_id, window_start, sent_in_window)
		VALUES (?, ?, ?)
		ON CONFLICT(broadcast_id) DO UPDATE SET
			window_start   = excluded.window_start,
			sent_in_window = excluded.sent_in_window
	`,
		w.BroadcastID,
		w.WindowStart.UTC().Format("2006-01-02 15:04:05"),
		w.SentInWindow,
	)
	return err
}

// ClearBroadcastWindow removes the row for a broadcast that has finished.
// Safe to call when no row exists.
func (s *ContactStore) ClearBroadcastWindow(ctx context.Context, broadcastID int64) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM broadcast_window WHERE broadcast_id = ?", broadcastID)
	return err
}

// TakeInterruptedBroadcasts returns the ids of broadcasts that still had a
// rolling-window row at boot — meaning their in-process send goroutine was
// killed by a restart before it finished (a clean finish clears the row) — and
// deletes those rows. Broadcasts are NOT resumed: the unsent recipients and
// their rendered bodies were never persisted, so the remainder is lost. This
// exists to surface that loss in the logs at boot rather than hide it.
func (s *ContactStore) TakeInterruptedBroadcasts(ctx context.Context) ([]int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	rows, err := tx.QueryContext(ctx, "SELECT broadcast_id FROM broadcast_window ORDER BY broadcast_id")
	if err != nil {
		return nil, err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}
	_ = rows.Close()

	if len(ids) > 0 {
		if _, err := tx.ExecContext(ctx, "DELETE FROM broadcast_window"); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return ids, nil
}

// GetBroadcastStats returns aggregate stats across all broadcasts.
func (s *ContactStore) GetBroadcastStats(ctx context.Context) (map[string]int, error) {
	var broadcasts, total, ok, fail, delivered, read int
	err := s.db.QueryRowContext(ctx, `
		SELECT
			(SELECT COUNT(*)                FROM broadcasts),
			(SELECT COALESCE(SUM(total),0)      FROM broadcasts),
			(SELECT COALESCE(SUM(ok_count),0)   FROM broadcasts),
			(SELECT COALESCE(SUM(fail_count),0) FROM broadcasts),
			(SELECT COUNT(*) FROM broadcast_messages WHERE status IN ('delivered','read')),
			(SELECT COUNT(*) FROM broadcast_messages WHERE status = 'read')
	`).Scan(&broadcasts, &total, &ok, &fail, &delivered, &read)
	if err != nil {
		return nil, err
	}
	return map[string]int{
		"broadcasts": broadcasts, "total": total, "ok": ok, "fail": fail,
		"delivered": delivered, "read": read,
	}, nil
}

// --- Helpers ---

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// cleanPhone normalizes a phone number to digits only (E.164 without +).
// Returns empty string if input has no digits.
func cleanPhone(phone string) string {
	var digits []byte
	for _, c := range phone {
		if c >= '0' && c <= '9' {
			digits = append(digits, byte(c))
		}
	}
	return string(digits)
}

// validatePhone ensures the phone number is a valid E.164 (7-15 digits,
// cannot start with 0). Returns the cleaned phone or an error.
func validatePhone(phone string) (string, error) {
	cleaned := cleanPhone(phone)
	if cleaned == "" {
		return "", newErrKey("err_phone_required")
	}
	if len(cleaned) < 7 {
		return "", newErrKey("err_phone_too_short", len(cleaned))
	}
	if len(cleaned) > 15 {
		return "", newErrKey("err_phone_too_long", len(cleaned))
	}
	if cleaned[0] == '0' {
		return "", newErrKey("err_phone_starts_zero")
	}
	return cleaned, nil
}
