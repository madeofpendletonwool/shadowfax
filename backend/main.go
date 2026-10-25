package main

import (
	"archive/zip"
	"crypto/rand"
	"database/sql"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"math/big"
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

var (
	db        *sql.DB
	dataDir   string
	apiKey    string
	masterKey []byte

	pinLength      int
	trustProxy     bool
	maxUploadBytes int64
	maxExpirySecs  int64
	allowedOrigins []string

	lookupLimiter   *rateLimiter
	downloadLimiter *rateLimiter
	uploadLimiter   *rateLimiter
)

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
	Encrypted bool       `json:"encrypted"`
	Message   string     `json:"message"`
}

// ---------------------------------------------------------------- environment

func envString(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

func envInt(key string, fallback, min, max int) int {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return fallback
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < min || n > max {
		log.Printf("WARNING: %s=%q is not an integer in [%d,%d]; using %d", key, v, min, max, fallback)
		return fallback
	}
	return n
}

func envInt64(key string, fallback int64) int64 {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return fallback
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n < 0 {
		log.Printf("WARNING: %s=%q is not a non-negative integer; using %d", key, v, fallback)
		return fallback
	}
	return n
}

func envBool(key string, fallback bool) bool {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return fallback
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		log.Printf("WARNING: %s=%q is not a boolean; using %t", key, v, fallback)
		return fallback
	}
	return b
}

// ---------------------------------------------------------------- URL helpers

// baseURL reconstructs the public-facing origin. Forwarded headers are only
// honoured behind a trusted proxy; otherwise a client could forge the host and
// have us hand a victim a share link pointing at an attacker's domain.
func baseURL(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	host := r.Host

	if trustProxy {
		if proto := r.Header.Get("X-Forwarded-Proto"); proto != "" {
			if first, _, found := strings.Cut(proto, ","); found {
				proto = first
			}
			scheme = strings.TrimSpace(proto)
		}
		if fwdHost := r.Header.Get("X-Forwarded-Host"); fwdHost != "" {
			if first, _, found := strings.Cut(fwdHost, ","); found {
				fwdHost = first
			}
			host = strings.TrimSpace(fwdHost)
		}
	}
	return scheme + "://" + host
}

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
		Encrypted: true,
		Message: fmt.Sprintf("Share PIN %s or the link below to let someone download these files. Expires %s.",
			pin, expiresAt.Format(time.RFC3339)),
	}
}

// ----------------------------------------------------------- filename helpers

var unsafeChars = regexp.MustCompile(`[^a-zA-Z0-9._\-]`)

// sanitizeStoredName produces the on-disk name: conservative ASCII only.
func sanitizeStoredName(name string) string {
	name = filepath.Base(name)
	name = unsafeChars.ReplaceAllString(name, "_")
	name = strings.TrimLeft(name, ".")
	if name == "" {
		name = "file"
	}
	if len(name) > 120 {
		name = name[:120]
	}
	return name
}

// sanitizeDisplayName keeps the name the user sees (unicode and spaces intact)
// but removes anything that could escape a directory, inject a response header,
// or land outside the archive when the recipient unzips it.
func sanitizeDisplayName(name string) string {
	name = strings.ReplaceAll(name, "\\", "/")
	if i := strings.LastIndex(name, "/"); i >= 0 {
		name = name[i+1:]
	}

	var b strings.Builder
	for _, r := range name {
		if r < 0x20 || r == 0x7f {
			continue
		}
		b.WriteRune(r)
	}
	name = strings.TrimSpace(b.String())

	if name == "" || name == "." || name == ".." {
		return "file"
	}
	if len(name) > 200 {
		ext := filepath.Ext(name)
		if len(ext) > 20 {
			ext = ""
		}
		name = name[:200-len(ext)] + ext
	}
	return name
}

// encodeExtValue percent-encodes per RFC 5987 attr-char, for filename*.
func encodeExtValue(s string) string {
	const attrChars = "!#$&+-.^_`|~"
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') ||
			strings.IndexByte(attrChars, c) >= 0 {
			b.WriteByte(c)
			continue
		}
		fmt.Fprintf(&b, "%%%02X", c)
	}
	return b.String()
}

// contentDisposition emits both a plain ASCII filename for old clients and an
// RFC 5987 filename* so unicode names survive the trip.
func contentDisposition(name string) string {
	var ascii strings.Builder
	for _, r := range name {
		switch {
		case r < 0x20 || r == 0x7f:
		case r > 0x7e, r == '"', r == '\\':
			ascii.WriteByte('_')
		default:
			ascii.WriteRune(r)
		}
	}
	fallback := strings.TrimSpace(ascii.String())
	if fallback == "" {
		fallback = "download"
	}
	return fmt.Sprintf(`attachment; filename="%s"; filename*=UTF-8''%s`, fallback, encodeExtValue(name))
}

// fileNameCandidates expands a filename taken from the URL into every form it
// might have been stored as.
//
// chi routes on the raw path whenever the request is not in Go's canonical
// percent-encoding, which is the case for ordinary names containing "(", ")",
// "+", ",", "&", ":", ";", "=", "@" or "$". In that case URLParam returns a
// still-encoded string, and matching it against original_name silently 404s.
// Decoding here is what fixes that.
func fileNameCandidates(raw string) []string {
	var out []string
	seen := make(map[string]bool)
	add := func(s string) {
		if s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}

	add(raw)
	if decoded, err := url.PathUnescape(raw); err == nil {
		add(decoded)
	}
	if strings.Contains(raw, "+") {
		if decoded, err := url.QueryUnescape(raw); err == nil {
			add(decoded)
		}
	}
	for _, name := range append([]string(nil), out...) {
		add(sanitizeDisplayName(name))
		add(sanitizeStoredName(name))
	}
	return out
}

// ------------------------------------------------------------------ responses

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// ----------------------------------------------------------------- PIN issuing

// generatePIN draws from crypto/rand. rand.Int rejects biased samples, so every
// PIN is uniform over the space - unlike math/rand, whose stream is predictable
// from a handful of observed PINs.
func generatePIN(length int) (string, error) {
	max := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(length)), nil)
	n, err := rand.Int(rand.Reader, max)
	if err != nil {
		return "", fmt.Errorf("generate PIN: %w", err)
	}
	return fmt.Sprintf("%0*d", length, n), nil
}

// insertFileset claims a PIN inside the caller's transaction.
//
// Generating the PIN in the same statement that claims it closes the race the
// old SELECT-then-INSERT had, where two concurrent uploads could pick the same
// PIN and the loser died with a 500 on the unique index.
func insertFileset(tx *sql.Tx, now, expiresAt time.Time, textBlob, textKey []byte) (int64, string, error) {
	for attempt := 0; attempt < 100; attempt++ {
		pin, err := generatePIN(pinLength)
		if err != nil {
			return 0, "", err
		}
		res, err := tx.Exec(
			`INSERT INTO filesets (pin, created_at, expires_at, text_blob, text_key)
			 VALUES (?, ?, ?, ?, ?) ON CONFLICT(pin) DO NOTHING`,
			pin, now, expiresAt, textBlob, textKey,
		)
		if err != nil {
			return 0, "", err
		}
		affected, err := res.RowsAffected()
		if err != nil {
			return 0, "", err
		}
		if affected == 0 {
			continue // PIN already in use; draw another
		}
		id, err := res.LastInsertId()
		return id, pin, err
	}
	return 0, "", errors.New("could not find a free PIN; the PIN space may be exhausted (raise PIN_LENGTH)")
}

// ---------------------------------------------------------------- middleware

func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		allowed := ""
		for _, o := range allowedOrigins {
			if o == "*" {
				allowed = "*"
				break
			}
			if origin != "" && strings.EqualFold(o, origin) {
				allowed = origin
				break
			}
		}

		if allowed != "" {
			w.Header().Set("Access-Control-Allow-Origin", allowed)
			if allowed != "*" {
				w.Header().Add("Vary", "Origin")
			}
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-API-Key")
			w.Header().Set("Access-Control-Max-Age", "600")
		}

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Cross-Origin-Resource-Policy", "same-origin")
		// A PIN in a URL is a credential; keep it out of shared caches.
		h.Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

var pinInPath = regexp.MustCompile(`(/api/transfer/)[^/?]+`)

// redactPIN keeps PINs out of the access log. A PIN is the only credential
// guarding a transfer, so logging it verbatim would leak every upload to
// anyone who can read the logs.
func redactPIN(s string) string {
	s = pinInPath.ReplaceAllString(s, "${1}***")
	if i := strings.Index(s, "pin="); i >= 0 {
		s = s[:i] + "pin=***"
	}
	return s
}

func requestLogger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
		next.ServeHTTP(ww, r)
		log.Printf("%s %s %d %dB %s",
			r.Method, redactPIN(r.URL.RequestURI()), ww.Status(), ww.BytesWritten(),
			time.Since(start).Round(time.Millisecond))
	})
}

// ------------------------------------------------------------------- uploads

// parseExpiry reads ?expiry=<seconds>, clamped to MAX_EXPIRY_SECONDS so a
// caller cannot pin data on the server indefinitely.
func parseExpiry(r *http.Request) int64 {
	expirySecs := int64(3600)
	if raw := r.URL.Query().Get("expiry"); raw != "" {
		if v, err := strconv.ParseInt(raw, 10, 64); err == nil && v > 0 {
			expirySecs = v
		}
	}
	if expirySecs > maxExpirySecs {
		expirySecs = maxExpirySecs
	}
	return expirySecs
}

func transferDir(pin string) string { return filepath.Join(dataDir, "files", pin) }

// storeEncrypted writes one uploaded stream to disk, encrypted, and returns the
// plaintext byte count plus the wrapped key needed to read it back.
func storeEncrypted(destPath string, src io.Reader) (int64, []byte, error) {
	dek, err := newDEK()
	if err != nil {
		return 0, nil, err
	}
	wrapped, err := wrapDEK(masterKey, dek)
	if err != nil {
		return 0, nil, err
	}

	f, err := os.OpenFile(destPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC|os.O_EXCL, 0o600)
	if err != nil {
		return 0, nil, err
	}
	written, err := encryptStream(f, src, dek)
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		os.Remove(destPath)
		return 0, nil, err
	}
	return written, wrapped, nil
}

// uniqueDestPath resolves collisions between files whose sanitized names match.
func uniqueDestPath(dir, sanitized string) (string, string) {
	destPath := filepath.Join(dir, sanitized)
	ext := filepath.Ext(sanitized)
	stem := strings.TrimSuffix(sanitized, ext)
	for counter := 1; ; counter++ {
		if _, err := os.Stat(destPath); os.IsNotExist(err) {
			return destPath, sanitized
		}
		sanitized = fmt.Sprintf("%s_%d%s", stem, counter, ext)
		destPath = filepath.Join(dir, sanitized)
	}
}

type pendingFile struct {
	displayName string
	storedName  string
	size        int64
	wrappedKey  []byte
}

// stageDir creates a private staging directory for an in-flight upload.
func stageDir() (string, error) {
	root := filepath.Join(dataDir, "tmp")
	if err := os.MkdirAll(root, 0o700); err != nil {
		return "", err
	}
	return os.MkdirTemp(root, "up-")
}

// commitTransfer records a staged upload and moves it into place.
//
// The transaction opens only once every byte is already on disk. Holding it
// open across the upload itself would pin the single SQLite connection for the
// whole transfer, stalling every other request behind one slow uploader.
func commitTransfer(staging string, files []pendingFile, text string, now, expiresAt time.Time) (string, error) {
	var textBlob, textKey []byte
	if text != "" {
		var err error
		if textBlob, textKey, err = sealText(text); err != nil {
			return "", err
		}
	}

	tx, err := db.Begin()
	if err != nil {
		return "", err
	}
	defer tx.Rollback()

	filesetID, pin, err := insertFileset(tx, now, expiresAt, textBlob, textKey)
	if err != nil {
		return "", err
	}

	dest := transferDir(pin)
	if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
		return "", err
	}
	if err := os.Rename(staging, dest); err != nil {
		return "", fmt.Errorf("move staged upload into place: %w", err)
	}

	for _, f := range files {
		if _, err := tx.Exec(
			`INSERT INTO files (fileset_id, original_name, stored_name, size, wrapped_key)
			 VALUES (?, ?, ?, ?, ?)`,
			filesetID, f.displayName, f.storedName, f.size, f.wrappedKey,
		); err != nil {
			os.RemoveAll(dest)
			return "", err
		}
	}
	if err := tx.Commit(); err != nil {
		os.RemoveAll(dest)
		return "", err
	}
	return pin, nil
}

func isTooLarge(err error) bool {
	var maxErr *http.MaxBytesError
	return errors.As(err, &maxErr) || strings.Contains(err.Error(), "request body too large")
}

func writeTooLarge(w http.ResponseWriter) {
	writeError(w, http.StatusRequestEntityTooLarge,
		fmt.Sprintf("upload exceeds the %d byte limit", maxUploadBytes))
}

// POST /api/transfer?expiry=<seconds>
func handleUpload(w http.ResponseWriter, r *http.Request) {
	if maxUploadBytes > 0 {
		r.Body = http.MaxBytesReader(w, r.Body, maxUploadBytes)
	}

	mediaType, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || !strings.HasPrefix(mediaType, "multipart/") {
		writeError(w, http.StatusBadRequest, "expected multipart/form-data")
		return
	}
	boundary := params["boundary"]
	if boundary == "" {
		writeError(w, http.StatusBadRequest, "missing multipart boundary")
		return
	}

	now := time.Now().UTC()
	expiresAt := now.Add(time.Duration(parseExpiry(r)) * time.Second)

	staging, err := stageDir()
	if err != nil {
		log.Printf("staging dir error: %v", err)
		writeError(w, http.StatusInternalServerError, "could not create storage directory")
		return
	}
	done := false
	defer func() {
		if !done {
			os.RemoveAll(staging)
		}
	}()

	mr := multipart.NewReader(r.Body, boundary)
	var pending []pendingFile
	var textContent string

	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			if isTooLarge(err) {
				writeTooLarge(w)
				return
			}
			log.Printf("multipart error: %v", err)
			writeError(w, http.StatusBadRequest, "multipart read error")
			return
		}

		if part.FormName() == "text" && part.FileName() == "" {
			buf, err := io.ReadAll(io.LimitReader(part, 1<<20))
			part.Close()
			if err != nil {
				writeError(w, http.StatusBadRequest, "could not read text field")
				return
			}
			textContent = string(buf)
			continue
		}
		if part.FileName() == "" {
			part.Close()
			continue
		}

		displayName := sanitizeDisplayName(part.FileName())
		destPath, storedName := uniqueDestPath(staging, sanitizeStoredName(displayName))
		written, wrapped, err := storeEncrypted(destPath, part)
		part.Close()
		if err != nil {
			if isTooLarge(err) {
				writeTooLarge(w)
				return
			}
			log.Printf("store file error: %v", err)
			writeError(w, http.StatusInternalServerError, "could not save file")
			return
		}
		pending = append(pending, pendingFile{displayName, storedName, written, wrapped})
	}

	if len(pending) == 0 && textContent == "" {
		writeError(w, http.StatusBadRequest, "no files or text provided")
		return
	}

	pin, err := commitTransfer(staging, pending, textContent, now, expiresAt)
	if err != nil {
		log.Printf("commit transfer error: %v", err)
		writeError(w, http.StatusInternalServerError, "could not create transfer")
		return
	}
	done = true

	files := make([]FileInfo, len(pending))
	for i, f := range pending {
		files[i] = FileInfo{Name: f.displayName, Size: f.size}
	}
	writeJSON(w, http.StatusCreated, buildResponse(r, pin, expiresAt, files, textContent))
}

func sealText(text string) (blob, wrapped []byte, err error) {
	dek, err := newDEK()
	if err != nil {
		return nil, nil, err
	}
	if wrapped, err = wrapDEK(masterKey, dek); err != nil {
		return nil, nil, err
	}
	if blob, err = sealBytes(dek, []byte(text)); err != nil {
		return nil, nil, err
	}
	return blob, wrapped, nil
}

// PUT|POST /api/upload/:filename - raw body upload, no multipart.
func handleRawUpload(w http.ResponseWriter, r *http.Request) {
	if maxUploadBytes > 0 {
		r.Body = http.MaxBytesReader(w, r.Body, maxUploadBytes)
	}

	raw := chi.URLParam(r, "filename")
	if decoded, err := url.PathUnescape(raw); err == nil {
		raw = decoded
	}
	if strings.TrimSpace(raw) == "" {
		writeError(w, http.StatusBadRequest, "missing filename in path: use /api/upload/<filename>")
		return
	}
	if r.Body == nil {
		writeError(w, http.StatusBadRequest, "empty request body")
		return
	}
	displayName := sanitizeDisplayName(raw)

	now := time.Now().UTC()
	expiresAt := now.Add(time.Duration(parseExpiry(r)) * time.Second)

	staging, err := stageDir()
	if err != nil {
		log.Printf("staging dir error: %v", err)
		writeError(w, http.StatusInternalServerError, "could not create storage directory")
		return
	}
	done := false
	defer func() {
		if !done {
			os.RemoveAll(staging)
		}
	}()

	destPath, storedName := uniqueDestPath(staging, sanitizeStoredName(displayName))
	written, wrapped, err := storeEncrypted(destPath, r.Body)
	if err != nil {
		if isTooLarge(err) {
			writeTooLarge(w)
			return
		}
		log.Printf("store file error: %v", err)
		writeError(w, http.StatusInternalServerError, "could not save file")
		return
	}
	if written == 0 {
		writeError(w, http.StatusBadRequest,
			"empty request body: send the file as the raw body, e.g. curl -T FILE")
		return
	}

	pending := []pendingFile{{displayName, storedName, written, wrapped}}
	pin, err := commitTransfer(staging, pending, "", now, expiresAt)
	if err != nil {
		log.Printf("commit transfer error: %v", err)
		writeError(w, http.StatusInternalServerError, "could not create transfer")
		return
	}
	done = true

	writeJSON(w, http.StatusCreated,
		buildResponse(r, pin, expiresAt, []FileInfo{{Name: displayName, Size: written}}, ""))
}

// ----------------------------------------------------------------- downloads

// lookupFileset resolves a live transfer, feeding the enumeration lockout.
func lookupFileset(w http.ResponseWriter, r *http.Request, limiter *rateLimiter) (int64, time.Time, bool) {
	pin := chi.URLParam(r, "pin")
	ip := clientIP(r)

	var id int64
	var expiresAt time.Time
	err := db.QueryRow(
		`SELECT id, expires_at FROM filesets WHERE pin = ? AND expires_at > datetime('now')`, pin,
	).Scan(&id, &expiresAt)

	switch {
	case err == sql.ErrNoRows:
		limiter.recordFailure(ip)
		writeError(w, http.StatusNotFound, "transfer not found or expired")
		return 0, time.Time{}, false
	case err != nil:
		log.Printf("fileset lookup error: %v", err)
		writeError(w, http.StatusInternalServerError, "db error")
		return 0, time.Time{}, false
	}

	limiter.recordSuccess(ip)
	return id, expiresAt, true
}

// readText decrypts a fileset's note, falling back to the pre-encryption column
// for transfers created before this version.
func readText(filesetID int64) string {
	var blob, wrapped []byte
	var legacy sql.NullString
	err := db.QueryRow(
		`SELECT text_blob, text_key, text_content FROM filesets WHERE id = ?`, filesetID,
	).Scan(&blob, &wrapped, &legacy)
	if err != nil {
		return ""
	}
	if len(wrapped) == 0 || len(blob) == 0 {
		return legacy.String
	}
	dek, err := unwrapDEK(masterKey, wrapped)
	if err != nil {
		log.Printf("text key unwrap failed for fileset %d: %v", filesetID, err)
		return ""
	}
	plain, err := openBytes(dek, blob)
	if err != nil {
		log.Printf("text decrypt failed for fileset %d: %v", filesetID, err)
		return ""
	}
	return string(plain)
}

// GET /api/transfer/:pin
func handleGetTransfer(w http.ResponseWriter, r *http.Request) {
	filesetID, expiresAt, ok := lookupFileset(w, r, lookupLimiter)
	if !ok {
		return
	}

	rows, err := db.Query(`SELECT original_name, size FROM files WHERE fileset_id = ? ORDER BY id`, filesetID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db error")
		return
	}
	files := []FileInfo{}
	for rows.Next() {
		var f FileInfo
		if err := rows.Scan(&f.Name, &f.Size); err != nil {
			continue
		}
		files = append(files, f)
	}
	rows.Close()

	writeJSON(w, http.StatusOK,
		buildResponse(r, chi.URLParam(r, "pin"), expiresAt, files, readText(filesetID)))
}

type storedFile struct {
	storedName  string
	displayName string
	size        int64
	wrappedKey  []byte
}

// findFile matches a URL-supplied name against the stored file, trying each
// encoding the name might have arrived in.
func findFile(filesetID int64, rawName string) (storedFile, error) {
	candidates := fileNameCandidates(rawName)
	if len(candidates) == 0 {
		return storedFile{}, sql.ErrNoRows
	}

	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(candidates)), ",")
	query := fmt.Sprintf(
		`SELECT stored_name, original_name, size, wrapped_key FROM files
		 WHERE fileset_id = ? AND (original_name IN (%s) OR stored_name IN (%s)) LIMIT 1`,
		placeholders, placeholders)

	args := make([]any, 0, 1+2*len(candidates))
	args = append(args, filesetID)
	for range 2 {
		for _, c := range candidates {
			args = append(args, c)
		}
	}

	var sf storedFile
	err := db.QueryRow(query, args...).Scan(&sf.storedName, &sf.displayName, &sf.size, &sf.wrappedKey)
	return sf, err
}

// openStored opens a stored file for reading, transparently handling both
// encrypted files and any plaintext ones left from before encryption existed.
func openStored(pin string, sf storedFile) (io.ReadCloser, func(io.Writer) error, error) {
	f, err := os.Open(filepath.Join(transferDir(pin), sf.storedName))
	if err != nil {
		return nil, nil, err
	}
	if len(sf.wrappedKey) == 0 {
		// Legacy transfer written before encryption; copy through as-is.
		return f, func(dst io.Writer) error { _, err := io.Copy(dst, f); return err }, nil
	}
	dek, err := unwrapDEK(masterKey, sf.wrappedKey)
	if err != nil {
		f.Close()
		return nil, nil, err
	}
	return f, func(dst io.Writer) error { return decryptStream(dst, f, dek) }, nil
}

// GET /api/transfer/:pin/download/*
func handleDownloadFile(w http.ResponseWriter, r *http.Request) {
	filesetID, _, ok := lookupFileset(w, r, downloadLimiter)
	if !ok {
		return
	}
	pin := chi.URLParam(r, "pin")

	sf, err := findFile(filesetID, chi.URLParam(r, "*"))
	if err == sql.ErrNoRows {
		writeError(w, http.StatusNotFound, "file not found")
		return
	}
	if err != nil {
		log.Printf("file lookup error: %v", err)
		writeError(w, http.StatusInternalServerError, "db error")
		return
	}

	closer, copyTo, err := openStored(pin, sf)
	if err != nil {
		log.Printf("open stored file error: %v", err)
		writeError(w, http.StatusNotFound, "file not found on disk")
		return
	}
	defer closer.Close()

	// size is the plaintext length, which is what the client will receive.
	w.Header().Set("Content-Disposition", contentDisposition(sf.displayName))
	w.Header().Set("Content-Length", strconv.FormatInt(sf.size, 10))
	w.Header().Set("Content-Type", "application/octet-stream")

	if err := copyTo(w); err != nil {
		// Headers are already sent, so this can only be logged.
		log.Printf("download stream error (pin ***, file %q): %v", sf.storedName, err)
	}
}

// GET /api/transfer/:pin/zip
func handleDownloadZip(w http.ResponseWriter, r *http.Request) {
	filesetID, _, ok := lookupFileset(w, r, downloadLimiter)
	if !ok {
		return
	}
	pin := chi.URLParam(r, "pin")

	rows, err := db.Query(
		`SELECT stored_name, original_name, size, wrapped_key FROM files WHERE fileset_id = ? ORDER BY id`,
		filesetID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db error")
		return
	}
	var entries []storedFile
	for rows.Next() {
		var sf storedFile
		if err := rows.Scan(&sf.storedName, &sf.displayName, &sf.size, &sf.wrappedKey); err != nil {
			continue
		}
		entries = append(entries, sf)
	}
	rows.Close() // released before streaming: the pool is single-connection

	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", contentDisposition(fmt.Sprintf("shadowfax-%s.zip", pin)))

	zw := zip.NewWriter(w)
	defer zw.Close()

	used := make(map[string]bool)
	for _, sf := range entries {
		// Re-sanitize on the way out: the archive entry name is interpreted as
		// a path by whatever unzips it, so a traversal name here would write
		// outside the recipient's chosen directory.
		name := sanitizeDisplayName(sf.displayName)
		for i := 1; used[name]; i++ {
			ext := filepath.Ext(name)
			name = fmt.Sprintf("%s_%d%s", strings.TrimSuffix(name, ext), i, ext)
		}
		used[name] = true

		closer, copyTo, err := openStored(pin, sf)
		if err != nil {
			log.Printf("zip: skipping %q: %v", sf.storedName, err)
			continue
		}
		fw, err := zw.Create(name)
		if err != nil {
			closer.Close()
			continue
		}
		if err := copyTo(fw); err != nil {
			log.Printf("zip: stream error for %q: %v", sf.storedName, err)
		}
		closer.Close()
	}
}

// DELETE /api/transfer/:pin
func handleDeleteTransfer(w http.ResponseWriter, r *http.Request) {
	pin := chi.URLParam(r, "pin")

	var filesetID int64
	err := db.QueryRow(`SELECT id FROM filesets WHERE pin = ?`, pin).Scan(&filesetID)
	if err == sql.ErrNoRows {
		lookupLimiter.recordFailure(clientIP(r))
		writeError(w, http.StatusNotFound, "transfer not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db error")
		return
	}

	os.RemoveAll(transferDir(pin))

	// Dropping the row destroys the wrapped keys, so the ciphertext is
	// unrecoverable even if the blocks survive on disk.
	if _, err := db.Exec(`DELETE FROM filesets WHERE id = ?`, filesetID); err != nil {
		writeError(w, http.StatusInternalServerError, "db error")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ------------------------------------------------------------------- cleanup

func startCleanupRoutine() {
	go func() {
		for range time.Tick(time.Minute) {
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
	type entry struct {
		id  int64
		pin string
	}
	var expired []entry
	for rows.Next() {
		var e entry
		if err := rows.Scan(&e.id, &e.pin); err == nil {
			expired = append(expired, e)
		}
	}
	rows.Close()

	for _, e := range expired {
		if err := os.RemoveAll(transferDir(e.pin)); err != nil {
			log.Printf("cleanup remove dir error: %v", err)
		}
		if _, err := db.Exec(`DELETE FROM filesets WHERE id = ?`, e.id); err != nil {
			log.Printf("cleanup db delete error for id %d: %v", e.id, err)
		}
	}
	if len(expired) > 0 {
		log.Printf("cleaned up %d expired transfer(s)", len(expired))
	}
}

// ------------------------------------------------------------------ database

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
		`PRAGMA busy_timeout=5000`,
		// Zero out freed pages so deleted wrapped keys do not linger in the
		// database file after a transfer expires.
		`PRAGMA secure_delete=ON`,
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
  text_content TEXT NOT NULL DEFAULT '',
  text_blob BLOB,
  text_key BLOB
);
CREATE TABLE IF NOT EXISTS files (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  fileset_id INTEGER NOT NULL,
  original_name TEXT NOT NULL,
  stored_name TEXT NOT NULL,
  size INTEGER NOT NULL,
  wrapped_key BLOB,
  FOREIGN KEY (fileset_id) REFERENCES filesets(id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS idx_files_fileset ON files(fileset_id);
CREATE INDEX IF NOT EXISTS idx_filesets_expires ON filesets(expires_at);
`
	if _, err := db.Exec(schema); err != nil {
		return fmt.Errorf("create schema: %w", err)
	}

	// Migrations for databases created by earlier versions. Each is a no-op
	// (and a harmless error) when the column already exists.
	for _, stmt := range []string{
		`ALTER TABLE filesets ADD COLUMN text_content TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE filesets ADD COLUMN text_blob BLOB`,
		`ALTER TABLE filesets ADD COLUMN text_key BLOB`,
		`ALTER TABLE files ADD COLUMN wrapped_key BLOB`,
	} {
		db.Exec(stmt)
	}

	var legacy int
	db.QueryRow(`SELECT COUNT(*) FROM files WHERE wrapped_key IS NULL`).Scan(&legacy)
	if legacy > 0 {
		log.Printf("note: %d file(s) predate encryption and remain plaintext on disk; "+
			"they stay readable until they expire, and every new upload is encrypted", legacy)
	}

	return nil
}

// ---------------------------------------------------------------------- docs

func isBrowser(r *http.Request) bool {
	return strings.Contains(r.Header.Get("User-Agent"), "Mozilla")
}

func renderDocs(r *http.Request) string {
	base := baseURL(r)
	keyHeader := ""
	keyNote := "No API key is required."
	if apiKey == "" && uploadPassword != "" {
		keyNote = "Uploads on this server require signing in on the web UI; there is no API key for scripts."
	}
	if apiKey != "" {
		keyHeader = ` -H "X-API-Key: <your key>"`
		keyNote = `This server requires an API key. Add the header  -H "X-API-Key: <your key>"  to every upload/delete request (ask the operator for the key).`
	}

	return fmt.Sprintf(`# Shadowfax - file transfer API (for humans and robots)

Upload a file and you get back a %[5]d-digit PIN plus a share URL. Give the PIN or
the share_url to whoever (or whatever) needs the file; it expires automatically.

Files are encrypted at rest with AES-256-GCM and the key is destroyed when the
transfer expires. Anyone holding the PIN can download, so treat it as a secret.

Base URL: %[1]s
Auth: %[2]s

## Upload (simplest - raw body, recommended for scripts/agents)
Send the file as the raw request body. The filename goes in the URL path:

    curl -T ./android-sdk.zip%[3]s %[1]s/api/upload/android-sdk.zip

## Upload (multipart - supports multiple files and a text note)

    curl -F "files=@./android-sdk.zip"%[3]s %[1]s/api/transfer?expiry=3600

Add more "-F files=@..." for multiple files, and "-F text=..." for a note.

## Optional: how long it lives
Append ?expiry=<seconds> to either upload URL. Default 3600 (1h), max %[4]d.
Common values: 3600 (1h), 21600 (6h), 86400 (1d), 259200 (3d), 604800 (7d).

## What you get back (HTTP 201, JSON)
    {
      "pin": "%[6]s",
      "expires_at": "2026-06-13T22:45:00Z",
      "share_url": "%[1]s/?pin=%[6]s",
      "files": [{"name": "android-sdk.zip", "size": 1234,
                 "download_url": "%[1]s/api/transfer/%[6]s/download/android-sdk.zip"}],
      "zip_url": "%[1]s/api/transfer/%[6]s/zip",
      "encrypted": true,
      "message": "Share PIN %[6]s or the link below ..."
    }

>> Relay the "pin" and "share_url" back to the user - that is how they download the file.

## Download
    GET %[1]s/api/transfer/<pin>                      -> JSON metadata
    GET %[1]s/api/transfer/<pin>/download/<filename>  -> the file
    GET %[1]s/api/transfer/<pin>/zip                  -> all files as a zip

Lookups are rate limited per IP, and repeatedly guessing wrong PINs will lock
your address out for a while. Use the PIN you were given.
`, base, keyNote, keyHeader, maxExpirySecs, pinLength, strings.Repeat("1", pinLength))
}

func handleDocs(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/api" && strings.Contains(r.Header.Get("Accept"), "application/json") {
		writeJSON(w, http.StatusOK, map[string]any{
			"service":          "shadowfax",
			"base_url":         baseURL(r),
			"api_key_required": apiKey != "",
			"pin_length":       pinLength,
			"max_expiry_secs":  maxExpirySecs,
			"max_upload_bytes": maxUploadBytes,
			"encryption":       "AES-256-GCM at rest; per-file keys wrapped by a master key",
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

// handleConfig tells the web UI how the server is configured, so the PIN entry
// boxes match PIN_LENGTH instead of assuming four.
func handleConfig(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"pin_length":       pinLength,
		"max_upload_bytes": maxUploadBytes,
		"max_expiry_secs":  maxExpirySecs,
		"api_key_required": apiKey != "",
		"login_required":   authRequired(),
		"encrypted":        true,
	})
}

// ------------------------------------------------------------------ frontend

func spaHandler(staticFS fs.FS) http.HandlerFunc {
	fileServer := http.FileServer(http.FS(staticFS))
	const csp = "default-src 'self'; img-src 'self' data:; style-src 'self' 'unsafe-inline'; " +
		"script-src 'self'; connect-src 'self'; object-src 'none'; base-uri 'none'; " +
		"form-action 'self'; frame-ancestors 'none'"

	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" && !isBrowser(r) {
			handleDocs(w, r)
			return
		}

		urlPath := r.URL.Path
		if urlPath == "/" {
			urlPath = "/index.html"
		}

		if f, err := staticFS.Open(strings.TrimPrefix(urlPath, "/")); err == nil {
			f.Close()
			w.Header().Set("Content-Security-Policy", csp)
			// Hashed asset filenames are safe to cache; index.html is not.
			if strings.HasPrefix(urlPath, "/assets/") {
				w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			}
			fileServer.ServeHTTP(w, r)
			return
		}

		index, err := staticFS.Open("index.html")
		if err != nil {
			http.Error(w, "index.html not found", http.StatusInternalServerError)
			return
		}
		defer index.Close()

		w.Header().Set("Content-Security-Policy", csp)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		io.Copy(w, index)
	}
}

// ---------------------------------------------------------------------- main

func loadConfig() {
	dataDir = envString("DATA_DIR", "./data")
	apiKey = strings.TrimSpace(os.Getenv("API_KEY"))
	uploadPassword = os.Getenv("UPLOAD_PASSWORD")
	pinLength = envInt("PIN_LENGTH", 6, 4, 10)
	trustProxy = envBool("TRUSTED_PROXY", false)
	maxUploadBytes = envInt64("MAX_UPLOAD_BYTES", 5<<30) // 5 GiB, 0 disables
	maxExpirySecs = envInt64("MAX_EXPIRY_SECONDS", 7*24*3600)
	if maxExpirySecs == 0 {
		maxExpirySecs = 7 * 24 * 3600
	}

	for _, o := range strings.Split(os.Getenv("ALLOWED_ORIGINS"), ",") {
		if o = strings.TrimSpace(o); o != "" {
			allowedOrigins = append(allowedOrigins, o)
		}
	}

	lookupPerMin := envInt("RATE_LIMIT_LOOKUPS_PER_MIN", 10, 1, 100000)
	downloadPerMin := envInt("RATE_LIMIT_DOWNLOADS_PER_MIN", 60, 1, 100000)
	uploadPerMin := envInt("RATE_LIMIT_UPLOADS_PER_MIN", 20, 1, 100000)
	failLimit := envInt("RATE_LIMIT_FAILURES", 20, 1, 100000)
	lockoutMins := envInt("RATE_LIMIT_LOCKOUT_MINUTES", 15, 1, 1440)
	lockout := time.Duration(lockoutMins) * time.Minute

	lookupLimiter = newRateLimiter(lookupPerMin, failLimit, lockout)
	downloadLimiter = newRateLimiter(downloadPerMin, failLimit, lockout)
	uploadLimiter = newRateLimiter(uploadPerMin, 0, lockout)
	loginLimiter = newRateLimiter(10, envInt("RATE_LIMIT_LOGIN_FAILURES", 5, 1, 1000), lockout)
}

func warnAboutConfig() {
	if !authRequired() {
		log.Printf("WARNING: neither API_KEY nor UPLOAD_PASSWORD is set, so anyone who can reach " +
			"this server can upload and delete transfers. Set one on any instance exposed to the internet.")
	}
	if len(allowedOrigins) == 1 && allowedOrigins[0] == "*" {
		log.Printf("WARNING: ALLOWED_ORIGINS=* lets any website call this API from a browser.")
	}
	if !trustProxy {
		log.Printf("note: TRUSTED_PROXY is off, so forwarded headers are ignored and rate limits " +
			"key on the direct peer address. Set TRUSTED_PROXY=true when running behind a reverse proxy.")
	}
	pinSpace := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(pinLength)), nil)
	log.Printf("PIN length %d (%s possible PINs), drawn from crypto/rand", pinLength, pinSpace)
}

// newRouter wires every route. main and the tests share it so tests exercise
// the same middleware chain the server runs.
func newRouter(staticFS fs.FS) chi.Router {
	r := chi.NewRouter()
	r.Use(middleware.Recoverer)
	r.Use(requestLogger)
	r.Use(securityHeaders)
	r.Use(corsMiddleware)

	r.Get("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		io.WriteString(w, "ok")
	})
	r.Get("/llms.txt", handleDocs)
	r.Get("/api", handleDocs)
	r.Get("/api/config", handleConfig)
	r.Get("/api/session", handleSession)
	r.Post("/api/logout", handleLogout)
	r.With(loginLimiter.middleware).Post("/api/login", handleLogin)

	// Write routes: optional API key, plus a per-IP ceiling so an anonymous
	// instance cannot be used to fill the disk.
	r.Group(func(pr chi.Router) {
		pr.Use(uploadLimiter.middleware)
		pr.Use(requireUploadAuth)
		pr.Post("/api/transfer", handleUpload)
		pr.Put("/api/upload/{filename}", handleRawUpload)
		pr.Post("/api/upload/{filename}", handleRawUpload)
		pr.Delete("/api/transfer/{pin}", handleDeleteTransfer)
	})

	// Read routes stay open so a recipient needs nothing but the PIN, but they
	// are rate limited: this is the surface an enumeration attack would use.
	r.Group(func(pr chi.Router) {
		pr.Use(lookupLimiter.middleware)
		pr.Get("/api/transfer/{pin}", handleGetTransfer)
	})
	r.Group(func(pr chi.Router) {
		pr.Use(downloadLimiter.middleware)
		// Wildcard, not {filename}: a name containing an encoded slash would
		// otherwise fail to route at all.
		pr.Get("/api/transfer/{pin}/download/*", handleDownloadFile)
		pr.Get("/api/transfer/{pin}/zip", handleDownloadZip)
	})

	r.Get("/*", spaHandler(staticFS))

	return r
}

func main() {
	log.SetFlags(log.LstdFlags | log.LUTC)
	loadConfig()

	if err := os.MkdirAll(filepath.Join(dataDir, "files"), 0o700); err != nil {
		log.Fatalf("create data dir: %v", err)
	}

	var err error
	if masterKey, err = loadMasterKey(dataDir); err != nil {
		log.Fatalf("encryption: %v", err)
	}

	dbPath := filepath.Join(dataDir, "shadowfax.db")
	if err := initDB(dbPath); err != nil {
		log.Fatalf("init db: %v", err)
	}
	defer db.Close()
	os.Chmod(dbPath, 0o600)

	warnAboutConfig()
	os.RemoveAll(filepath.Join(dataDir, "tmp")) // discard uploads interrupted by a restart
	cleanupExpired()
	startCleanupRoutine()

	subFS, err := fs.Sub(staticFiles, "static")
	if err != nil {
		log.Fatalf("embed sub: %v", err)
	}

	port := envString("PORT", "8080")
	srv := &http.Server{
		Addr:    ":" + port,
		Handler: newRouter(subFS),
		// No Read/WriteTimeout: transfers can legitimately run for a long time.
		// ReadHeaderTimeout still bounds slowloris-style header stalls.
		ReadHeaderTimeout: 15 * time.Second,
		IdleTimeout:       90 * time.Second,
		MaxHeaderBytes:    1 << 16,
	}

	log.Printf("Shadowfax listening on :%s (data dir: %s, encrypted at rest)", port, dataDir)
	if err := srv.ListenAndServe(); err != nil {
		log.Fatalf("server error: %v", err)
	}
}
