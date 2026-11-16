package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"mime"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
)

// Upload management: the logged-in side of Shadowfax. Whoever holds the
// password or API key can list every live transfer, see where it came from and
// who has fetched it, extend or shorten its life, rename it, edit its note,
// add or remove files, download it without spending the recipient's rate
// limit, and revoke its PIN by issuing a new one.
//
// Login is a single shared secret, so "your uploads" is every upload on the
// server. That is why requireManageAuth refuses to serve any of this on an
// instance with no login configured.

// ------------------------------------------------------------------ provenance

type uploadMeta struct {
	method     string // web | multipart | raw
	auth       string // session | api_key | none
	ip         string
	agent      string
	expirySecs int64
}

const maxAgentLen = 300

func truncateAgent(ua string) string {
	if len(ua) <= maxAgentLen {
		return ua
	}
	ua = ua[:maxAgentLen]
	for !utf8.ValidString(ua) {
		ua = ua[:len(ua)-1]
	}
	return ua
}

func newUploadMeta(r *http.Request, method string, expirySecs int64) uploadMeta {
	return uploadMeta{
		method:     method,
		auth:       authMethodOf(r),
		ip:         clientIP(r),
		agent:      truncateAgent(r.UserAgent()),
		expirySecs: expirySecs,
	}
}

func (m uploadMeta) summary(files int, hasText bool) string {
	parts := []string{fmt.Sprintf("%d file(s)", files)}
	if hasText {
		parts = append(parts, "a note")
	}
	return strings.Join(parts, " and ") + " via " + m.method
}

// --------------------------------------------------------------------- events

const (
	eventCreated             = "created"
	eventOpened              = "opened"
	eventDownload            = "download"
	eventDownloadInterrupted = "download_interrupted"
	eventOwnerDownload       = "owner_download"
	eventExpiryChanged       = "expiry_changed"
	eventRenamed             = "renamed"
	eventNoteEdited          = "note_edited"
	eventPINRotated          = "pin_rotated"
	eventFilesAdded          = "files_added"
	eventFileDeleted         = "file_deleted"
)

const maxEventsShown = 200

// recordEvent appends to a transfer's activity log. The log lives and dies with
// the transfer (ON DELETE CASCADE), so it is shredded alongside the keys.
func recordEvent(filesetID int64, kind, detail, ip, agent string) {
	if _, err := db.Exec(
		`INSERT INTO transfer_events (fileset_id, at, kind, detail, ip, user_agent) VALUES (?, ?, ?, ?, ?, ?)`,
		filesetID, time.Now().UTC(), kind, detail, ip, truncateAgent(agent),
	); err != nil {
		// A transfer deleted mid-download lands here via the foreign key.
		log.Printf("record event %q for fileset %d: %v", kind, filesetID, err)
	}
}

// recordDownload logs a finished download. Only complete, non-owner downloads
// count towards the totals, so an owner checking a file does not inflate them.
func recordDownload(r *http.Request, filesetID int64, fileIDs []int64, detail string, complete, owner bool) {
	kind := eventDownload
	switch {
	case owner:
		kind = eventOwnerDownload
		if !complete {
			detail += " (interrupted)"
		}
	case !complete:
		kind = eventDownloadInterrupted
	}
	recordEvent(filesetID, kind, detail, clientIP(r), r.UserAgent())

	if !complete || owner {
		return
	}
	now := time.Now().UTC()
	db.Exec(`UPDATE filesets SET download_count = download_count + 1, last_download_at = ? WHERE id = ?`,
		now, filesetID)
	for _, id := range fileIDs {
		db.Exec(`UPDATE files SET download_count = download_count + 1 WHERE id = ?`, id)
	}
}

// ------------------------------------------------------------------ responses

type manageFile struct {
	ID            int64      `json:"id"`
	Name          string     `json:"name"`
	Size          int64      `json:"size"`
	DiskSize      int64      `json:"disk_size,omitempty"`
	ContentType   string     `json:"content_type"`
	Encrypted     bool       `json:"encrypted"`
	DownloadCount int        `json:"download_count"`
	AddedAt       *time.Time `json:"added_at,omitempty"`
	DownloadURL   string     `json:"download_url"`
	OwnerURL      string     `json:"owner_url"`

	storedName string
}

type manageUpload struct {
	Method              string `json:"method"`
	Auth                string `json:"auth"`
	IP                  string `json:"ip"`
	UserAgent           string `json:"user_agent"`
	RequestedExpirySecs int64  `json:"requested_expiry_secs"`
}

type manageEvent struct {
	At        time.Time `json:"at"`
	Kind      string    `json:"kind"`
	Detail    string    `json:"detail,omitempty"`
	IP        string    `json:"ip,omitempty"`
	UserAgent string    `json:"user_agent,omitempty"`
}

type manageTransfer struct {
	Pin            string        `json:"pin"`
	Label          string        `json:"label"`
	CreatedAt      time.Time     `json:"created_at"`
	ExpiresAt      *time.Time    `json:"expires_at"` // null when it never expires
	NeverExpires   bool          `json:"never_expires"`
	MaxExpiresAt   time.Time     `json:"max_expires_at"`
	ShareURL       string        `json:"share_url"`
	ZipURL         string        `json:"zip_url,omitempty"`
	OwnerZipURL    string        `json:"owner_zip_url,omitempty"`
	FileCount      int           `json:"file_count"`
	TotalSize      int64         `json:"total_size"`
	DiskSize       int64         `json:"disk_size,omitempty"`
	HasText        bool          `json:"has_text"`
	Text           *string       `json:"text,omitempty"`
	Encrypted      bool          `json:"encrypted"`
	Files          []manageFile  `json:"files"`
	Upload         manageUpload  `json:"upload"`
	ExtendCount    int           `json:"extend_count"`
	DownloadCount  int           `json:"download_count"`
	LastDownloadAt *time.Time    `json:"last_download_at,omitempty"`
	Events         []manageEvent `json:"events,omitempty"`

	id        int64
	expiresAt time.Time
}

func contentTypeFor(name string) string {
	if t := mime.TypeByExtension(strings.ToLower(filepath.Ext(name))); t != "" {
		if mt, _, err := mime.ParseMediaType(t); err == nil {
			return mt
		}
	}
	return "application/octet-stream"
}

const transferColumns = `f.id, f.pin, f.label, f.created_at, f.expires_at,
	f.upload_method, f.auth_method, f.uploader_ip, f.uploader_agent, f.requested_expiry_secs,
	f.extend_count, f.download_count, f.last_download_at,
	((f.text_blob IS NOT NULL AND length(f.text_blob) > 0) OR f.text_content != '')`

type rowScanner interface{ Scan(dest ...any) error }

func scanTransfer(row rowScanner) (manageTransfer, error) {
	var t manageTransfer
	var last sql.NullTime
	err := row.Scan(&t.id, &t.Pin, &t.Label, &t.CreatedAt, &t.expiresAt,
		&t.Upload.Method, &t.Upload.Auth, &t.Upload.IP, &t.Upload.UserAgent, &t.Upload.RequestedExpirySecs,
		&t.ExtendCount, &t.DownloadCount, &last, &t.HasText)
	if last.Valid {
		lt := last.Time
		t.LastDownloadAt = &lt
	}
	t.ExpiresAt = expiryJSON(t.expiresAt)
	t.NeverExpires = isPermanent(t.expiresAt)
	t.Files = []manageFile{}
	t.Encrypted = true
	return t, err
}

// finish fills in the URLs and totals once the file list is attached.
func (t *manageTransfer) finish(r *http.Request) {
	base := baseURL(r)
	ownerBase := fmt.Sprintf("%s/api/manage/transfers/%s", base, t.Pin)
	t.ShareURL = fmt.Sprintf("%s/?pin=%s", base, t.Pin)
	t.MaxExpiresAt = time.Now().UTC().Add(time.Duration(maxExpirySecs) * time.Second)
	t.FileCount = len(t.Files)
	t.TotalSize = 0
	for i := range t.Files {
		f := &t.Files[i]
		f.DownloadURL = fmt.Sprintf("%s/api/transfer/%s/download/%s", base, t.Pin, url.PathEscape(f.Name))
		f.OwnerURL = fmt.Sprintf("%s/files/%d", ownerBase, f.ID)
		t.TotalSize += f.Size
		if !f.Encrypted {
			t.Encrypted = false
		}
	}
	if len(t.Files) > 0 {
		t.ZipURL = fmt.Sprintf("%s/api/transfer/%s/zip", base, t.Pin)
		t.OwnerZipURL = ownerBase + "/zip"
	}
}

func scanManageFile(row rowScanner) (int64, manageFile, error) {
	var filesetID int64
	var f manageFile
	var added sql.NullTime
	err := row.Scan(&filesetID, &f.ID, &f.Name, &f.storedName, &f.Size, &f.Encrypted, &f.DownloadCount, &added)
	if added.Valid {
		at := added.Time
		f.AddedAt = &at
	}
	f.ContentType = contentTypeFor(f.Name)
	return filesetID, f, err
}

const fileColumns = `fileset_id, id, original_name, stored_name, size, wrapped_key IS NOT NULL, download_count, added_at`

// lookupManaged resolves a live transfer by PIN for an authenticated owner.
func lookupManaged(w http.ResponseWriter, r *http.Request) (manageTransfer, bool) {
	t, err := scanTransfer(db.QueryRow(
		`SELECT `+transferColumns+` FROM filesets f WHERE f.pin = ? AND f.expires_at > datetime('now')`,
		chi.URLParam(r, "pin")))
	switch {
	case errors.Is(err, sql.ErrNoRows):
		writeError(w, http.StatusNotFound, "transfer not found or expired")
		return t, false
	case err != nil:
		log.Printf("manage lookup error: %v", err)
		writeError(w, http.StatusInternalServerError, "db error")
		return t, false
	}
	return t, true
}

// loadDetail builds the full view of one transfer: files with on-disk sizes,
// the decrypted note, and the activity log.
func loadDetail(r *http.Request, t manageTransfer) (manageTransfer, error) {
	rows, err := db.Query(`SELECT `+fileColumns+` FROM files WHERE fileset_id = ? ORDER BY id`, t.id)
	if err != nil {
		return t, err
	}
	for rows.Next() {
		_, f, err := scanManageFile(rows)
		if err != nil {
			continue
		}
		t.Files = append(t.Files, f)
	}
	rows.Close()

	for i := range t.Files {
		if info, err := os.Stat(filepath.Join(transferDir(t.Pin), t.Files[i].storedName)); err == nil {
			t.Files[i].DiskSize = info.Size()
			t.DiskSize += info.Size()
		}
	}

	if t.HasText {
		text := readText(t.id)
		t.Text = &text
	}

	evRows, err := db.Query(
		`SELECT at, kind, detail, ip, user_agent FROM transfer_events
		 WHERE fileset_id = ? ORDER BY id DESC LIMIT ?`, t.id, maxEventsShown)
	if err != nil {
		return t, err
	}
	t.Events = []manageEvent{}
	for evRows.Next() {
		var e manageEvent
		if err := evRows.Scan(&e.At, &e.Kind, &e.Detail, &e.IP, &e.UserAgent); err == nil {
			t.Events = append(t.Events, e)
		}
	}
	evRows.Close()

	t.finish(r)
	return t, nil
}

func writeDetail(w http.ResponseWriter, r *http.Request, pin string, status int) {
	t, err := scanTransfer(db.QueryRow(`SELECT `+transferColumns+` FROM filesets f WHERE f.pin = ?`, pin))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db error")
		return
	}
	detail, err := loadDetail(r, t)
	if err != nil {
		log.Printf("manage detail error: %v", err)
		writeError(w, http.StatusInternalServerError, "db error")
		return
	}
	writeJSON(w, status, detail)
}

// ------------------------------------------------------------------- handlers

// GET /api/manage/transfers
func handleManageList(w http.ResponseWriter, r *http.Request) {
	rows, err := db.Query(`SELECT ` + transferColumns +
		` FROM filesets f WHERE f.expires_at > datetime('now') ORDER BY f.created_at DESC, f.id DESC`)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db error")
		return
	}
	transfers := []manageTransfer{}
	index := map[int64]int{}
	for rows.Next() {
		t, err := scanTransfer(rows)
		if err != nil {
			log.Printf("manage list scan: %v", err)
			continue
		}
		index[t.id] = len(transfers)
		transfers = append(transfers, t)
	}
	rows.Close()

	fileRows, err := db.Query(`SELECT ` + fileColumns + ` FROM files
		WHERE fileset_id IN (SELECT id FROM filesets WHERE expires_at > datetime('now')) ORDER BY id`)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db error")
		return
	}
	for fileRows.Next() {
		filesetID, f, err := scanManageFile(fileRows)
		if err != nil {
			continue
		}
		if i, ok := index[filesetID]; ok {
			transfers[i].Files = append(transfers[i].Files, f)
		}
	}
	fileRows.Close()

	type stats struct {
		Transfers  int        `json:"transfers"`
		Files      int        `json:"files"`
		Bytes      int64      `json:"bytes"`
		Downloads  int        `json:"downloads"`
		Permanent  int        `json:"permanent"`
		NextExpiry *time.Time `json:"next_expiry,omitempty"`
	}
	var s stats
	for i := range transfers {
		t := &transfers[i]
		t.finish(r)
		s.Transfers++
		s.Files += t.FileCount
		s.Bytes += t.TotalSize
		s.Downloads += t.DownloadCount
		if t.NeverExpires {
			s.Permanent++
		}
		if !t.NeverExpires && (s.NextExpiry == nil || t.expiresAt.Before(*s.NextExpiry)) {
			exp := t.expiresAt
			s.NextExpiry = &exp
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"transfers":       transfers,
		"stats":           s,
		"server_time":     time.Now().UTC(),
		"max_expiry_secs": maxExpirySecs,
		"allow_permanent": allowPermanent,
	})
}

// GET /api/manage/transfers/{pin}
func handleManageGet(w http.ResponseWriter, r *http.Request) {
	t, ok := lookupManaged(w, r)
	if !ok {
		return
	}
	writeDetail(w, r, t.Pin, http.StatusOK)
}

func sanitizeLabel(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= 0x20 && r != 0x7f {
			b.WriteRune(r)
		}
	}
	out := strings.TrimSpace(b.String())
	if utf8.RuneCountInString(out) > 120 {
		out = string([]rune(out)[:120])
	}
	return out
}

const minRemainingLife = time.Minute

// PATCH /api/manage/transfers/{pin}
//
//	{"label": "...", "text": "...",
//	 "extend_secs": 86400 | "expires_in": 3600 | "expires_at": "<RFC3339>" |
//	 "never_expires": true}
//
// Any one expiry field moves the deadline; the result is clamped to
// MAX_EXPIRY_SECONDS from now, the same ceiling a fresh upload gets.
// never_expires makes the transfer permanent (when ALLOW_PERMANENT_UPLOADS is
// on); any of the other expiry fields gives a permanent transfer a deadline again.
func handleManageUpdate(w http.ResponseWriter, r *http.Request) {
	t, ok := lookupManaged(w, r)
	if !ok {
		return
	}
	var body struct {
		Label      *string    `json:"label"`
		Text       *string    `json:"text"`
		ExtendSecs *int64     `json:"extend_secs"`
		ExpiresIn  *int64     `json:"expires_in"`
		ExpiresAt  *time.Time `json:"expires_at"`
		Never      *bool      `json:"never_expires"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest,
			"expected JSON body with any of: label, text, extend_secs, expires_in, expires_at, never_expires")
		return
	}

	expiryFields := 0
	for _, set := range []bool{body.ExtendSecs != nil, body.ExpiresIn != nil, body.ExpiresAt != nil, body.Never != nil} {
		if set {
			expiryFields++
		}
	}
	if expiryFields > 1 {
		writeError(w, http.StatusBadRequest, "send only one of extend_secs, expires_in, expires_at, never_expires")
		return
	}
	if body.Never != nil {
		if !*body.Never {
			writeError(w, http.StatusBadRequest,
				"to give a transfer an expiry, send expires_in, expires_at or extend_secs")
			return
		}
		if !allowPermanent {
			writeError(w, http.StatusForbidden, "this server does not allow transfers that never expire")
			return
		}
	}

	now := time.Now().UTC()
	var newExpiry *time.Time
	clamped := false
	if body.Never != nil {
		target := neverExpires
		newExpiry = &target
	} else if expiryFields == 1 {
		var target time.Time
		switch {
		case body.ExtendSecs != nil:
			// A permanent transfer has no deadline to extend; count from now.
			base := t.expiresAt
			if t.NeverExpires {
				base = now
			}
			target = base.Add(time.Duration(*body.ExtendSecs) * time.Second)
		case body.ExpiresIn != nil:
			target = now.Add(time.Duration(*body.ExpiresIn) * time.Second)
		default:
			target = *body.ExpiresAt
		}
		target = target.UTC()
		if ceiling := now.Add(time.Duration(maxExpirySecs) * time.Second); target.After(ceiling) {
			target, clamped = ceiling, true
		}
		if target.Before(now.Add(minRemainingLife)) {
			writeError(w, http.StatusBadRequest,
				"new expiry must be at least a minute away; delete the transfer to remove it now")
			return
		}
		newExpiry = &target
	}

	if body.Text != nil && *body.Text == "" {
		var files int
		db.QueryRow(`SELECT COUNT(*) FROM files WHERE fileset_id = ?`, t.id).Scan(&files)
		if files == 0 {
			writeError(w, http.StatusBadRequest,
				"this transfer has no files, so removing the note would leave it empty; delete it instead")
			return
		}
	}
	if body.Text != nil && len(*body.Text) > 1<<20 {
		writeError(w, http.StatusRequestEntityTooLarge, "note exceeds 1 MiB")
		return
	}

	ip, ua := clientIP(r), r.UserAgent()

	if body.Label != nil {
		label := sanitizeLabel(*body.Label)
		if label != t.Label {
			if _, err := db.Exec(`UPDATE filesets SET label = ? WHERE id = ?`, label, t.id); err != nil {
				writeError(w, http.StatusInternalServerError, "db error")
				return
			}
			detail := "label cleared"
			if label != "" {
				detail = fmt.Sprintf("renamed to %q", label)
			}
			recordEvent(t.id, eventRenamed, detail, ip, ua)
		}
	}

	if body.Text != nil {
		var blob, key []byte
		if *body.Text != "" {
			var err error
			if blob, key, err = sealText(*body.Text); err != nil {
				log.Printf("seal note: %v", err)
				writeError(w, http.StatusInternalServerError, "could not encrypt note")
				return
			}
		}
		// The old wrapped key is overwritten, which shreds the previous note.
		if _, err := db.Exec(
			`UPDATE filesets SET text_blob = ?, text_key = ?, text_content = '' WHERE id = ?`,
			blob, key, t.id); err != nil {
			writeError(w, http.StatusInternalServerError, "db error")
			return
		}
		detail := "note updated"
		if *body.Text == "" {
			detail = "note removed"
		}
		recordEvent(t.id, eventNoteEdited, detail, ip, ua)
	}

	if newExpiry != nil && !(t.NeverExpires && isPermanent(*newExpiry)) {
		bump := 0
		if newExpiry.After(t.expiresAt) {
			bump = 1
		}
		if _, err := db.Exec(
			`UPDATE filesets SET expires_at = ?, extend_count = extend_count + ? WHERE id = ?`,
			*newExpiry, bump, t.id); err != nil {
			writeError(w, http.StatusInternalServerError, "db error")
			return
		}
		detail := fmt.Sprintf("%s → %s", describeExpiry(t.expiresAt), describeExpiry(*newExpiry))
		if clamped {
			detail += " (clamped to the server maximum)"
		}
		recordEvent(t.id, eventExpiryChanged, detail, ip, ua)
	}

	if clamped {
		w.Header().Set("X-Expiry-Clamped", "true")
	}
	writeDetail(w, r, t.Pin, http.StatusOK)
}

// POST /api/manage/transfers/{pin}/rotate-pin issues a fresh PIN and revokes
// the old one, so a leaked share link stops working without re-uploading.
func handleManageRotatePIN(w http.ResponseWriter, r *http.Request) {
	t, ok := lookupManaged(w, r)
	if !ok {
		return
	}

	tx, err := db.Begin()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db error")
		return
	}
	defer tx.Rollback()

	newPin := ""
	for attempt := 0; attempt < 100 && newPin == ""; attempt++ {
		candidate, err := generatePIN(pinLength)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "could not generate PIN")
			return
		}
		res, err := tx.Exec(
			`UPDATE filesets SET pin = ? WHERE id = ? AND NOT EXISTS (SELECT 1 FROM filesets WHERE pin = ?)`,
			candidate, t.id, candidate)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "db error")
			return
		}
		if n, _ := res.RowsAffected(); n == 1 {
			newPin = candidate
		}
	}
	if newPin == "" {
		writeError(w, http.StatusServiceUnavailable, "could not find a free PIN; raise PIN_LENGTH")
		return
	}

	oldDir, newDir := transferDir(t.Pin), transferDir(newPin)
	moved := true
	if err := os.Rename(oldDir, newDir); err != nil {
		if !os.IsNotExist(err) {
			log.Printf("rotate PIN: move dir: %v", err)
			writeError(w, http.StatusInternalServerError, "could not move transfer files")
			return
		}
		moved = false // text-only transfers from old versions have no directory
	}
	if err := tx.Commit(); err != nil {
		if moved {
			os.Rename(newDir, oldDir)
		}
		writeError(w, http.StatusInternalServerError, "db error")
		return
	}

	recordEvent(t.id, eventPINRotated, "new PIN issued; the previous PIN and share link no longer work",
		clientIP(r), r.UserAgent())
	writeDetail(w, r, newPin, http.StatusOK)
}

func managedFile(w http.ResponseWriter, r *http.Request, t manageTransfer) (storedFile, bool) {
	id, err := strconv.ParseInt(chi.URLParam(r, "fileID"), 10, 64)
	if err != nil {
		writeError(w, http.StatusNotFound, "file not found")
		return storedFile{}, false
	}
	var sf storedFile
	err = db.QueryRow(
		`SELECT id, stored_name, original_name, size, wrapped_key FROM files WHERE id = ? AND fileset_id = ?`,
		id, t.id).Scan(&sf.id, &sf.storedName, &sf.displayName, &sf.size, &sf.wrappedKey)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "file not found")
		return sf, false
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db error")
		return sf, false
	}
	return sf, true
}

// GET /api/manage/transfers/{pin}/files/{fileID}
func handleManageDownloadFile(w http.ResponseWriter, r *http.Request) {
	t, ok := lookupManaged(w, r)
	if !ok {
		return
	}
	sf, ok := managedFile(w, r, t)
	if !ok {
		return
	}
	serveFile(w, r, t.Pin, t.id, sf, true)
}

// GET /api/manage/transfers/{pin}/zip
func handleManageZip(w http.ResponseWriter, r *http.Request) {
	t, ok := lookupManaged(w, r)
	if !ok {
		return
	}
	entries, err := listStoredFiles(t.id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db error")
		return
	}
	if len(entries) == 0 {
		writeError(w, http.StatusNotFound, "this transfer has no files")
		return
	}
	serveZip(w, r, t.Pin, t.id, entries, true)
}

// removeTransfer deletes a transfer's files and its row, which destroys the keys.
func removeTransfer(id int64, pin string) error {
	os.RemoveAll(transferDir(pin))
	_, err := db.Exec(`DELETE FROM filesets WHERE id = ?`, id)
	return err
}

// DELETE /api/manage/transfers/{pin}/files/{fileID}
//
// Deleting the last file of a transfer with no note deletes the transfer too,
// since an empty transfer is nothing a recipient could use.
func handleManageDeleteFile(w http.ResponseWriter, r *http.Request) {
	t, ok := lookupManaged(w, r)
	if !ok {
		return
	}
	sf, ok := managedFile(w, r, t)
	if !ok {
		return
	}

	if _, err := db.Exec(`DELETE FROM files WHERE id = ?`, sf.id); err != nil {
		writeError(w, http.StatusInternalServerError, "db error")
		return
	}
	if err := os.Remove(filepath.Join(transferDir(t.Pin), sf.storedName)); err != nil && !os.IsNotExist(err) {
		log.Printf("delete file from disk: %v", err)
	}

	var remaining int
	db.QueryRow(`SELECT COUNT(*) FROM files WHERE fileset_id = ?`, t.id).Scan(&remaining)
	if remaining == 0 && !t.HasText {
		if err := removeTransfer(t.id, t.Pin); err != nil {
			writeError(w, http.StatusInternalServerError, "db error")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"transfer_deleted": true})
		return
	}

	recordEvent(t.id, eventFileDeleted, sf.displayName, clientIP(r), r.UserAgent())
	writeDetail(w, r, t.Pin, http.StatusOK)
}

// dedupeName keeps display names unique within a transfer, so the public
// by-name download URL always resolves to the file the owner expects.
func dedupeName(name string, taken map[string]bool) string {
	if !taken[name] {
		return name
	}
	ext := filepath.Ext(name)
	stem := strings.TrimSuffix(name, ext)
	for i := 2; ; i++ {
		candidate := fmt.Sprintf("%s (%d)%s", stem, i, ext)
		if !taken[candidate] {
			return candidate
		}
	}
}

// POST /api/manage/transfers/{pin}/files (multipart, field "files")
func handleManageAddFiles(w http.ResponseWriter, r *http.Request) {
	t, ok := lookupManaged(w, r)
	if !ok {
		return
	}
	if maxUploadBytes > 0 {
		r.Body = http.MaxBytesReader(w, r.Body, maxUploadBytes)
	}
	mediaType, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || !strings.HasPrefix(mediaType, "multipart/") || params["boundary"] == "" {
		writeError(w, http.StatusBadRequest, "expected multipart/form-data with one or more \"files\" parts")
		return
	}

	staging, err := stageDir()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not create storage directory")
		return
	}
	defer os.RemoveAll(staging)

	pending, _, ok := readMultipartInto(w, multipart.NewReader(r.Body, params["boundary"]), staging)
	if !ok {
		return
	}
	if len(pending) == 0 {
		writeError(w, http.StatusBadRequest, "no files provided")
		return
	}

	// The upload may have taken a while; make sure the transfer is still live.
	var stillLive int
	db.QueryRow(`SELECT COUNT(*) FROM filesets WHERE id = ? AND expires_at > datetime('now')`, t.id).Scan(&stillLive)
	if stillLive == 0 {
		writeError(w, http.StatusGone, "transfer expired or was deleted during the upload")
		return
	}

	taken := map[string]bool{}
	if rows, err := db.Query(`SELECT original_name FROM files WHERE fileset_id = ?`, t.id); err == nil {
		for rows.Next() {
			var n string
			if rows.Scan(&n) == nil {
				taken[n] = true
			}
		}
		rows.Close()
	}

	dest := transferDir(t.Pin)
	if err := os.MkdirAll(dest, 0o700); err != nil {
		writeError(w, http.StatusInternalServerError, "could not create storage directory")
		return
	}

	tx, err := db.Begin()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db error")
		return
	}
	defer tx.Rollback()

	var placed []string
	cleanup := func() {
		for _, p := range placed {
			os.Remove(p)
		}
	}
	now := time.Now().UTC()
	for _, f := range pending {
		destPath, storedName := uniqueDestPath(dest, f.storedName)
		if err := os.Rename(filepath.Join(staging, f.storedName), destPath); err != nil {
			cleanup()
			writeError(w, http.StatusInternalServerError, "could not move file into place")
			return
		}
		placed = append(placed, destPath)
		name := dedupeName(f.displayName, taken)
		taken[name] = true
		if _, err := tx.Exec(
			`INSERT INTO files (fileset_id, original_name, stored_name, size, wrapped_key, added_at)
			 VALUES (?, ?, ?, ?, ?, ?)`,
			t.id, name, storedName, f.size, f.wrappedKey, now); err != nil {
			cleanup()
			writeError(w, http.StatusInternalServerError, "db error")
			return
		}
	}
	if err := tx.Commit(); err != nil {
		cleanup()
		writeError(w, http.StatusInternalServerError, "db error")
		return
	}

	recordEvent(t.id, eventFilesAdded, fmt.Sprintf("%d file(s) added", len(pending)), clientIP(r), r.UserAgent())
	writeDetail(w, r, t.Pin, http.StatusCreated)
}
