package main

import (
	"encoding/json"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func newJarClient(t *testing.T) *http.Client {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	return &http.Client{Jar: jar}
}

func login(t *testing.T, c *http.Client, srv *httptest.Server, password string) int {
	t.Helper()
	resp, err := c.Post(srv.URL+"/api/login", "application/json",
		strings.NewReader(`{"password":`+jsonString(password)+`}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func rawUpload(t *testing.T, c *http.Client, srv *httptest.Server, origin string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPut, srv.URL+"/api/upload/x.txt", strings.NewReader("hi"))
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestLoginGatesUploadsButNotDownloads(t *testing.T) {
	srv := newTestServer(t, func() { uploadPassword = "hunter2-long-enough" })
	c := newJarClient(t)

	if resp := rawUpload(t, c, srv, ""); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("upload before login got %d, want 401", resp.StatusCode)
	}
	if got := login(t, c, srv, "wrong"); got != http.StatusUnauthorized {
		t.Fatalf("wrong password got %d, want 401", got)
	}
	if got := login(t, c, srv, "hunter2-long-enough"); got != http.StatusOK {
		t.Fatalf("login got %d, want 200", got)
	}

	resp := rawUpload(t, c, srv, srv.URL)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("upload after login got %d, want 201", resp.StatusCode)
	}
	var tr TransferResponse
	json.NewDecoder(resp.Body).Decode(&tr)

	// A recipient with nothing but the PIN can still download.
	dl, err := http.Get(tr.Files[0].DownloadURL)
	if err != nil {
		t.Fatal(err)
	}
	dl.Body.Close()
	if dl.StatusCode != http.StatusOK {
		t.Fatalf("anonymous download got %d, want 200", dl.StatusCode)
	}

	// Logging out drops the cookie.
	out, _ := c.Post(srv.URL+"/api/logout", "application/json", nil)
	out.Body.Close()
	if resp := rawUpload(t, c, srv, ""); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("upload after logout got %d, want 401", resp.StatusCode)
	}
}

func TestSessionRefusesCrossOriginWrites(t *testing.T) {
	srv := newTestServer(t, func() { uploadPassword = "hunter2-long-enough" })
	c := newJarClient(t)
	login(t, c, srv, "hunter2-long-enough")

	resp := rawUpload(t, c, srv, "https://evil.example")
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-origin upload with session got %d, want 403", resp.StatusCode)
	}
}

func TestForgedOrStaleSessionsAreRejected(t *testing.T) {
	srv := newTestServer(t, func() { uploadPassword = "hunter2-long-enough" })

	for name, value := range map[string]string{
		"garbage":       "nope",
		"bad signature": signSession(time.Now().Add(time.Hour))[:12] + "AAAA",
		"expired":       signSession(time.Now().Add(-time.Minute)),
	} {
		req, _ := http.NewRequest(http.MethodPut, srv.URL+"/api/upload/x.txt", strings.NewReader("hi"))
		req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: value})
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s session got %d, want 401", name, resp.StatusCode)
		}
	}

	// Changing the password invalidates sessions signed under the old one.
	old := signSession(time.Now().Add(time.Hour))
	uploadPassword = "a-different-password"
	req, _ := http.NewRequest(http.MethodPut, srv.URL+"/api/upload/x.txt", strings.NewReader("hi"))
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: old})
	resp, _ := http.DefaultClient.Do(req)
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("session from before a password change got %d, want 401", resp.StatusCode)
	}
}

func TestAPIKeyDoublesAsLoginWithoutPassword(t *testing.T) {
	srv := newTestServer(t, func() { apiKey = "s3cret-api-key" })
	t.Cleanup(func() { apiKey = "" })
	c := newJarClient(t)

	if got := login(t, c, srv, "s3cret-api-key"); got != http.StatusOK {
		t.Fatalf("login with API key got %d, want 200", got)
	}
	resp := rawUpload(t, c, srv, "")
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("upload with session got %d, want 201", resp.StatusCode)
	}
}

func TestWrongPasswordsTriggerLoginLockout(t *testing.T) {
	srv := newTestServer(t, func() { uploadPassword = "hunter2-long-enough" })
	c := newJarClient(t)

	for i := 0; i < 5; i++ {
		login(t, c, srv, "wrong")
	}
	if got := login(t, c, srv, "hunter2-long-enough"); got != http.StatusTooManyRequests {
		t.Fatalf("correct password during lockout got %d, want 429", got)
	}
}

func TestSessionEndpointReportsState(t *testing.T) {
	srv := newTestServer(t, func() { uploadPassword = "hunter2-long-enough" })
	c := newJarClient(t)

	state := func() (bool, bool) {
		resp, err := c.Get(srv.URL + "/api/session")
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var s struct {
			LoginRequired bool `json:"login_required"`
			Authenticated bool `json:"authenticated"`
		}
		json.NewDecoder(resp.Body).Decode(&s)
		return s.LoginRequired, s.Authenticated
	}

	if req, authed := state(); !req || authed {
		t.Fatalf("before login: login_required=%v authenticated=%v", req, authed)
	}
	login(t, c, srv, "hunter2-long-enough")
	if _, authed := state(); !authed {
		t.Fatal("after login: authenticated=false")
	}
}

func TestClientIPUsesRightmostForwardedFor(t *testing.T) {
	trustProxy = true
	t.Cleanup(func() { trustProxy = false })

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("X-Forwarded-For", "1.2.3.4, 203.0.113.9")
	if got := clientIP(r); got != "203.0.113.9" {
		t.Fatalf("clientIP = %q, want the proxy-appended 203.0.113.9", got)
	}
}
