package main

import (
	"archive/zip"
	"crypto/subtle"
	"database/sql"
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log"
	"math/rand"
	"mime"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	_ "modernc.org/sqlite"
)

//go:embed static/*
var staticFiles embed.FS

var db *sql.DB
var dataDir string
var apiKey string

type FileInfo struct {
	Name        string `json:"name"`
	Size        int64  `json:"size"`
	DownloadURL string `json:"download_url,omitempty"`
}

type TransferResponse struct {
	Pin       string     `json:"pin"`
	ExpiresAt time.Time  `json:"expires_at"`
	Files     []FileInfo `json:"files"`
	Text      string     `json:"text,omitempty"`
	ShareURL  string     `json:"share_url"`
	ZipURL    string     `json:"zip_url,omitempty"`
	Message   string     `json:"message"`
}

// baseURL reconstructs the public-facing origin from proxy-aware headers,
// since the app typically runs behind a TLS reverse proxy.
func baseURL(r *http.Request) string {
	scheme := "http"
	if proto := r.Header.Get("X-Forwarded-Proto"); proto != "" {
		scheme = proto
	} else if r.TLS != nil {
		scheme = "https"
	}
	host := r.Header.Get("X-Forwarded-Host")
	if host == "" {
		host = r.Host
	}
	return scheme + "://" + host
}

// buildResponse fills in the self-describing URLs and message so callers
// (including AI agents) can relay results without constructing URLs themselves.
func buildResponse(r *http.Request, pin string, expiresAt time.Time, files []FileInfo, text string) TransferResponse {
	base := baseURL(r)
	enriched := make([]FileInfo, len(files))
	for i, f := range files {
		f.DownloadURL = fmt.Sprintf("%s/api/transfer/%s/download/%s", base, pin, url.PathEscape(f.Name))
		enriched[i] = f
	}
	zipURL := ""
	if len(files) > 0 {
		zipURL = fmt.Sprintf("%s/api/transfer/%s/zip", base, pin)
	}
	return TransferResponse{
		Pin:       pin,
		ExpiresAt: expiresAt,
		Files:     enriched,
		Text:      text,
		ShareURL:  fmt.Sprintf("%s/?pin=%s", base, pin),
		ZipURL:    zipURL,
		Message:   fmt.Sprintf("Share PIN %s or the link below to let someone download these files. Expires %s.", pin, expiresAt.Format(time.RFC3339)),
	}
}

var unsafeChars = regexp.MustCompile(`[^a-zA-Z0-9._\-]`)

func sanitizeFilename(name string) string {
	name = filepath.Base(name)
	name = unsafeChars.ReplaceAllString(name, "_")
	if name == "" || name == "." {
		name = "file"
	}
	return name
}

func generatePIN() string {
	return fmt.Sprintf("%04d", rand.Intn(10000))
}

func getUniquePIN() (string, error) {
	for i := 0; i < 100; i++ {
		pin := generatePIN()
		var count int
		err := db.QueryRow(`SELECT COUNT(*) FROM filesets WHERE pin = ?`, pin).Scan(&count)
		if err != nil {
			return "", err
		}
		if count == 0 {
			return pin, nil
		}
	}
	return "", fmt.Errorf("could not generate unique PIN after 100 attempts")
}

func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-API-Key")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// requireAPIKey gates write routes when API_KEY is set. When unset the API
// stays fully open (backwards-compatible). Accepts the key via either the
// X-API-Key header or an "Authorization: Bearer <key>" header.
func requireAPIKey(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if apiKey == "" {
			next.ServeHTTP(w, r)
			return
		}
		provided := r.Header.Get("X-API-Key")
		if provided == "" {
			provided = strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		}
		if subtle.ConstantTimeCompare([]byte(provided), []byte(apiKey)) != 1 {
			writeError(w, http.StatusUnauthorized, "missing or invalid API key: send header 'X-API-Key: <your key>'")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// POST /api/transfer?expiry=<seconds>
func handleUpload(w http.ResponseWriter, r *http.Request) {
	expirySecs := parseExpiry(r)

	contentType := r.Header.Get("Content-Type")
	mediaType, params, err := mime.ParseMediaType(contentType)
	if err != nil || !strings.HasPrefix(mediaType, "multipart/") {
		writeError(w, http.StatusBadRequest, "expected multipart/form-data")
		return
	}
	boundary := params["boundary"]
	if boundary == "" {
		writeError(w, http.StatusBadRequest, "missing multipart boundary")
		return
	}

	pin, err := getUniquePIN()
	if err != nil {
		log.Printf("PIN generation error: %v", err)
		writeError(w, http.StatusInternalServerError, "could not generate PIN")
		return
	}

	now := time.Now().UTC()
	expiresAt := now.Add(time.Duration(expirySecs) * time.Second)

	tx, err := db.Begin()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db error")
		return
	}
	defer tx.Rollback()

	res, err := tx.Exec(
		`INSERT INTO filesets (pin, created_at, expires_at) VALUES (?, ?, ?)`,
		pin, now, expiresAt,
	)
	if err != nil {
		log.Printf("insert fileset error: %v", err)
		writeError(w, http.StatusInternalServerError, "db error")
		return
	}
	filesetID, _ := res.LastInsertId()

	dir := filepath.Join(dataDir, "files", pin)
	if err := os.MkdirAll(dir, 0755); err != nil {
		writeError(w, http.StatusInternalServerError, "could not create storage directory")
		return
	}

	mr := multipart.NewReader(r.Body, boundary)
	savedFiles := []FileInfo{}
	var textContent string

	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			log.Printf("multipart error: %v", err)
			writeError(w, http.StatusBadRequest, "multipart read error")
			return
		}

		// Check for text field
		if part.FormName() == "text" && part.FileName() == "" {
			buf, err := io.ReadAll(part)
			part.Close()
			if err != nil {
				writeError(w, http.StatusBadRequest, "could not read text field")
				return
			}
			textContent = string(buf)
			continue
		}

		originalName := part.FileName()
		if originalName == "" {
			part.Close()
			continue
		}

		sanitized := sanitizeFilename(originalName)
		// Ensure uniqueness if multiple files have same sanitized name
		destPath := filepath.Join(dir, sanitized)
		base := sanitized
		ext := filepath.Ext(sanitized)
		stem := strings.TrimSuffix(base, ext)
		for counter := 1; ; counter++ {
			if _, err := os.Stat(destPath); os.IsNotExist(err) {
				break
			}
			sanitized = fmt.Sprintf("%s_%d%s", stem, counter, ext)
			destPath = filepath.Join(dir, sanitized)
		}

		f, err := os.Create(destPath)
		if err != nil {
			part.Close()
			log.Printf("create file error: %v", err)
			writeError(w, http.StatusInternalServerError, "could not save file")
			return
		}

		written, err := io.Copy(f, part)
		f.Close()
		part.Close()
		if err != nil {
			log.Printf("write file error: %v", err)
			writeError(w, http.StatusInternalServerError, "could not write file")
			return
		}

		_, err = tx.Exec(
			`INSERT INTO files (fileset_id, original_name, stored_name, size) VALUES (?, ?, ?, ?)`,
			filesetID, originalName, sanitized, written,
		)
		if err != nil {
			log.Printf("insert file error: %v", err)
			writeError(w, http.StatusInternalServerError, "db error")
			return
		}

		savedFiles = append(savedFiles, FileInfo{Name: originalName, Size: written})
	}

	if len(savedFiles) == 0 && textContent == "" {
		writeError(w, http.StatusBadRequest, "no files or text provided")
		return
	}

	// Save text content to the fileset
	if textContent != "" {
		_, err = tx.Exec(`UPDATE filesets SET text_content = ? WHERE id = ?`, textContent, filesetID)
		if err != nil {
			log.Printf("update text_content error: %v", err)
			writeError(w, http.StatusInternalServerError, "db error")
			return
		}
	}

	if err := tx.Commit(); err != nil {
		writeError(w, http.StatusInternalServerError, "db commit error")
		return
	}

	writeJSON(w, http.StatusCreated, buildResponse(r, pin, expiresAt, savedFiles, textContent))
}

// parseExpiry reads the ?expiry=<seconds> query param, defaulting to 1h.
func parseExpiry(r *http.Request) int64 {
	expirySecs := int64(3600) // default 1h
	if expiryStr := r.URL.Query().Get("expiry"); expiryStr != "" {
		if v, err := strconv.ParseInt(expiryStr, 10, 64); err == nil && v > 0 {
			expirySecs = v
		}
	}
	return expirySecs
}

// PUT|POST /api/upload/:filename
// Foolproof, no-multipart upload: the raw request body is the file contents.
//
//	curl -T ./android-sdk.zip https://host/api/upload/android-sdk.zip
func handleRawUpload(w http.ResponseWriter, r *http.Request) {
	originalName := chi.URLParam(r, "filename")
	if originalName == "" {
		writeError(w, http.StatusBadRequest, "missing filename in path: use /api/upload/<filename>")
		return
	}
	if r.Body == nil {
		writeError(w, http.StatusBadRequest, "empty request body")
		return
	}

	expiresAt := time.Now().UTC().Add(time.Duration(parseExpiry(r)) * time.Second)

	pin, err := getUniquePIN()
	if err != nil {
		log.Printf("PIN generation error: %v", err)
		writeError(w, http.StatusInternalServerError, "could not generate PIN")
		return
	}

	tx, err := db.Begin()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db error")
		return
	}
	defer tx.Rollback()

	res, err := tx.Exec(
		`INSERT INTO filesets (pin, created_at, expires_at) VALUES (?, ?, ?)`,
		pin, time.Now().UTC(), expiresAt,
	)
	if err != nil {
		log.Printf("insert fileset error: %v", err)
		writeError(w, http.StatusInternalServerError, "db error")
		return
	}
	filesetID, _ := res.LastInsertId()

	dir := filepath.Join(dataDir, "files", pin)
	if err := os.MkdirAll(dir, 0755); err != nil {
		writeError(w, http.StatusInternalServerError, "could not create storage directory")
		return
	}

	stored := sanitizeFilename(originalName)
	f, err := os.Create(filepath.Join(dir, stored))
	if err != nil {
		log.Printf("create file error: %v", err)
		writeError(w, http.StatusInternalServerError, "could not save file")
		return
	}
	written, err := io.Copy(f, r.Body)
	f.Close()
	if err != nil {
		log.Printf("write file error: %v", err)
		writeError(w, http.StatusInternalServerError, "could not write file")
		return
	}
	if written == 0 {
		writeError(w, http.StatusBadRequest, "empty request body: send the file as the raw body, e.g. curl -T FILE")
		return
	}

	if _, err := tx.Exec(
		`INSERT INTO files (fileset_id, original_name, stored_name, size) VALUES (?, ?, ?, ?)`,
		filesetID, originalName, stored, written,
	); err != nil {
		log.Printf("insert file error: %v", err)
		writeError(w, http.StatusInternalServerError, "db error")
		return
	}

	if err := tx.Commit(); err != nil {
		writeError(w, http.StatusInternalServerError, "db commit error")
		return
	}

	writeJSON(w, http.StatusCreated, buildResponse(r, pin, expiresAt, []FileInfo{{Name: originalName, Size: written}}, ""))
}

// GET /api/transfer/:pin
func handleGetTransfer(w http.ResponseWriter, r *http.Request) {
	pin := chi.URLParam(r, "pin")

	var filesetID int64
	var expiresAt time.Time
	var textContent string
	err := db.QueryRow(
		`SELECT id, expires_at, text_content FROM filesets WHERE pin = ? AND expires_at > datetime('now')`,
		pin,
	).Scan(&filesetID, &expiresAt, &textContent)
	if err == sql.ErrNoRows {
		writeError(w, http.StatusNotFound, "transfer not found or expired")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db error")
		return
	}

	rows, err := db.Query(`SELECT original_name, size FROM files WHERE fileset_id = ?`, filesetID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db error")
		return
	}
	defer rows.Close()

	files := []FileInfo{}
	for rows.Next() {
		var f FileInfo
		if err := rows.Scan(&f.Name, &f.Size); err != nil {
			continue
		}
		files = append(files, f)
	}

	writeJSON(w, http.StatusOK, buildResponse(r, pin, expiresAt, files, textContent))
}

// GET /api/transfer/:pin/download/:filename
func handleDownloadFile(w http.ResponseWriter, r *http.Request) {
	pin := chi.URLParam(r, "pin")
	filename := chi.URLParam(r, "filename")

	var filesetID int64
	err := db.QueryRow(
		`SELECT id FROM filesets WHERE pin = ? AND expires_at > datetime('now')`,
		pin,
	).Scan(&filesetID)
	if err == sql.ErrNoRows {
		writeError(w, http.StatusNotFound, "transfer not found or expired")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db error")
		return
	}

	var storedName string
	var originalName string
	err = db.QueryRow(
		`SELECT stored_name, original_name FROM files WHERE fileset_id = ? AND original_name = ?`,
		filesetID, filename,
	).Scan(&storedName, &originalName)
	if err == sql.ErrNoRows {
		writeError(w, http.StatusNotFound, "file not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db error")
		return
	}

	filePath := filepath.Join(dataDir, "files", pin, storedName)
	f, err := os.Open(filePath)
	if err != nil {
		writeError(w, http.StatusNotFound, "file not found on disk")
		return
	}
	defer f.Close()

	stat, err := f.Stat()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not stat file")
		return
	}

	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, strings.ReplaceAll(originalName, `"`, `\"`)))
	w.Header().Set("Content-Length", strconv.FormatInt(stat.Size(), 10))
	w.Header().Set("Content-Type", "application/octet-stream")
	io.Copy(w, f)
}

// GET /api/transfer/:pin/zip
func handleDownloadZip(w http.ResponseWriter, r *http.Request) {
	pin := chi.URLParam(r, "pin")

	var filesetID int64
	err := db.QueryRow(
		`SELECT id FROM filesets WHERE pin = ? AND expires_at > datetime('now')`,
		pin,
	).Scan(&filesetID)
	if err == sql.ErrNoRows {
		writeError(w, http.StatusNotFound, "transfer not found or expired")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db error")
		return
	}

	rows, err := db.Query(
		`SELECT original_name, stored_name FROM files WHERE fileset_id = ?`,
		filesetID,
	)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db error")
		return
	}
	defer rows.Close()

	type fileEntry struct {
		originalName string
		storedName   string
	}
	var entries []fileEntry
	for rows.Next() {
		var e fileEntry
		rows.Scan(&e.originalName, &e.storedName)
		entries = append(entries, e)
	}

	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="shadowfax-%s.zip"`, pin))

	zw := zip.NewWriter(w)
	defer zw.Close()

	for _, entry := range entries {
		filePath := filepath.Join(dataDir, "files", pin, entry.storedName)
		f, err := os.Open(filePath)
		if err != nil {
			continue
		}

		fw, err := zw.Create(entry.originalName)
		if err != nil {
			f.Close()
			continue
		}
		io.Copy(fw, f)
		f.Close()
	}
}

// DELETE /api/transfer/:pin
func handleDeleteTransfer(w http.ResponseWriter, r *http.Request) {
	pin := chi.URLParam(r, "pin")

	var filesetID int64
	err := db.QueryRow(`SELECT id FROM filesets WHERE pin = ?`, pin).Scan(&filesetID)
	if err == sql.ErrNoRows {
		writeError(w, http.StatusNotFound, "transfer not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db error")
		return
	}

	dir := filepath.Join(dataDir, "files", pin)
	os.RemoveAll(dir)

	_, err = db.Exec(`DELETE FROM filesets WHERE id = ?`, filesetID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db error")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func startCleanupRoutine() {
	ticker := time.NewTicker(time.Minute)
	go func() {
		for range ticker.C {
			cleanupExpired()
		}
	}()
}

func cleanupExpired() {
	rows, err := db.Query(`SELECT id, pin FROM filesets WHERE expires_at <= datetime('now')`)
	if err != nil {
		log.Printf("cleanup query error: %v", err)
		return
	}

	type expiredEntry struct {
		id  int64
		pin string
	}
	var expired []expiredEntry
	for rows.Next() {
		var e expiredEntry
		rows.Scan(&e.id, &e.pin)
		expired = append(expired, e)
	}
	rows.Close()

	for _, e := range expired {
		dir := filepath.Join(dataDir, "files", e.pin)
		if err := os.RemoveAll(dir); err != nil {
			log.Printf("cleanup remove dir error for pin %s: %v", e.pin, err)
		}
		if _, err := db.Exec(`DELETE FROM filesets WHERE id = ?`, e.id); err != nil {
			log.Printf("cleanup db delete error for id %d: %v", e.id, err)
		}
	}

	if len(expired) > 0 {
		log.Printf("cleaned up %d expired transfer(s)", len(expired))
	}
}

func initDB(dbPath string) error {
	var err error
	db, err = sql.Open("sqlite", dbPath)
	if err != nil {
		return fmt.Errorf("open db: %w", err)
	}

	db.SetMaxOpenConns(1)

	pragmas := []string{
		`PRAGMA journal_mode=WAL`,
		`PRAGMA foreign_keys=ON`,
	}
	for _, p := range pragmas {
		if _, err := db.Exec(p); err != nil {
			return fmt.Errorf("pragma %q: %w", p, err)
		}
	}

	schema := `
CREATE TABLE IF NOT EXISTS filesets (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  pin TEXT UNIQUE NOT NULL,
  created_at DATETIME NOT NULL,
  expires_at DATETIME NOT NULL,
  text_content TEXT NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS files (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  fileset_id INTEGER NOT NULL,
  original_name TEXT NOT NULL,
  stored_name TEXT NOT NULL,
  size INTEGER NOT NULL,
  FOREIGN KEY (fileset_id) REFERENCES filesets(id) ON DELETE CASCADE
);
`
	if _, err := db.Exec(schema); err != nil {
		return fmt.Errorf("create schema: %w", err)
	}

	// Migration: add text_content column if missing (for existing databases)
	db.Exec(`ALTER TABLE filesets ADD COLUMN text_content TEXT NOT NULL DEFAULT ''`)

	return nil
}

// isBrowser is a heuristic: every major browser sends a User-Agent containing
// "Mozilla", while curl/wget/python-requests/Go-http-client and most AI
// fetchers do not. Used to serve docs to bots and the SPA to humans.
func isBrowser(r *http.Request) bool {
	return strings.Contains(r.Header.Get("User-Agent"), "Mozilla")
}

// renderDocs returns plain-text/markdown API docs with the real host baked in,
// so an AI agent can copy-paste a curl command verbatim.
func renderDocs(r *http.Request) string {
	base := baseURL(r)
	keyMultipart := ""
	keyRaw := ""
	keyNote := "No API key is required."
	if apiKey != "" {
		keyMultipart = ` -H "X-API-Key: <your key>"`
		keyRaw = ` -H "X-API-Key: <your key>"`
		keyNote = `This server requires an API key. Add the header  -H "X-API-Key: <your key>"  to every upload/delete request (ask the operator for the key).`
	}

	return fmt.Sprintf(`# Shadowfax — file transfer API (for humans and robots)

Upload a file and you get back a 4-digit PIN plus a share URL. Give the PIN or
the share_url to whoever (or whatever) needs the file; it expires automatically.

Base URL: %[1]s
Auth: %[2]s

## Upload (simplest — raw body, recommended for scripts/agents)
Send the file as the raw request body. The filename goes in the URL path:

    curl -T ./android-sdk.zip%[3]s %[1]s/api/upload/android-sdk.zip

## Upload (multipart — supports multiple files and a text note)

    curl -F "files=@./android-sdk.zip"%[4]s %[1]s/api/transfer?expiry=3600

Add more "-F files=@..." for multiple files, and "-F text=..." for a note.

## Optional: how long it lives
Append ?expiry=<seconds> to either upload URL. Default 3600 (1h).
Common values: 3600 (1h), 21600 (6h), 86400 (1d), 259200 (3d), 604800 (7d).

## What you get back (HTTP 201, JSON)
    {
      "pin": "1234",
      "expires_at": "2026-06-13T22:45:00Z",
      "share_url": "%[1]s/?pin=1234",
      "files": [{"name": "android-sdk.zip", "size": 1234,
                 "download_url": "%[1]s/api/transfer/1234/download/android-sdk.zip"}],
      "zip_url": "%[1]s/api/transfer/1234/zip",
      "message": "Share PIN 1234 or the link below ..."
    }

>> Relay the "pin" and "share_url" back to the user — that is how they download the file.

## Download
    GET %[1]s/api/transfer/<pin>                      -> JSON metadata
    GET %[1]s/api/transfer/<pin>/download/<filename>  -> the file
    GET %[1]s/api/transfer/<pin>/zip                  -> all files as a zip
`, base, keyNote, keyRaw, keyMultipart)
}

// handleDocs serves the API docs. GET /api returns a structured JSON listing
// when the client prefers JSON; otherwise (and for /llms.txt) it returns text.
func handleDocs(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/api" && strings.Contains(r.Header.Get("Accept"), "application/json") {
		writeJSON(w, http.StatusOK, map[string]any{
			"service":         "shadowfax",
			"base_url":        baseURL(r),
			"api_key_required": apiKey != "",
			"endpoints": []map[string]string{
				{"method": "PUT", "path": "/api/upload/{filename}", "body": "raw file bytes", "desc": "simplest upload; returns pin + share_url"},
				{"method": "POST", "path": "/api/transfer", "body": "multipart/form-data (files, text)", "desc": "multi-file upload; returns pin + share_url"},
				{"method": "GET", "path": "/api/transfer/{pin}", "desc": "transfer metadata"},
				{"method": "GET", "path": "/api/transfer/{pin}/download/{filename}", "desc": "download one file"},
				{"method": "GET", "path": "/api/transfer/{pin}/zip", "desc": "download all files as zip"},
			},
			"docs": baseURL(r) + "/llms.txt",
		})
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	io.WriteString(w, renderDocs(r))
}

func spaHandler(staticFS fs.FS) http.HandlerFunc {
	fileServer := http.FileServer(http.FS(staticFS))
	return func(w http.ResponseWriter, r *http.Request) {
		// Serve machine-readable docs to non-browser clients hitting the root,
		// so a bot/agent discovers the API instead of an opaque JS bundle.
		if r.URL.Path == "/" && !isBrowser(r) {
			handleDocs(w, r)
			return
		}

		// Try to serve the file directly
		urlPath := r.URL.Path
		if urlPath == "/" {
			urlPath = "/index.html"
		}

		f, err := staticFS.Open(strings.TrimPrefix(urlPath, "/"))
		if err == nil {
			f.Close()
			fileServer.ServeHTTP(w, r)
			return
		}

		// Fall back to index.html for SPA routing
		index, err := staticFS.Open("index.html")
		if err != nil {
			http.Error(w, "index.html not found", http.StatusInternalServerError)
			return
		}
		defer index.Close()

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		io.Copy(w, index)
	}
}

func main() {
	dataDir = os.Getenv("DATA_DIR")
	if dataDir == "" {
		dataDir = "./data"
	}

	apiKey = os.Getenv("API_KEY")

	if err := os.MkdirAll(filepath.Join(dataDir, "files"), 0755); err != nil {
		log.Fatalf("create data dir: %v", err)
	}

	dbPath := filepath.Join(dataDir, "shadowfax.db")
	if err := initDB(dbPath); err != nil {
		log.Fatalf("init db: %v", err)
	}
	defer db.Close()

	startCleanupRoutine()

	subFS, err := fs.Sub(staticFiles, "static")
	if err != nil {
		log.Fatalf("embed sub: %v", err)
	}

	r := chi.NewRouter()
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)
	r.Use(corsMiddleware)

	// Machine-readable API docs for robots / AI agents.
	r.Get("/llms.txt", handleDocs)
	r.Get("/api", handleDocs)

	// Write routes — gated by the optional API key.
	r.Group(func(pr chi.Router) {
		pr.Use(requireAPIKey)
		pr.Post("/api/transfer", handleUpload)
		pr.Put("/api/upload/{filename}", handleRawUpload)
		pr.Post("/api/upload/{filename}", handleRawUpload)
		pr.Delete("/api/transfer/{pin}", handleDeleteTransfer)
	})

	// Read routes — always open so anyone with a PIN can download.
	r.Get("/api/transfer/{pin}", handleGetTransfer)
	r.Get("/api/transfer/{pin}/download/{filename}", handleDownloadFile)
	r.Get("/api/transfer/{pin}/zip", handleDownloadZip)

	// SPA fallback (serves docs to non-browser clients at "/").
	r.Get("/*", spaHandler(subFS))

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	log.Printf("Shadowfax server starting on :%s (data dir: %s)", port, dataDir)
	if err := http.ListenAndServe(":"+port, r); err != nil {
		log.Fatalf("server error: %v", err)
	}
}
