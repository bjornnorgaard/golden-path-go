package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/bjornnorgaard/golden-path-go/internal/id"
)

// CookieName is the session cookie. It must match the securityScheme in the OpenAPI contracts.
const CookieName = "gp_session"

// Sessions issues and verifies signed, stateless session cookies
// (base64url(JSON payload) "." base64url(HMAC-SHA256)).
type Sessions struct {
	key    []byte
	ttl    time.Duration
	secure bool
	now    func() time.Time
}

// NewSessions creates a session codec. secure marks cookies Secure (use with https).
func NewSessions(secret string, ttl time.Duration, secure bool) *Sessions {
	return &Sessions{key: []byte(secret), ttl: ttl, secure: secure, now: time.Now}
}

type payload struct {
	UserID  id.UserID `json:"uid"`
	Email   string    `json:"email"`
	Expires int64     `json:"exp"`
}

// Encode returns the cookie value for p.
func (s *Sessions) Encode(p Principal) string {
	body, _ := json.Marshal(payload{UserID: p.UserID, Email: p.Email, Expires: s.now().Add(s.ttl).Unix()})
	enc := base64.RawURLEncoding.EncodeToString(body)
	return enc + "." + s.sign(enc)
}

// Issue sets the session cookie on the response.
func (s *Sessions) Issue(w http.ResponseWriter, p Principal) {
	http.SetCookie(w, &http.Cookie{ //nolint:gosec // Secure follows the scheme of AUTH_PUBLIC_BASE_URL so plain-http local development works
		Name: CookieName, Value: s.Encode(p), Path: "/", HttpOnly: true, Secure: s.secure,
		SameSite: http.SameSiteLaxMode, MaxAge: int(s.ttl.Seconds()),
	})
}

// Clear expires the session cookie.
func (s *Sessions) Clear(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{Name: CookieName, Value: "", Path: "/", HttpOnly: true, Secure: s.secure, SameSite: http.SameSiteLaxMode, MaxAge: -1}) //nolint:gosec // Secure follows the AUTH_PUBLIC_BASE_URL scheme so plain-http local development works
}

// Authenticate implements Authenticator.
func (s *Sessions) Authenticate(r *http.Request) (Principal, bool) {
	c, err := r.Cookie(CookieName)
	if err != nil {
		return Principal{}, false
	}
	enc, sig, ok := strings.Cut(c.Value, ".")
	if !ok || !hmac.Equal([]byte(sig), []byte(s.sign(enc))) {
		return Principal{}, false
	}
	body, err := base64.RawURLEncoding.DecodeString(enc)
	if err != nil {
		return Principal{}, false
	}
	var p payload
	if err := json.Unmarshal(body, &p); err != nil || s.now().Unix() >= p.Expires {
		return Principal{}, false
	}
	return Principal{UserID: p.UserID, Email: p.Email}, true
}

func (s *Sessions) sign(enc string) string {
	m := hmac.New(sha256.New, s.key)
	m.Write([]byte(enc))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}
