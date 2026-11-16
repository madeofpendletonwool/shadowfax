package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

const testPassword = "hunter2-long-enough"

type manageListResp struct {
	Transfers []manageTransfer `json:"transfers"`
	Stats     struct {
		Transfers int   `json:"transfers"`
		Files     int   `json:"files"`
		Bytes     int64 `json:"bytes"`
		Downloads int   `json:"downloads"`
	} `json:"stats"`
}

// managedServer boots a server with a login and returns a signed-in client.
func managedServer(t *testing.T) (*httptest.Server, *http.Client) {
	t.Helper()
	srv := newTestServer(t, func() { uploadPassword = testPassword })
	c := newJarClient(t)
	if got := login(t, c, srv, testPassword); got != http.StatusOK {
		t.Fatalf("login got %d", got)
	}
	return srv, c
}

func doJSON(t *testing.T, c *http.Client, method, url string, body any, out any) int {
	t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, url, rd)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if out != nil {
		json.NewDecoder(resp.Body).Decode(out)
	}
	return resp.StatusCode
}

func multipartBody(t *testing.T, files map[string]string, text string) (*bytes.Buffer, string) {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	for name, content := range files {
		fw, _ := mw.CreateFormFile("files", name)
		fw.Write([]byte(content))
	}
	if text != "" {
		mw.WriteField("text", text)
	}
	mw.Close()
	return &body, mw.FormDataContentType()
}

func sessionUpload(t *testing.T, c *http.Client, srv *httptest.Server, files map[string]string, text string) TransferResponse {
	t.Helper()
	body, ct := multipartBody(t, files, text)
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/transfer?expiry=3600", body)
	req.Header.Set("Content-Type", ct)
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh) Chrome/130")
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("upload: %d %s", resp.StatusCode, b)
	}
	var tr TransferResponse
	json.NewDecoder(resp.Body).Decode(&tr)
	return tr
}

func getBody(t *testing.T, c *http.Client, url string) (int, string) {
	t.Helper()
	resp, err := c.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func TestManagementDisabledWithoutLogin(t *testing.T) {
	srv := newTestServer(t)
	uploadMultipart(t, srv, map[string][]byte{"a.txt": []byte("x")}, "")

	// On an open instance the listing would leak every PIN, so it must not exist.
	if code := doJSON(t, http.DefaultClient, http.MethodGet, srv.URL+"/api/manage/transfers", nil, nil); code != http.StatusForbidden {
		t.Fatalf("open instance listing got %d, want 403", code)
	}
}

func TestManagementRequiresAuth(t *testing.T) {
	srv, c := managedServer(t)
	sessionUpload(t, c, srv, map[string]string{"a.txt": "x"}, "")

	if code := doJSON(t, http.DefaultClient, http.MethodGet, srv.URL+"/api/manage/transfers", nil, nil); code != http.StatusUnauthorized {
		t.Fatalf("anonymous listing got %d, want 401", code)
	}

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/manage/transfers", nil)
	req.Header.Set("Origin", "https://evil.example")
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-origin listing got %d, want 403", resp.StatusCode)
	}

	// The API key works too, so scripts can manage what they upload.
	apiKey = "robot-key"
	t.Cleanup(func() { apiKey = "" })
	req, _ = http.NewRequest(http.MethodGet, srv.URL+"/api/manage/transfers", nil)
	req.Header.Set("X-API-Key", "robot-key")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("API key listing got %d, want 200", resp.StatusCode)
	}
}

func TestManageListShowsProvenanceAndStats(t *testing.T) {
	srv, c := managedServer(t)
	sessionUpload(t, c, srv, map[string]string{"a.txt": "hello", "b.txt": "world!"}, "a note")

	apiKey = "robot-key"
	t.Cleanup(func() { apiKey = "" })
	req, _ := http.NewRequest(http.MethodPut, srv.URL+"/api/upload/raw.bin", strings.NewReader("raw"))
	req.Header.Set("X-API-Key", "robot-key")
	req.Header.Set("User-Agent", "curl/8.0")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	var list manageListResp
	if code := doJSON(t, c, http.MethodGet, srv.URL+"/api/manage/transfers", nil, &list); code != http.StatusOK {
		t.Fatalf("list got %d", code)
	}
	if list.Stats.Transfers != 2 || list.Stats.Files != 3 || list.Stats.Bytes != 14 {
		t.Fatalf("unexpected stats: %+v", list.Stats)
	}
	raw, web := list.Transfers[0], list.Transfers[1] // newest first
	if raw.Upload.Method != "raw" || raw.Upload.Auth != authAPIKey || raw.Upload.UserAgent != "curl/8.0" {
		t.Fatalf("raw upload provenance: %+v", raw.Upload)
	}
	if web.Upload.Method != "web" || web.Upload.Auth != authSession || web.Upload.RequestedExpirySecs != 3600 {
		t.Fatalf("web upload provenance: %+v", web.Upload)
	}
	if !web.HasText || web.Text != nil {
		t.Fatal("list should flag the note without decrypting it")
	}
	if web.FileCount != 2 || web.Files[0].ContentType != "text/plain" || web.Files[0].OwnerURL == "" {
		t.Fatalf("web upload files: %+v", web.Files)
	}
}

func TestDownloadsAreCountedButOwnerDownloadsAreNot(t *testing.T) {
	srv, c := managedServer(t)
	tr := sessionUpload(t, c, srv, map[string]string{"a.txt": "hello"}, "")

	if code, body := getBody(t, http.DefaultClient, tr.Files[0].DownloadURL); code != 200 || body != "hello" {
		t.Fatalf("public download: %d %q", code, body)
	}
	if code, _ := getBody(t, http.DefaultClient, tr.ZipURL); code != 200 {
		t.Fatalf("public zip: %d", code)
	}

	var detail manageTransfer
	doJSON(t, c, http.MethodGet, srv.URL+"/api/manage/transfers/"+tr.Pin, nil, &detail)
	if code, body := getBody(t, c, detail.Files[0].OwnerURL); code != 200 || body != "hello" {
		t.Fatalf("owner download: %d %q", code, body)
	}
	if code, _ := getBody(t, c, detail.OwnerZipURL); code != 200 {
		t.Fatalf("owner zip: %d", code)
	}
	// Owner routes must not be reachable without the login.
	if code, _ := getBody(t, http.DefaultClient, detail.Files[0].OwnerURL); code != http.StatusUnauthorized {
		t.Fatalf("anonymous owner download got %d", code)
	}

	doJSON(t, c, http.MethodGet, srv.URL+"/api/manage/transfers/"+tr.Pin, nil, &detail)
	if detail.DownloadCount != 2 || detail.Files[0].DownloadCount != 2 || detail.LastDownloadAt == nil {
		t.Fatalf("download counts: transfer %d, file %d", detail.DownloadCount, detail.Files[0].DownloadCount)
	}
	kinds := map[string]int{}
	for _, e := range detail.Events {
		kinds[e.Kind]++
	}
	if kinds[eventCreated] != 1 || kinds[eventDownload] != 2 || kinds[eventOwnerDownload] != 2 {
		t.Fatalf("event log: %v", kinds)
	}
	if detail.DiskSize <= detail.TotalSize {
		t.Fatalf("on-disk size %d should exceed plaintext %d (encryption overhead)", detail.DiskSize, detail.TotalSize)
	}
}

func TestExtendAndClampExpiry(t *testing.T) {
	srv, c := managedServer(t)
	tr := sessionUpload(t, c, srv, map[string]string{"a.txt": "x"}, "")
	url := srv.URL + "/api/manage/transfers/" + tr.Pin

	var d manageTransfer
	if code := doJSON(t, c, http.MethodPatch, url, map[string]any{"extend_secs": 86400}, &d); code != 200 {
		t.Fatalf("extend got %d", code)
	}
	if got := d.ExpiresAt.Sub(*tr.ExpiresAt); got < 86399*time.Second || got > 86401*time.Second {
		t.Fatalf("extended by %v, want 24h", got)
	}
	if d.ExtendCount != 1 {
		t.Fatalf("extend_count = %d", d.ExtendCount)
	}

	doJSON(t, c, http.MethodPatch, url, map[string]any{"expires_in": 365 * 86400}, &d)
	if max := time.Now().Add(time.Duration(maxExpirySecs) * time.Second); d.ExpiresAt.After(max.Add(time.Second)) {
		t.Fatalf("expiry %v exceeds ceiling %v", d.ExpiresAt, max)
	}

	if code := doJSON(t, c, http.MethodPatch, url, map[string]any{"expires_in": 5}, nil); code != http.StatusBadRequest {
		t.Fatalf("near-immediate expiry got %d, want 400", code)
	}
	if code := doJSON(t, c, http.MethodPatch, url, map[string]any{"expires_in": 3600, "extend_secs": 60}, nil); code != http.StatusBadRequest {
		t.Fatalf("two expiry fields got %d, want 400", code)
	}

	// Shortening is allowed and does not count as an extension.
	doJSON(t, c, http.MethodPatch, url, map[string]any{"expires_in": 600}, &d)
	if time.Until(*d.ExpiresAt) > 601*time.Second || d.ExtendCount != 2 {
		t.Fatalf("shorten: expires %v, extend_count %d", d.ExpiresAt, d.ExtendCount)
	}
}

func TestLabelAndNoteEditing(t *testing.T) {
	srv, c := managedServer(t)
	tr := sessionUpload(t, c, srv, map[string]string{"a.txt": "x"}, "old note")
	url := srv.URL + "/api/manage/transfers/" + tr.Pin

	var d manageTransfer
	doJSON(t, c, http.MethodPatch, url, map[string]any{"label": "  Q3 build\x00 ", "text": "new note"}, &d)
	if d.Label != "Q3 build" || d.Text == nil || *d.Text != "new note" {
		t.Fatalf("label %q, text %v", d.Label, d.Text)
	}
	var pub TransferResponse
	doJSON(t, http.DefaultClient, http.MethodGet, srv.URL+"/api/transfer/"+tr.Pin, nil, &pub)
	if pub.Text != "new note" {
		t.Fatalf("recipient sees note %q", pub.Text)
	}

	// A text-only transfer cannot have its note removed; that would empty it.
	textOnly := sessionUpload(t, c, srv, nil, "just text")
	if code := doJSON(t, c, http.MethodPatch, srv.URL+"/api/manage/transfers/"+textOnly.Pin,
		map[string]any{"text": ""}, nil); code != http.StatusBadRequest {
		t.Fatalf("emptying a text-only transfer got %d, want 400", code)
	}
}

func TestRotatePINRevokesOldPIN(t *testing.T) {
	srv, c := managedServer(t)
	tr := sessionUpload(t, c, srv, map[string]string{"a.txt": "secret"}, "")

	var d manageTransfer
	if code := doJSON(t, c, http.MethodPost, srv.URL+"/api/manage/transfers/"+tr.Pin+"/rotate-pin", nil, &d); code != 200 {
		t.Fatalf("rotate got %d", code)
	}
	if d.Pin == tr.Pin || len(d.Pin) != pinLength {
		t.Fatalf("new PIN %q (old %q)", d.Pin, tr.Pin)
	}
	if code, _ := getBody(t, http.DefaultClient, tr.Files[0].DownloadURL); code != http.StatusNotFound {
		t.Fatalf("old PIN still downloads: %d", code)
	}
	if code, body := getBody(t, http.DefaultClient, d.Files[0].DownloadURL); code != 200 || body != "secret" {
		t.Fatalf("new PIN download: %d %q", code, body)
	}
	if _, err := os.Stat(transferDir(tr.Pin)); !os.IsNotExist(err) {
		t.Fatal("old transfer directory still exists")
	}
}

func TestAddAndDeleteFiles(t *testing.T) {
	srv, c := managedServer(t)
	tr := sessionUpload(t, c, srv, map[string]string{"a.txt": "one"}, "")
	base := srv.URL + "/api/manage/transfers/" + tr.Pin

	body, ct := multipartBody(t, map[string]string{"a.txt": "dupe", "b.txt": "two"}, "")
	req, _ := http.NewRequest(http.MethodPost, base+"/files", body)
	req.Header.Set("Content-Type", ct)
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var d manageTransfer
	json.NewDecoder(resp.Body).Decode(&d)
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated || d.FileCount != 3 {
		t.Fatalf("add files: %d, %d files", resp.StatusCode, d.FileCount)
	}
	names := map[string]string{}
	for _, f := range d.Files {
		_, content := getBody(t, http.DefaultClient, f.DownloadURL)
		names[f.Name] = content
	}
	if names["a.txt"] != "one" || names["a (2).txt"] != "dupe" || names["b.txt"] != "two" {
		t.Fatalf("files after add: %v", names)
	}

	for i, f := range d.Files {
		var out map[string]any
		code := doJSON(t, c, http.MethodDelete, fmt.Sprintf("%s/files/%d", base, f.ID), nil, &out)
		if code != 200 {
			t.Fatalf("delete file %d got %d", i, code)
		}
		last := i == len(d.Files)-1
		if deleted, _ := out["transfer_deleted"].(bool); deleted != last {
			t.Fatalf("after deleting file %d, transfer_deleted=%v", i, deleted)
		}
	}
	var count int
	db.QueryRow(`SELECT COUNT(*) FROM filesets WHERE pin = ?`, tr.Pin).Scan(&count)
	if count != 0 {
		t.Fatal("removing every file should delete the transfer")
	}
}

func TestManageDeleteTransfer(t *testing.T) {
	srv, c := managedServer(t)
	tr := sessionUpload(t, c, srv, map[string]string{"a.txt": "x"}, "")
	if code := doJSON(t, c, http.MethodDelete, srv.URL+"/api/manage/transfers/"+tr.Pin, nil, nil); code != http.StatusNoContent {
		t.Fatalf("delete got %d", code)
	}
	var list manageListResp
	doJSON(t, c, http.MethodGet, srv.URL+"/api/manage/transfers", nil, &list)
	if len(list.Transfers) != 0 {
		t.Fatal("deleted transfer still listed")
	}
	var events int
	db.QueryRow(`SELECT COUNT(*) FROM transfer_events`).Scan(&events)
	if events != 0 {
		t.Fatal("activity log should be deleted with the transfer")
	}
}

func TestManagePINsAreRedactedFromLogs(t *testing.T) {
	cases := map[string]string{
		"/api/manage/transfers/482107":         "/api/manage/transfers/***",
		"/api/manage/transfers/482107/files/3": "/api/manage/transfers/***/files/3",
		"/api/manage/transfers":                "/api/manage/transfers",
	}
	for in, want := range cases {
		if got := redactPIN(in); got != want {
			t.Fatalf("redactPIN(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPermanentUpload(t *testing.T) {
	srv, c := managedServer(t)
	allowPermanent = true

	body, ct := multipartBody(t, map[string]string{"keep.txt": "forever"}, "")
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/transfer?expiry=never", body)
	req.Header.Set("Content-Type", ct)
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var tr TransferResponse
	json.NewDecoder(resp.Body).Decode(&tr)
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("upload got %d", resp.StatusCode)
	}
	if !tr.NeverExpires || tr.ExpiresAt != nil {
		t.Fatalf("never_expires=%v expires_at=%v, want true/null", tr.NeverExpires, tr.ExpiresAt)
	}

	// The sweep must leave it alone, and it must still be fetchable.
	cleanupExpired()
	var got TransferResponse
	if code := doJSON(t, c, http.MethodGet, srv.URL+"/api/transfer/"+tr.Pin, nil, &got); code != 200 || !got.NeverExpires {
		t.Fatalf("lookup got %d never_expires=%v", code, got.NeverExpires)
	}

	var list struct {
		manageListResp
		Stats struct {
			Permanent  int        `json:"permanent"`
			NextExpiry *time.Time `json:"next_expiry"`
		} `json:"stats"`
	}
	doJSON(t, c, http.MethodGet, srv.URL+"/api/manage/transfers", nil, &list)
	if list.Stats.Permanent != 1 || list.Stats.NextExpiry != nil {
		t.Fatalf("stats permanent=%d next_expiry=%v", list.Stats.Permanent, list.Stats.NextExpiry)
	}

	// Giving it a deadline again: extend_secs counts from now, not from year 9999.
	url := srv.URL + "/api/manage/transfers/" + tr.Pin
	var d manageTransfer
	if code := doJSON(t, c, http.MethodPatch, url, map[string]any{"extend_secs": 3600}, &d); code != 200 {
		t.Fatalf("extend permanent got %d", code)
	}
	if d.NeverExpires || d.ExpiresAt == nil || time.Until(*d.ExpiresAt) > 3601*time.Second {
		t.Fatalf("after extend: never=%v expires=%v", d.NeverExpires, d.ExpiresAt)
	}

	if code := doJSON(t, c, http.MethodPatch, url, map[string]any{"never_expires": true}, &d); code != 200 {
		t.Fatalf("make permanent got %d", code)
	}
	if !d.NeverExpires || d.ExpiresAt != nil {
		t.Fatalf("after never_expires: never=%v expires=%v", d.NeverExpires, d.ExpiresAt)
	}
	if code := doJSON(t, c, http.MethodPatch, url, map[string]any{"never_expires": false}, nil); code != http.StatusBadRequest {
		t.Fatalf("never_expires=false got %d, want 400", code)
	}
	if code := doJSON(t, c, http.MethodPatch, url, map[string]any{"never_expires": true, "expires_in": 3600}, nil); code != http.StatusBadRequest {
		t.Fatalf("two expiry fields got %d, want 400", code)
	}
}

func TestPermanentUploadDisabled(t *testing.T) {
	srv, c := managedServer(t)
	allowPermanent = false

	body, ct := multipartBody(t, map[string]string{"a.txt": "x"}, "")
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/transfer?expiry=never", body)
	req.Header.Set("Content-Type", ct)
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("never upload got %d, want 403", resp.StatusCode)
	}

	tr := sessionUpload(t, c, srv, map[string]string{"a.txt": "x"}, "")
	if code := doJSON(t, c, http.MethodPatch, srv.URL+"/api/manage/transfers/"+tr.Pin,
		map[string]any{"never_expires": true}, nil); code != http.StatusForbidden {
		t.Fatalf("never_expires patch got %d, want 403", code)
	}
}
