package main

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"
)

// newTestServer boots a real server against a throwaway data directory.
// Any opts run after the defaults but before the router is built, because the
// middleware binds the limiter instances it is given.
func newTestServer(t *testing.T, opts ...func()) *httptest.Server {
	t.Helper()
	log.SetOutput(io.Discard)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	dir := t.TempDir()
	dataDir = dir
	apiKey = ""
	uploadPassword = ""
	pinLength = 6
	trustProxy = false
	maxUploadBytes = 5 << 20
	maxExpirySecs = 7 * 24 * 3600
	allowedOrigins = nil
	lookupLimiter = newRateLimiter(10000, 20, time.Minute)
	downloadLimiter = newRateLimiter(10000, 20, time.Minute)
	uploadLimiter = newRateLimiter(10000, 0, time.Minute)
	loginLimiter = newRateLimiter(10000, 5, time.Minute)

	if err := os.MkdirAll(filepath.Join(dir, "files"), 0o700); err != nil {
		t.Fatal(err)
	}
	key, err := loadMasterKey(dir)
	if err != nil {
		t.Fatal(err)
	}
	masterKey = key
	if err := initDB(filepath.Join(dir, "shadowfax.db")); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	for _, opt := range opts {
		opt()
	}

	static := fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("<html></html>")}}
	srv := httptest.NewServer(newRouter(static))
	t.Cleanup(srv.Close)
	return srv
}

// encodeURIComponent, as a browser encodes a filename into a URL.
func euc(s string) string {
	const keep = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_.!~*'()"
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if strings.IndexByte(keep, s[i]) >= 0 {
			b.WriteByte(s[i])
		} else {
			fmt.Fprintf(&b, "%%%02X", s[i])
		}
	}
	return b.String()
}

func uploadMultipart(t *testing.T, srv *httptest.Server, files map[string][]byte, text string) TransferResponse {
	t.Helper()

	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	for name, content := range files {
		fw, err := mw.CreateFormFile("files", name)
		if err != nil {
			t.Fatal(err)
		}
		fw.Write(content)
	}
	if text != "" {
		mw.WriteField("text", text)
	}
	mw.Close()

	resp, err := http.Post(srv.URL+"/api/transfer", mw.FormDataContentType(), &body)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("upload: status %d: %s", resp.StatusCode, b)
	}

	var out TransferResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	return out
}

// TestDownloadFilenamesWithSpecialCharacters is the regression test for the
// reported bug: chi hands the handler a still-percent-encoded filename whenever
// the request path is not in Go's canonical escaping, which is the case for
// ordinary names containing "(", "+", ",", "&", ":", ";", "=", "@" or "$".
func TestDownloadFilenamesWithSpecialCharacters(t *testing.T) {
	srv := newTestServer(t)

	names := []string{
		"plain.txt",
		"my file.pdf",
		"2024-01-01 report (final).pdf",
		"a+b.zip",
		"notes,final.txt",
		"tom & jerry.txt",
		"100%.pdf",
		"résumé.pdf",
		"log:1.txt",
		"a;b.txt",
		"f@x.txt",
		"cost=5.csv",
		"a$b.txt",
		"日本語.txt",
		"emoji 🚀 file.bin",
		"quote\"name.txt",
		"back\\slash.txt",
	}

	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			content := []byte("contents of " + name)
			tr := uploadMultipart(t, srv, map[string][]byte{name: content}, "")
			if len(tr.Files) != 1 {
				t.Fatalf("expected 1 file, got %d", len(tr.Files))
			}
			stored := tr.Files[0].Name

			// 1. The download_url the API itself advertises.
			resp, err := http.Get(tr.Files[0].DownloadURL)
			if err != nil {
				t.Fatal(err)
			}
			got, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("api download_url: status %d for %q", resp.StatusCode, stored)
			}
			if !bytes.Equal(got, content) {
				t.Fatalf("api download_url: content mismatch for %q", stored)
			}

			// 2. The URL the browser builds with encodeURIComponent.
			browserURL := fmt.Sprintf("%s/api/transfer/%s/download/%s", srv.URL, tr.Pin, euc(stored))
			resp2, err := http.Get(browserURL)
			if err != nil {
				t.Fatal(err)
			}
			got2, _ := io.ReadAll(resp2.Body)
			resp2.Body.Close()
			if resp2.StatusCode != http.StatusOK {
				t.Fatalf("browser-style URL: status %d for %q (%s)", resp2.StatusCode, stored, browserURL)
			}
			if !bytes.Equal(got2, content) {
				t.Fatalf("browser-style URL: content mismatch for %q", stored)
			}

			// The name must survive the round trip in the download header.
			if cd := resp2.Header.Get("Content-Disposition"); !strings.Contains(cd, "filename*=UTF-8''") {
				t.Fatalf("Content-Disposition lacks an RFC 5987 name: %q", cd)
			}
		})
	}
}

func TestFilesAreEncryptedOnDisk(t *testing.T) {
	srv := newTestServer(t)

	secret := []byte("SUPER-SECRET-CANARY-abcdefghijklmnop")
	tr := uploadMultipart(t, srv, map[string][]byte{"secret.txt": secret}, "note canary: TOP-SECRET-NOTE")

	// Nothing anywhere under the data directory may contain the plaintext.
	var checked int
	err := filepath.Walk(dataDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		blob, readErr := os.ReadFile(path)
		if readErr != nil {
			return nil
		}
		checked++
		if bytes.Contains(blob, secret) {
			return fmt.Errorf("plaintext file content found in %s", path)
		}
		if bytes.Contains(blob, []byte("TOP-SECRET-NOTE")) {
			return fmt.Errorf("plaintext text note found in %s", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if checked == 0 {
		t.Fatal("walked no files")
	}

	// ...but it still reads back correctly through the API.
	resp, err := http.Get(tr.Files[0].DownloadURL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	got, _ := io.ReadAll(resp.Body)
	if !bytes.Equal(got, secret) {
		t.Fatal("decrypted download did not match the original")
	}
	if cl := resp.Header.Get("Content-Length"); cl != fmt.Sprint(len(secret)) {
		t.Fatalf("Content-Length %q is not the plaintext size %d", cl, len(secret))
	}
}

func TestTextNoteRoundTripsEncrypted(t *testing.T) {
	srv := newTestServer(t)
	note := "meet me at the usual place"
	tr := uploadMultipart(t, srv, map[string][]byte{"a.txt": []byte("x")}, note)

	var blob, key []byte
	if err := db.QueryRow(`SELECT text_blob, text_key FROM filesets WHERE pin = ?`, tr.Pin).
		Scan(&blob, &key); err != nil {
		t.Fatal(err)
	}
	if len(key) == 0 || bytes.Contains(blob, []byte(note)) {
		t.Fatal("text note is not encrypted in the database")
	}

	resp, err := http.Get(srv.URL + "/api/transfer/" + tr.Pin)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out TransferResponse
	json.NewDecoder(resp.Body).Decode(&out)
	if out.Text != note {
		t.Fatalf("text round trip: got %q want %q", out.Text, note)
	}
}

func TestZipDownloadAndZipSlip(t *testing.T) {
	srv := newTestServer(t)

	tr := uploadMultipart(t, srv, map[string][]byte{
		"../../../etc/passwd": []byte("traversal attempt"),
		"normal.txt":          []byte("hello"),
	}, "")

	resp, err := http.Get(tr.ZipURL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	zr, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		t.Fatalf("zip is unreadable: %v", err)
	}
	if len(zr.File) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(zr.File))
	}
	for _, f := range zr.File {
		if strings.Contains(f.Name, "..") || strings.HasPrefix(f.Name, "/") ||
			filepath.IsAbs(f.Name) {
			t.Fatalf("zip entry escapes the extraction directory: %q", f.Name)
		}
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		content, _ := io.ReadAll(rc)
		rc.Close()
		if len(content) == 0 {
			t.Fatalf("entry %q decrypted to nothing", f.Name)
		}
	}
}

func TestPINIsRandomAndCorrectLength(t *testing.T) {
	srv := newTestServer(t)

	seen := make(map[string]bool)
	for i := 0; i < 40; i++ {
		tr := uploadMultipart(t, srv, map[string][]byte{"f.txt": []byte("x")}, "")
		if len(tr.Pin) != pinLength {
			t.Fatalf("PIN %q is not %d digits", tr.Pin, pinLength)
		}
		if strings.Trim(tr.Pin, "0123456789") != "" {
			t.Fatalf("PIN %q is not all digits", tr.Pin)
		}
		if seen[tr.Pin] {
			t.Fatalf("duplicate PIN %q issued", tr.Pin)
		}
		seen[tr.Pin] = true
	}
}

// TestConcurrentUploadsGetDistinctPINs covers the race the old
// SELECT-then-INSERT had, where two uploads could pick the same PIN.
func TestConcurrentUploadsGetDistinctPINs(t *testing.T) {
	srv := newTestServer(t)

	const n = 25
	pins := make([]string, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			tr := uploadMultipart(t, srv, map[string][]byte{"f.txt": []byte("x")}, "")
			pins[i] = tr.Pin
		}(i)
	}
	wg.Wait()

	seen := make(map[string]bool)
	for _, p := range pins {
		if p == "" {
			t.Fatal("an upload failed under concurrency")
		}
		if seen[p] {
			t.Fatalf("two concurrent uploads got the same PIN %q", p)
		}
		seen[p] = true
	}
}

func TestWrongPINsTriggerLockout(t *testing.T) {
	// Tight failure budget so the lockout is reachable in a test.
	srv := newTestServer(t, func() {
		lookupLimiter = newRateLimiter(10000, 5, time.Minute)
	})

	var lockedOut bool
	for i := 0; i < 12; i++ {
		resp, err := http.Get(fmt.Sprintf("%s/api/transfer/%06d", srv.URL, i))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode == http.StatusTooManyRequests {
			lockedOut = true
			break
		}
	}
	if !lockedOut {
		t.Fatal("guessing wrong PINs never triggered a lockout")
	}
}

func TestExpiryIsCapped(t *testing.T) {
	srv := newTestServer(t)

	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, _ := mw.CreateFormFile("files", "f.txt")
	fw.Write([]byte("x"))
	mw.Close()

	resp, err := http.Post(srv.URL+"/api/transfer?expiry=99999999999",
		mw.FormDataContentType(), &body)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var tr TransferResponse
	json.NewDecoder(resp.Body).Decode(&tr)

	maxAllowed := time.Now().Add(time.Duration(maxExpirySecs)*time.Second + time.Minute)
	if tr.ExpiresAt.After(maxAllowed) {
		t.Fatalf("expiry %v exceeds the cap", tr.ExpiresAt)
	}
}

func TestUploadSizeLimit(t *testing.T) {
	srv := newTestServer(t)
	maxUploadBytes = 1024

	resp, err := http.Post(srv.URL+"/api/upload/big.bin", "application/octet-stream",
		bytes.NewReader(make([]byte, 64*1024)))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized upload got status %d, want 413", resp.StatusCode)
	}
}

func TestRawUploadRoundTrip(t *testing.T) {
	srv := newTestServer(t)

	content := []byte("raw body upload")
	req, _ := http.NewRequest(http.MethodPut, srv.URL+"/api/upload/build%20(1).zip",
		bytes.NewReader(content))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("status %d: %s", resp.StatusCode, b)
	}
	var tr TransferResponse
	json.NewDecoder(resp.Body).Decode(&tr)
	if tr.Files[0].Name != "build (1).zip" {
		t.Fatalf("filename not decoded: %q", tr.Files[0].Name)
	}

	dl, err := http.Get(tr.Files[0].DownloadURL)
	if err != nil {
		t.Fatal(err)
	}
	defer dl.Body.Close()
	got, _ := io.ReadAll(dl.Body)
	if !bytes.Equal(got, content) {
		t.Fatal("raw upload round trip mismatch")
	}
}

func TestDeleteShredsTransfer(t *testing.T) {
	srv := newTestServer(t)
	tr := uploadMultipart(t, srv, map[string][]byte{"f.txt": []byte("x")}, "")

	req, _ := http.NewRequest(http.MethodDelete, srv.URL+"/api/transfer/"+tr.Pin, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete: status %d", resp.StatusCode)
	}

	if _, err := os.Stat(transferDir(tr.Pin)); !os.IsNotExist(err) {
		t.Fatal("transfer directory survived deletion")
	}
	var count int
	db.QueryRow(`SELECT COUNT(*) FROM filesets WHERE pin = ?`, tr.Pin).Scan(&count)
	if count != 0 {
		t.Fatal("fileset row survived deletion")
	}
}

func TestAPIKeyGating(t *testing.T) {
	srv := newTestServer(t)
	apiKey = "s3cret"
	t.Cleanup(func() { apiKey = "" })

	resp, err := http.Post(srv.URL+"/api/upload/x.txt", "application/octet-stream",
		strings.NewReader("hi"))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated upload got %d, want 401", resp.StatusCode)
	}

	req, _ := http.NewRequest(http.MethodPut, srv.URL+"/api/upload/x.txt", strings.NewReader("hi"))
	req.Header.Set("X-API-Key", "s3cret")
	resp2, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusCreated {
		t.Fatalf("authenticated upload got %d, want 201", resp2.StatusCode)
	}

	// Downloads stay open even with an API key configured.
	var tr TransferResponse
	json.NewDecoder(resp2.Body).Decode(&tr)
	dl, err := http.Get(tr.Files[0].DownloadURL)
	if err != nil {
		t.Fatal(err)
	}
	dl.Body.Close()
	if dl.StatusCode != http.StatusOK {
		t.Fatalf("download with API key set got %d, want 200", dl.StatusCode)
	}
}

func TestNoCORSHeaderByDefault(t *testing.T) {
	srv := newTestServer(t)

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/config", nil)
	req.Header.Set("Origin", "https://evil.example")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if got := resp.Header.Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("unexpected CORS header for an unlisted origin: %q", got)
	}
	if resp.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatal("security headers are missing")
	}
}

func TestPINsAreRedactedFromLogs(t *testing.T) {
	cases := map[string]string{
		"/api/transfer/482107":                "/api/transfer/***",
		"/api/transfer/482107/download/a.txt": "/api/transfer/***/download/a.txt",
		"/?pin=482107":                        "/?pin=***",
	}
	for in, want := range cases {
		if got := redactPIN(in); got != want {
			t.Fatalf("redactPIN(%q) = %q, want %q", in, got, want)
		}
	}
}
