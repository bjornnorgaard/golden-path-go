package auth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

// Provisioner creates or finds the user for a verified identity.
type Provisioner interface {
	ProvisionUser(ctx context.Context, email string, displayName, givenName, familyName, avatarURL *string) (Principal, error)
}

// GoogleConfig configures Google sign-in.
type GoogleConfig struct {
	ClientID     string
	ClientSecret string
	BaseURL      string // externally visible base URL, for the redirect URI
}

// LoginHandlers serves /login, /logout, /access-denied and the OAuth callback.
// Google is the only provider wired up; others are rejected exactly like an
// unrecognised provider, mirroring the ASP.NET reference.
type LoginHandlers struct {
	sessions *Sessions
	prov     Provisioner
	google   *GoogleConfig // nil when not configured
	log      *slog.Logger

	mu       sync.Mutex
	oauthCfg *oauth2.Config
	verifier *oidc.IDTokenVerifier
}

// NewLoginHandlers creates the login handlers. google may be nil.
func NewLoginHandlers(s *Sessions, p Provisioner, google *GoogleConfig, log *slog.Logger) *LoginHandlers {
	return &LoginHandlers{sessions: s, prov: p, google: google, log: log}
}

// Register mounts the handlers on mux.
func (h *LoginHandlers) Register(handle func(pattern string, handler http.HandlerFunc)) {
	handle("GET /login", h.login)
	handle("POST /logout", h.logout)
	handle("GET /access-denied", h.accessDenied)
	handle("GET /auth/callback/google", h.callback)
}

// init performs OIDC discovery lazily so startup never depends on Google being
// reachable. A failure is not cached: the next request retries.
func (h *LoginHandlers) init(ctx context.Context) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.oauthCfg != nil {
		return nil
	}
	provider, err := oidc.NewProvider(ctx, "https://accounts.google.com")
	if err != nil {
		return err
	}
	h.oauthCfg = &oauth2.Config{
		ClientID: h.google.ClientID, ClientSecret: h.google.ClientSecret, Endpoint: provider.Endpoint(),
		RedirectURL: strings.TrimRight(h.google.BaseURL, "/") + "/auth/callback/google",
		Scopes:      []string{oidc.ScopeOpenID, "profile", "email"},
	}
	h.verifier = provider.Verifier(&oidc.Config{ClientID: h.google.ClientID})
	return nil
}

func (h *LoginHandlers) login(w http.ResponseWriter, r *http.Request) {
	provider := r.URL.Query().Get("provider")
	if (provider != "" && provider != "google") || h.google == nil {
		http.Error(w, "unknown or disabled sign-in provider", http.StatusBadRequest)
		return
	}
	if err := h.init(r.Context()); err != nil {
		h.log.ErrorContext(r.Context(), "google oidc discovery failed", "error", err)
		http.Error(w, "sign-in is temporarily unavailable", http.StatusBadGateway)
		return
	}
	state := randomToken()
	http.SetCookie(w, &http.Cookie{Name: "gp_oauth_state", Value: state + "|" + safeReturnURL(r.URL.Query().Get("returnUrl")), //nolint:gosec // Secure follows the AUTH_PUBLIC_BASE_URL scheme so plain-http local development works
		Path: "/auth/callback/google", HttpOnly: true, Secure: h.sessions.secure, SameSite: http.SameSiteLaxMode, MaxAge: 600})
	http.Redirect(w, r, h.oauthCfg.AuthCodeURL(state), http.StatusFound)
}

func (h *LoginHandlers) callback(w http.ResponseWriter, r *http.Request) {
	if h.google == nil {
		http.Error(w, "google sign-in is not enabled", http.StatusBadRequest)
		return
	}
	c, err := r.Cookie("gp_oauth_state")
	if err != nil {
		http.Redirect(w, r, "/access-denied", http.StatusFound)
		return
	}
	state, returnURL, _ := strings.Cut(c.Value, "|")
	if state == "" || state != r.URL.Query().Get("state") {
		http.Redirect(w, r, "/access-denied", http.StatusFound)
		return
	}
	if err := h.init(r.Context()); err != nil {
		http.Error(w, "sign-in is temporarily unavailable", http.StatusBadGateway)
		return
	}
	principal, err := h.exchange(r.Context(), r.URL.Query().Get("code"))
	if err != nil {
		h.log.WarnContext(r.Context(), "google sign-in rejected", "error", err)
		http.Redirect(w, r, "/access-denied", http.StatusFound)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: "gp_oauth_state", Path: "/auth/callback/google", HttpOnly: true, Secure: h.sessions.secure, SameSite: http.SameSiteLaxMode, MaxAge: -1}) //nolint:gosec // Secure follows the AUTH_PUBLIC_BASE_URL scheme so plain-http local development works
	h.sessions.Issue(w, principal)
	http.Redirect(w, r, safeReturnURL(returnURL), http.StatusFound) //nolint:gosec // safeReturnURL only allows local paths
}

func (h *LoginHandlers) exchange(ctx context.Context, code string) (Principal, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	tok, err := h.oauthCfg.Exchange(ctx, code)
	if err != nil {
		return Principal{}, err
	}
	raw, _ := tok.Extra("id_token").(string)
	idToken, err := h.verifier.Verify(ctx, raw)
	if err != nil {
		return Principal{}, err
	}
	var claims struct {
		Email         string `json:"email"`
		EmailVerified bool   `json:"email_verified"`
		Name          string `json:"name"`
		GivenName     string `json:"given_name"`
		FamilyName    string `json:"family_name"`
		Picture       string `json:"picture"`
	}
	if err := idToken.Claims(&claims); err != nil {
		return Principal{}, err
	}
	if claims.Email == "" || !claims.EmailVerified {
		return Principal{}, errors.New("identity has no verified email")
	}
	return h.prov.ProvisionUser(ctx, claims.Email, nonEmpty(claims.Name), nonEmpty(claims.GivenName), nonEmpty(claims.FamilyName), nonEmpty(claims.Picture))
}

func (h *LoginHandlers) logout(w http.ResponseWriter, _ *http.Request) {
	h.sessions.Clear(w)
	w.WriteHeader(http.StatusNoContent)
}

func (h *LoginHandlers) accessDenied(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusForbidden)
	_, _ = fmt.Fprint(w, "Access denied.")
}

func randomToken() string {
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// safeReturnURL only allows local paths, preventing open redirects.
func safeReturnURL(u string) string {
	if u == "" || !strings.HasPrefix(u, "/") || strings.HasPrefix(u, "//") || strings.HasPrefix(u, "/\\") {
		return "/"
	}
	if parsed, err := url.Parse(u); err != nil || parsed.Host != "" {
		return "/"
	}
	return u
}

func nonEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
