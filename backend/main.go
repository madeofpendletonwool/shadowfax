package main

import (
	"archive/zip"
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

type FileInfo struct {
	Name string `json:"name"`
	Size int64  `json:"size"`
}

type TransferResponse struct {
	Pin       string     `json:"pin"`
	ExpiresAt time.Time  `json:"expires_at"`
	Files     []FileInfo `json:"files,omitempty"`
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
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
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
	expiryStr := r.URL.Query().Get("expiry")
	expirySecs := int64(3600) // default 1h
	if expiryStr != "" {
		if v, err := strconv.ParseInt(expiryStr, 10, 64); err == nil && v > 0 {
			expirySecs = v
		}
	}

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
	var savedFiles []FileInfo

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

	if len(savedFiles) == 0 {
		writeError(w, http.StatusBadRequest, "no files uploaded")
		return
	}

	if err := tx.Commit(); err != nil {
		writeError(w, http.StatusInternalServerError, "db commit error")
		return
	}

	writeJSON(w, http.StatusCreated, TransferResponse{
		Pin:       pin,
		ExpiresAt: expiresAt,
		Files:     savedFiles,
	})
}

// GET /api/transfer/:pin
func handleGetTransfer(w http.ResponseWriter, r *http.Request) {
	pin := chi.URLParam(r, "pin")

	var filesetID int64
	var expiresAt time.Time
	err := db.QueryRow(
		`SELECT id, expires_at FROM filesets WHERE pin = ? AND expires_at > datetime('now')`,
		pin,
	).Scan(&filesetID, &expiresAt)
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

	var files []FileInfo
	for rows.Next() {
		var f FileInfo
		if err := rows.Scan(&f.Name, &f.Size); err != nil {
			continue
		}
		files = append(files, f)
	}

	writeJSON(w, http.StatusOK, TransferResponse{
		Pin:       pin,
		ExpiresAt: expiresAt,
		Files:     files,
	})
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
  expires_at DATETIME NOT NULL
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
	return nil
}

func spaHandler(staticFS fs.FS) http.HandlerFunc {
	fileServer := http.FileServer(http.FS(staticFS))
	return func(w http.ResponseWriter, r *http.Request) {
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

	r.Post("/api/transfer", handleUpload)
	r.Get("/api/transfer/{pin}", handleGetTransfer)
	r.Get("/api/transfer/{pin}/download/{filename}", handleDownloadFile)
	r.Get("/api/transfer/{pin}/zip", handleDownloadZip)
	r.Delete("/api/transfer/{pin}", handleDeleteTransfer)

	// SPA fallback
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
