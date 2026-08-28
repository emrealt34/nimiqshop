package db

import (
	"errors"
	"testing"
	"time"

	adminmodel "nimiqshop/internal/admin"
)

func TestAdminBootstrapSessionAuditAndRevocation(t *testing.T) {
	store := newTestStore(t)
	user, err := store.BootstrapAdmin("Operator", "argon2id-phc", "BASE32SECRET")
	if err != nil {
		t.Fatal(err)
	}
	if user.Username != "Operator" {
		t.Fatalf("wrong bootstrap user: %+v", user)
	}
	if _, err := store.BootstrapAdmin("second", "hash", "secret"); !errors.Is(err, ErrConflict) {
		t.Fatalf("second bootstrap error = %v, want ErrConflict", err)
	}
	found, err := store.FindAdminByUsername("operator")
	if err != nil || found.ID != user.ID {
		t.Fatalf("case-insensitive admin lookup = %+v, %v", found, err)
	}

	now := time.Now().UTC()
	session := adminmodel.Session{ID: "session-1", AdminID: user.ID, TokenHash: "hmac", CreatedAt: now, ExpiresAt: now.Add(time.Hour), IP: "127.0.0.1", UserAgent: "test"}
	if err := store.CreateAdminSession(session); err != nil {
		t.Fatal(err)
	}
	if err := store.RevokeAdminSession(session.ID, now); err != nil {
		t.Fatal(err)
	}
	revoked, err := store.GetAdminSession(session.ID)
	if err != nil || revoked.RevokedAt == nil {
		t.Fatalf("session revocation was not persisted: %+v, %v", revoked, err)
	}

	if _, err := store.WriteAdminAudit(adminmodel.AuditEvent{AdminID: user.ID, Action: "admin.login.success", IP: "127.0.0.1", UserAgent: "test"}); err != nil {
		t.Fatal(err)
	}
	audit, err := store.ListAdminAudit(10)
	if err != nil || len(audit) != 1 || audit[0].Action != "admin.login.success" {
		t.Fatalf("audit list = %+v, %v", audit, err)
	}
}

func TestAdminLoginFailureLocksAfterFiveAttempts(t *testing.T) {
	store := newTestStore(t)
	now := time.Now().UTC()
	for i := 0; i < 5; i++ {
		failure, err := store.RegisterAdminLoginFailure("operator", now.Add(time.Duration(i)*time.Second))
		if err != nil {
			t.Fatal(err)
		}
		if i < 4 && failure.LockedUntil != nil {
			t.Fatalf("attempt %d locked too early", i+1)
		}
	}
	locked, err := store.AdminLoginLocked("operator", now.Add(6*time.Second))
	if err != nil || !locked {
		t.Fatalf("locked = %v, err = %v", locked, err)
	}
	if err := store.ClearAdminLoginFailures("operator"); err != nil {
		t.Fatal(err)
	}
	locked, err = store.AdminLoginLocked("operator", now.Add(6*time.Second))
	if err != nil || locked {
		t.Fatalf("clear failures left account locked = %v, err = %v", locked, err)
	}
}

func TestGlobalMarginIsPersistent(t *testing.T) {
	store := newTestStore(t)
	initial, err := store.GetAdminSettings(500)
	if err != nil || initial.GlobalMarginBps != 500 {
		t.Fatalf("initial settings = %+v, %v", initial, err)
	}
	updated, err := store.SetGlobalMarginBps(725, "operator-id", time.Now())
	if err != nil || updated.GlobalMarginBps != 725 {
		t.Fatalf("updated settings = %+v, %v", updated, err)
	}
	persisted, err := store.GetAdminSettings(500)
	if err != nil || persisted.GlobalMarginBps != 725 || persisted.UpdatedBy != "operator-id" {
		t.Fatalf("persisted settings = %+v, %v", persisted, err)
	}
}
