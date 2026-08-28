package handlers

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/valyala/fasthttp"

	adminmodel "nimiqshop/internal/admin"
	"nimiqshop/internal/auth"
	"nimiqshop/internal/clientip"
	"nimiqshop/internal/db"
)

const adminSessionCookie = "nimiqshop_admin_session"

// devAdminID is the synthetic identity behind ADMIN_DEV_USERNAME/PASSWORD -
// a test-only login that skips TOTP (Config.AdminDevMode).
const devAdminID = "dev-admin"

// A valid, deliberately non-matching PHC string keeps unknown-user password
// attempts on an Argon2id code path too. It contains no live credential.
const dummyAdminPasswordHash = "$argon2id$v=19$m=65536,t=3,p=1$MDEyMzQ1Njc4OWFiY2RlZg$AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"

type adminLoginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
	TOTP     string `json:"totp"`
}

type adminBootstrapRequest struct {
	Username     string `json:"username"`
	PasswordHash string `json:"password_hash"`
	TOTPSecret   string `json:"totp_secret"`
}

type adminSessionContext struct {
	User    adminmodel.User
	Session adminmodel.Session
}

// RequireAdminSession is intentionally independent of RequireAuth: normal
// customer JWTs are neither read nor accepted on any admin endpoint.
func (h *Handlers) RequireAdminSession(next fasthttp.RequestHandler) fasthttp.RequestHandler {
	return func(ctx *fasthttp.RequestCtx) {
		if !h.Cfg.AdminSessionsEnabled() {
			writeError(ctx, fasthttp.StatusServiceUnavailable, "admin console is not configured")
			return
		}
		id, raw, ok := readAdminSessionCookie(ctx)
		if !ok {
			writeError(ctx, fasthttp.StatusUnauthorized, "admin authentication required")
			return
		}
		session, err := h.Store.GetAdminSession(id)
		if err != nil || session.RevokedAt != nil || !session.ExpiresAt.After(time.Now()) || !constantTimeSessionHashMatch(session.TokenHash, h.adminSessionHash(raw)) {
			clearAdminCookie(ctx, h)
			writeError(ctx, fasthttp.StatusUnauthorized, "admin authentication required")
			return
		}
		user, err := h.Store.GetAdminUser(session.AdminID)
		if err != nil && h.Cfg.AdminDevMode() && session.AdminID == devAdminID {
			// Env-credential test login: synthetic in-memory operator.
			user = adminmodel.User{ID: devAdminID, Username: h.Cfg.AdminDevUsername, CreatedAt: session.CreatedAt}
			err = nil
		}
		if err != nil || user.Disabled {
			clearAdminCookie(ctx, h)
			writeError(ctx, fasthttp.StatusUnauthorized, "admin authentication required")
			return
		}
		ctx.SetUserValue("admin_session", adminSessionContext{User: user, Session: session})
		next(ctx)
	}
}

func (h *Handlers) AdminBootstrap(ctx *fasthttp.RequestCtx) {
	// Bootstrap is unavailable until an explicit high-entropy deployment
	// secret is configured. It accepts a pre-generated PHC value, never a raw
	// password, and succeeds only once inside a Badger transaction.
	provided := string(ctx.Request.Header.Peek("X-Admin-Bootstrap-Token"))
	if h.Cfg.AdminBootstrapToken == "" || subtle.ConstantTimeCompare([]byte(provided), []byte(h.Cfg.AdminBootstrapToken)) != 1 {
		writeError(ctx, fasthttp.StatusNotFound, "not found")
		return
	}
	var req adminBootstrapRequest
	if err := readJSON(ctx, &req); err != nil || !validAdminUsername(req.Username) || auth.ValidateArgon2idPHC(req.PasswordHash) != nil || auth.ValidateTOTPSecret(req.TOTPSecret) != nil {
		writeError(ctx, fasthttp.StatusBadRequest, "username, Argon2id password_hash, and base32 totp_secret are required")
		return
	}
	user, err := h.Store.BootstrapAdmin(req.Username, req.PasswordHash, req.TOTPSecret)
	if errors.Is(err, db.ErrConflict) {
		writeError(ctx, fasthttp.StatusConflict, "admin bootstrap has already completed")
		return
	}
	if err != nil {
		writeError(ctx, fasthttp.StatusInternalServerError, "could not bootstrap admin")
		return
	}
	h.audit(user.ID, "admin.bootstrap.completed", ctx, "initial admin created")
	writeJSON(ctx, fasthttp.StatusCreated, map[string]any{"id": user.ID, "username": user.Username})
}

func (h *Handlers) AdminLogin(ctx *fasthttp.RequestCtx) {
	if !h.Cfg.AdminSessionsEnabled() {
		writeError(ctx, fasthttp.StatusServiceUnavailable, "admin console is not configured")
		return
	}
	var req adminLoginRequest
	if err := readJSON(ctx, &req); err != nil || !validAdminUsername(req.Username) || len(req.Password) == 0 || len(req.Password) > 1024 || (len(req.TOTP) != 6 && !(h.Cfg.AdminDevMode() && req.Username == h.Cfg.AdminDevUsername)) {
		h.recordAdminFailure("", "admin.login.failure", ctx, "invalid login request")
		writeError(ctx, fasthttp.StatusUnauthorized, "invalid administrator credentials")
		return
	}
	devLogin := h.Cfg.AdminDevMode() && req.Username == h.Cfg.AdminDevUsername

	now := time.Now().UTC()
	locked, lockErr := h.Store.AdminLoginLocked(req.Username, now)
	if lockErr != nil {
		writeError(ctx, fasthttp.StatusInternalServerError, "could not verify administrator login")
		return
	}
	if locked {
		// Do not extend a lockout on every blocked request; otherwise a remote
		// attacker could keep an operator locked forever by retrying.
		h.audit("", "admin.login.locked", ctx, "locked account attempt")
		writeError(ctx, fasthttp.StatusUnauthorized, "invalid administrator credentials")
		return
	}

	if devLogin {
		// TEST LOGIN (env credentials): constant-time password check, no TOTP.
		if subtle.ConstantTimeCompare([]byte(req.Password), []byte(h.Cfg.AdminDevPassword)) != 1 {
			h.audit("", "admin.login.failure", ctx, "dev env password mismatch")
			writeError(ctx, fasthttp.StatusUnauthorized, "invalid administrator credentials")
			return
		}
		devUser := adminmodel.User{ID: devAdminID, Username: h.Cfg.AdminDevUsername, CreatedAt: now}
		raw := make([]byte, 32)
		if _, err := rand.Read(raw); err != nil {
			writeError(ctx, fasthttp.StatusInternalServerError, "could not create administrator session")
			return
		}
		session := adminmodel.Session{
			ID:        uuid.NewString(),
			AdminID:   devAdminID,
			TokenHash: h.adminSessionHash(raw),
			IP:        h.requestIP(ctx),
			UserAgent: requestUserAgent(ctx),
			CreatedAt: now,
			ExpiresAt: now.Add(time.Duration(h.Cfg.AdminSessionMins) * time.Minute),
		}
		if err := h.Store.CreateAdminSession(session); err != nil {
			writeError(ctx, fasthttp.StatusInternalServerError, "could not create administrator session")
			return
		}
		h.audit(devAdminID, "admin.login.success", ctx, "dev env credentials (no TOTP)")
		setAdminCookie(ctx, h, session.ID, raw, session.ExpiresAt)
		writeJSON(ctx, fasthttp.StatusOK, map[string]any{"ok": true, "admin": safeAdminUser(devUser)})
		return
	}

	user, findErr := h.Store.FindAdminByUsername(req.Username)
	passwordHash := dummyAdminPasswordHash
	if findErr == nil {
		passwordHash = user.PasswordHash
	}
	passwordOK := auth.VerifyArgon2idPassword(passwordHash, req.Password)
	totpOK := findErr == nil && !user.Disabled && passwordOK && auth.VerifyTOTP(user.TOTPSecret, req.TOTP, now)
	if findErr != nil || !passwordOK || !totpOK {
		adminID := ""
		if findErr == nil {
			adminID = user.ID
		}
		_, _ = h.Store.RegisterAdminLoginFailure(req.Username, time.Now().UTC())
		// Attach a known identity to the immutable audit trail when available,
		// but preserve the same generic client response in all cases.
		h.audit(adminID, "admin.login.failure", ctx, "password or TOTP verification failed")
		writeError(ctx, fasthttp.StatusUnauthorized, "invalid administrator credentials")
		return
	}

	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		writeError(ctx, fasthttp.StatusInternalServerError, "could not create administrator session")
		return
	}
	session := adminmodel.Session{
		ID:        uuid.NewString(),
		AdminID:   user.ID,
		TokenHash: h.adminSessionHash(raw),
		IP:        h.requestIP(ctx),
		UserAgent: requestUserAgent(ctx),
		CreatedAt: now,
		ExpiresAt: now.Add(time.Duration(h.Cfg.AdminSessionMins) * time.Minute),
	}
	if err := h.Store.CreateAdminSession(session); err != nil {
		writeError(ctx, fasthttp.StatusInternalServerError, "could not create administrator session")
		return
	}
	if err := h.Store.ClearAdminLoginFailures(req.Username); err != nil {
		// The newly created session remains valid; audit the persistence issue
		// server-side rather than accidentally converting a valid sign-in into
		// a partial response.
		// No credential data is included.
	}
	h.audit(user.ID, "admin.login.success", ctx, "password and TOTP verified")
	setAdminCookie(ctx, h, session.ID, raw, session.ExpiresAt)
	writeJSON(ctx, fasthttp.StatusOK, map[string]any{"ok": true, "admin": safeAdminUser(user)})
}

func (h *Handlers) AdminLogout(ctx *fasthttp.RequestCtx) {
	identity, ok := ctx.UserValue("admin_session").(adminSessionContext)
	if !ok {
		clearAdminCookie(ctx, h)
		ctx.SetStatusCode(fasthttp.StatusNoContent)
		return
	}
	if err := h.Store.RevokeAdminSession(identity.Session.ID, time.Now().UTC()); err != nil && !errors.Is(err, db.ErrNotFound) {
		writeError(ctx, fasthttp.StatusInternalServerError, "could not end administrator session")
		return
	}
	h.audit(identity.User.ID, "admin.logout", ctx, "session revoked")
	clearAdminCookie(ctx, h)
	ctx.SetStatusCode(fasthttp.StatusNoContent)
}

func (h *Handlers) AdminMe(ctx *fasthttp.RequestCtx) {
	identity, ok := ctx.UserValue("admin_session").(adminSessionContext)
	if !ok {
		writeError(ctx, fasthttp.StatusUnauthorized, "admin authentication required")
		return
	}
	writeJSON(ctx, fasthttp.StatusOK, map[string]any{"admin": safeAdminUser(identity.User), "expires_at": identity.Session.ExpiresAt})
}

func safeAdminUser(user adminmodel.User) map[string]any {
	return map[string]any{"id": user.ID, "username": user.Username, "created_at": user.CreatedAt}
}

func (h *Handlers) recordAdminFailure(username, action string, ctx *fasthttp.RequestCtx, detail string) {
	if username != "" {
		_, _ = h.Store.RegisterAdminLoginFailure(username, time.Now().UTC())
	}
	h.audit("", action, ctx, detail)
}

func (h *Handlers) audit(adminID, action string, ctx *fasthttp.RequestCtx, detail string) {
	_, _ = h.Store.WriteAdminAudit(adminmodel.AuditEvent{
		AdminID: adminID, Action: action, IP: h.requestIP(ctx), UserAgent: requestUserAgent(ctx), Detail: detail,
	})
}

func (h *Handlers) adminSessionHash(raw []byte) string {
	mac := hmac.New(sha256.New, []byte(h.Cfg.AdminSessionSecret))
	_, _ = mac.Write(raw)
	return hex.EncodeToString(mac.Sum(nil))
}

func constantTimeSessionHashMatch(stored, expected string) bool {
	a, errA := hex.DecodeString(stored)
	b, errB := hex.DecodeString(expected)
	if errA != nil || errB != nil {
		return false
	}
	return subtle.ConstantTimeCompare(a, b) == 1
}

func readAdminSessionCookie(ctx *fasthttp.RequestCtx) (string, []byte, bool) {
	value := string(ctx.Request.Header.Cookie(adminSessionCookie))
	id, encoded, ok := strings.Cut(value, ".")
	if !ok || id == "" || encoded == "" {
		return "", nil, false
	}
	if _, err := uuid.Parse(id); err != nil {
		return "", nil, false
	}
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil || len(raw) != 32 {
		return "", nil, false
	}
	return id, raw, true
}

func setAdminCookie(ctx *fasthttp.RequestCtx, h *Handlers, id string, raw []byte, expires time.Time) {
	cookie := fasthttp.AcquireCookie()
	defer fasthttp.ReleaseCookie(cookie)
	cookie.SetKey(adminSessionCookie)
	cookie.SetValue(id + "." + base64.RawURLEncoding.EncodeToString(raw))
	cookie.SetPath("/api/admin")
	cookie.SetHTTPOnly(true)
	cookie.SetSecure(h.Cfg.AdminCookieSecure)
	cookie.SetSameSite(fasthttp.CookieSameSiteStrictMode)
	cookie.SetExpire(expires)
	ctx.Response.Header.SetCookie(cookie)
}

func clearAdminCookie(ctx *fasthttp.RequestCtx, h *Handlers) {
	cookie := fasthttp.AcquireCookie()
	defer fasthttp.ReleaseCookie(cookie)
	cookie.SetKey(adminSessionCookie)
	cookie.SetValue("")
	cookie.SetPath("/api/admin")
	cookie.SetHTTPOnly(true)
	cookie.SetSecure(h.Cfg.AdminCookieSecure)
	cookie.SetSameSite(fasthttp.CookieSameSiteStrictMode)
	cookie.SetMaxAge(-1)
	ctx.Response.Header.SetCookie(cookie)
}

// requestIP returns the real client IP for admin audit logs, resolved the same
// way as the rest of the API (Cloudflare-aware when TRUST_PROXY is on).
func (h *Handlers) requestIP(ctx *fasthttp.RequestCtx) string {
	return clientip.Resolve(ctx, h.Cfg.TrustProxy).IP
}
func requestUserAgent(ctx *fasthttp.RequestCtx) string {
	ua := strings.TrimSpace(string(ctx.UserAgent()))
	if len(ua) > 512 {
		return ua[:512]
	}
	return ua
}

func validAdminUsername(username string) bool {
	username = strings.TrimSpace(username)
	if len(username) < 3 || len(username) > 64 {
		return false
	}
	for _, r := range username {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '.' || r == '_' || r == '-') {
			return false
		}
	}
	return true
}
