package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Uploads need a login; downloads only ever need a PIN.
//
// Scripts authenticate with the X-API-Key header as before. The web UI logs in
// once with UPLOAD_PASSWORD (or the API key itself when no password is set) and
// gets a signed session cookie, so nobody has to paste the API key into a
// browser. Sessions are stateless: an HMAC over the expiry, keyed from the
// master key and the login secret, so rotating either one logs everyone out.

const (
	sessionCookieName = "sfx_session"
	sessionTTL        = 30 * 24 * time.Hour
)

var (
	uploadPassword string
	loginLimiter   *rateLimiter
)

// authRequired reports whether write routes are gated at all.
func authRequired() bool { return apiKey != "" || uploadPassword != "" }

// loginSecret is what the login form must match.
func loginSecret() string {
	if uploadPassword != "" {
		return uploadPassword
	}
	return apiKey
}

// secretsEqual compares SHA-256 digests so the comparison is constant-time in
// both content and length.
func secretsEqual(a, b string) bool {
	x := sha256.Sum256([]byte(a))
	y := sha256.Sum256([]byte(b))
	return subtle.ConstantTimeCompare(x[:], y[:]) == 1
}

func sessionKey() []byte {
	secretHash := sha256.Sum256([]byte(loginSecret()))
	mac := hmac.New(sha256.New, masterKey)
	mac.Write([]byte("shadowfax-session-v1|"))
	mac.Write(secretHash[:])
	return mac.Sum(nil)
}

func signSession(expires time.Time) string {
	exp := strconv.FormatInt(expires.Unix(), 10)
	mac := hmac.New(sha256.New, sessionKey())
	mac.Write([]byte(exp))
	return exp + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func validSession(r *http.Request) bool {
	if loginSecret() == "" {
		return false
	}
	c, err := r.Cookie(sessionCookieName)
	if err != nil {
		return false
	}
	exp, sig, ok := strings.Cut(c.Value, ".")
	if !ok {
		return false
	}
	expUnix, err := strconv.ParseInt(exp, 10, 64)
	if err != nil || time.Now().Unix() >= expUnix {
		return false
	}
	got, err := base64.RawURLEncoding.DecodeString(sig)
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, sessionKey())
	mac.Write([]byte(exp))
	return hmac.Equal(got, mac.Sum(nil))
}

// sameOrigin backs up SameSite=Strict for cookie-authenticated writes: a
// browser request carrying an Origin header must come from this site.
func sameOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	return origin == "" || strings.EqualFold(origin, baseURL(r))
}

func setSessionCookie(w http.ResponseWriter, r *http.Request, value string, maxAge int) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    value,
		Path:     "/",
		MaxAge:   maxAge,
		HttpOnly: true,
		Secure:   strings.HasPrefix(baseURL(r), "https://"),
		SameSite: http.SameSiteStrictMode,
	})
}

// requireUploadAuth gates write routes: a valid X-API-Key header or a
// same-origin request carrying a valid session cookie.
func requireUploadAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !authRequired() {
			next.ServeHTTP(w, r)
			return
		}
		if apiKey != "" {
			provided := r.Header.Get("X-API-Key")
			if provided == "" {
				provided = strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			}
			if provided != "" && secretsEqual(strings.TrimSpace(provided), apiKey) {
				next.ServeHTTP(w, r)
				return
			}
		}
		if validSession(r) {
			if !sameOrigin(r) {
				writeError(w, http.StatusForbidden, "cross-origin request refused")
				return
			}
			next.ServeHTTP(w, r)
			return
		}
		writeError(w, http.StatusUnauthorized,
			"login required: sign in on the web UI, or send header 'X-API-Key: <your key>'")
	})
}

// POST /api/login {"password": "..."}
func handleLogin(w http.ResponseWriter, r *http.Request) {
	if !authRequired() {
		writeError(w, http.StatusBadRequest, "login is not enabled on this server")
		return
	}
	if !sameOrigin(r) {
		writeError(w, http.StatusForbidden, "cross-origin request refused")
		return
	}
	var body struct {
		Password string `json:"password"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "expected JSON body: {\"password\": \"...\"}")
		return
	}

	ip := clientIP(r)
	if body.Password == "" || !secretsEqual(body.Password, loginSecret()) {
		loginLimiter.recordFailure(ip)
		writeError(w, http.StatusUnauthorized, "wrong password")
		return
	}
	loginLimiter.recordSuccess(ip)

	setSessionCookie(w, r, signSession(time.Now().Add(sessionTTL)), int(sessionTTL.Seconds()))
	writeJSON(w, http.StatusOK, map[string]any{"authenticated": true})
}

// POST /api/logout
func handleLogout(w http.ResponseWriter, r *http.Request) {
	setSessionCookie(w, r, "", -1)
	writeJSON(w, http.StatusOK, map[string]any{"authenticated": false})
}

// GET /api/session tells the UI whether to show the uploader or the login form.
func handleSession(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"login_required": authRequired(),
		"authenticated":  !authRequired() || validSession(r),
	})
}
