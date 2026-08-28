package db

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/dgraph-io/badger/v4"
	"github.com/google/uuid"

	adminmodel "nimiqshop/internal/admin"
)

const (
	adminUserPrefix       = "au:"
	adminSessionPrefix    = "as:"
	adminAuditPrefix      = "ae:"
	adminAuditIndexPrefix = "ix:ae:all:"
	adminBootstrapKey     = "meta:admin:bootstrapped"
	adminSettingsKey      = "meta:admin:settings"
	adminFailurePrefix    = "alf:"
)

func adminUserKey(id string) []byte { return []byte(adminUserPrefix + id) }
func adminUsernameKey(username string) []byte {
	return []byte("ix:au:username:" + canonicalAdminUsername(username))
}
func adminSessionKey(id string) []byte { return []byte(adminSessionPrefix + id) }
func adminFailureKey(username string) []byte {
	return []byte(adminFailurePrefix + canonicalAdminUsername(username))
}
func adminAuditKey(id string) []byte { return []byte(adminAuditPrefix + id) }
func adminAuditIndexKey(created int64, id string) []byte {
	return []byte(adminAuditIndexPrefix + reverseTS(created) + ":" + id)
}

// CanonicalAdminUsername provides case-insensitive operator logins without
// changing the display form stored in User.Username.
func canonicalAdminUsername(username string) string {
	return strings.ToLower(strings.TrimSpace(username))
}

// CreateAdminUser creates an operations identity and a unique canonical
// username index. Password/TOTP input validation intentionally lives in the
// auth/config boundary; this store accepts only already validated material.
func (s *Store) CreateAdminUser(u adminmodel.User) (adminmodel.User, error) {
	if canonicalAdminUsername(u.Username) == "" || u.PasswordHash == "" || u.TOTPSecret == "" {
		return adminmodel.User{}, fmt.Errorf("admin identity is incomplete")
	}
	if u.ID == "" {
		u.ID = uuid.NewString()
	}
	if u.CreatedAt.IsZero() {
		u.CreatedAt = time.Now().UTC()
	}

	err := s.Update(func(txn *badger.Txn) error {
		usernameKey := adminUsernameKey(u.Username)
		if _, err := txn.Get(usernameKey); err == nil {
			return ErrConflict
		} else if !errors.Is(err, badger.ErrKeyNotFound) {
			return err
		}
		blob, err := marshal(u)
		if err != nil {
			return err
		}
		if err := txn.Set(adminUserKey(u.ID), blob); err != nil {
			return err
		}
		return txn.Set(usernameKey, []byte(u.ID))
	})
	return u, err
}

// BootstrapAdmin atomically creates the first administrator exactly once.
// A bootstrap marker means an accidentally deleted user record cannot silently
// reopen the one-time bootstrap path.
func (s *Store) BootstrapAdmin(username, passwordHash, totpSecret string) (adminmodel.User, error) {
	if canonicalAdminUsername(username) == "" || passwordHash == "" || totpSecret == "" {
		return adminmodel.User{}, fmt.Errorf("admin bootstrap is incomplete")
	}
	u := adminmodel.User{
		ID:           uuid.NewString(),
		Username:     strings.TrimSpace(username),
		PasswordHash: passwordHash,
		TOTPSecret:   totpSecret,
		CreatedAt:    time.Now().UTC(),
	}
	err := s.Update(func(txn *badger.Txn) error {
		if _, err := txn.Get([]byte(adminBootstrapKey)); err == nil {
			return ErrConflict
		} else if !errors.Is(err, badger.ErrKeyNotFound) {
			return err
		}
		usernameKey := adminUsernameKey(u.Username)
		if _, err := txn.Get(usernameKey); err == nil {
			return ErrConflict
		} else if !errors.Is(err, badger.ErrKeyNotFound) {
			return err
		}
		blob, err := marshal(u)
		if err != nil {
			return err
		}
		if err := txn.Set(adminUserKey(u.ID), blob); err != nil {
			return err
		}
		if err := txn.Set(usernameKey, []byte(u.ID)); err != nil {
			return err
		}
		return txn.Set([]byte(adminBootstrapKey), []byte(u.ID))
	})
	return u, err
}

func (s *Store) GetAdminUser(id string) (adminmodel.User, error) {
	var u adminmodel.User
	err := s.View(func(txn *badger.Txn) error { return getJSON(txn, adminUserKey(id), &u) })
	return u, err
}

func (s *Store) FindAdminByUsername(username string) (adminmodel.User, error) {
	var u adminmodel.User
	err := s.View(func(txn *badger.Txn) error {
		id, err := getString(txn, adminUsernameKey(username))
		if err != nil {
			return err
		}
		return getJSON(txn, adminUserKey(id), &u)
	})
	return u, err
}

// CreateAdminSession persists a session credential hash. The caller owns
// generating the random raw credential and must never write it to Badger.
func (s *Store) CreateAdminSession(session adminmodel.Session) error {
	if session.ID == "" || session.AdminID == "" || session.TokenHash == "" || session.ExpiresAt.Before(time.Now()) {
		return fmt.Errorf("invalid admin session")
	}
	if session.CreatedAt.IsZero() {
		session.CreatedAt = time.Now().UTC()
	}
	return s.Update(func(txn *badger.Txn) error {
		if _, err := txn.Get(adminSessionKey(session.ID)); err == nil {
			return ErrConflict
		} else if !errors.Is(err, badger.ErrKeyNotFound) {
			return err
		}
		blob, err := marshal(session)
		if err != nil {
			return err
		}
		return txn.Set(adminSessionKey(session.ID), blob)
	})
}

func (s *Store) GetAdminSession(id string) (adminmodel.Session, error) {
	var session adminmodel.Session
	err := s.View(func(txn *badger.Txn) error { return getJSON(txn, adminSessionKey(id), &session) })
	return session, err
}

// RevokeAdminSession makes a session unusable immediately while retaining an
// auditably useful record of its original IP, user agent, and expiry.
func (s *Store) RevokeAdminSession(id string, now time.Time) error {
	return s.Update(func(txn *badger.Txn) error {
		var session adminmodel.Session
		if err := getJSON(txn, adminSessionKey(id), &session); err != nil {
			return err
		}
		if session.RevokedAt != nil {
			return nil
		}
		now = now.UTC()
		session.RevokedAt = &now
		blob, err := marshal(session)
		if err != nil {
			return err
		}
		return txn.Set(adminSessionKey(id), blob)
	})
}

// WriteAdminAudit appends an immutable audit event. There is intentionally no
// update/delete method for audit records.
func (s *Store) WriteAdminAudit(event adminmodel.AuditEvent) (adminmodel.AuditEvent, error) {
	if event.Action == "" {
		return adminmodel.AuditEvent{}, fmt.Errorf("audit action is required")
	}
	if event.ID == "" {
		event.ID = uuid.NewString()
	}
	if event.CreatedAt.IsZero() {
		event.CreatedAt = time.Now().UTC()
	}
	err := s.Update(func(txn *badger.Txn) error {
		blob, err := marshal(event)
		if err != nil {
			return err
		}
		if err := txn.Set(adminAuditKey(event.ID), blob); err != nil {
			return err
		}
		return txn.Set(adminAuditIndexKey(event.CreatedAt.UnixNano(), event.ID), []byte(event.ID))
	})
	return event, err
}

func (s *Store) ListAdminAudit(limit int) ([]adminmodel.AuditEvent, error) {
	out := make([]adminmodel.AuditEvent, 0)
	err := s.View(func(txn *badger.Txn) error {
		return scanIndex(txn, []byte(adminAuditIndexPrefix), limit, func(id string) error {
			var event adminmodel.AuditEvent
			if err := getJSON(txn, adminAuditKey(id), &event); err != nil {
				if errors.Is(err, ErrNotFound) {
					return nil
				}
				return err
			}
			out = append(out, event)
			return nil
		})
	})
	return out, err
}

// RegisterAdminLoginFailure records a private failure window. Five failures
// in 15 minutes lock that username for 30 minutes. Every call is transactional
// so concurrent attempts cannot skip the lockout threshold.
func (s *Store) RegisterAdminLoginFailure(username string, now time.Time) (adminmodel.LoginFailure, error) {
	var failure adminmodel.LoginFailure
	now = now.UTC()
	err := s.Update(func(txn *badger.Txn) error {
		key := adminFailureKey(username)
		err := getJSON(txn, key, &failure)
		if errors.Is(err, ErrNotFound) || now.Sub(failure.FirstFailedAt) > 15*time.Minute {
			failure = adminmodel.LoginFailure{Username: canonicalAdminUsername(username), FailedCount: 0, FirstFailedAt: now}
		} else if err != nil {
			return err
		}
		failure.FailedCount++
		failure.LastFailedAt = now
		if failure.FailedCount >= 5 {
			until := now.Add(30 * time.Minute)
			failure.LockedUntil = &until
		}
		blob, err := marshal(failure)
		if err != nil {
			return err
		}
		return txn.Set(key, blob)
	})
	return failure, err
}

func (s *Store) AdminLoginLocked(username string, now time.Time) (bool, error) {
	locked := false
	err := s.View(func(txn *badger.Txn) error {
		var failure adminmodel.LoginFailure
		err := getJSON(txn, adminFailureKey(username), &failure)
		if errors.Is(err, ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		locked = failure.LockedUntil != nil && failure.LockedUntil.After(now)
		return nil
	})
	return locked, err
}

func (s *Store) ClearAdminLoginFailures(username string) error {
	return s.Update(func(txn *badger.Txn) error {
		return txn.Delete(adminFailureKey(username))
	})
}

func (s *Store) GetAdminSettings(defaultMarginBps int) (adminmodel.Settings, error) {
	settings := adminmodel.Settings{GlobalMarginBps: defaultMarginBps}
	err := s.View(func(txn *badger.Txn) error {
		err := getJSON(txn, []byte(adminSettingsKey), &settings)
		if errors.Is(err, ErrNotFound) {
			return nil
		}
		return err
	})
	return settings, err
}

func (s *Store) SetGlobalMarginBps(marginBps int, adminID string, now time.Time) (adminmodel.Settings, error) {
	if marginBps < 0 || marginBps > 5000 {
		return adminmodel.Settings{}, fmt.Errorf("margin must be between 0 and 5000 basis points")
	}
	settings := adminmodel.Settings{GlobalMarginBps: marginBps, UpdatedAt: now.UTC(), UpdatedBy: adminID}
	err := s.Update(func(txn *badger.Txn) error {
		blob, err := marshal(settings)
		if err != nil {
			return err
		}
		return txn.Set([]byte(adminSettingsKey), blob)
	})
	return settings, err
}
