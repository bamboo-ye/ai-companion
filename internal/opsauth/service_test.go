package opsauth

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestServicePreventsLastActiveAdminLockout(t *testing.T) {
	now := time.Now().UTC()
	admin, err := BootstrapAccount("admin-1", "Admin One", "admin", "admin-token", "", false, now)
	if err != nil {
		t.Fatal(err)
	}
	service := NewService(NewMemoryStore(admin))

	if _, err = service.Disable(context.Background(), "admin-1", "admin-1", "should be blocked"); !errors.Is(err, ErrAdminLockout) || !errors.Is(err, ErrForbidden) {
		t.Fatalf("Disable last admin error = %v, want ErrAdminLockout wrapping ErrForbidden", err)
	}
}

func TestServicePreventsLastActiveMFAAdminLockout(t *testing.T) {
	now := time.Now().UTC()
	secret := "JBSWY3DPEHPK3PXP"
	mfaAdmin, err := BootstrapAccount("admin-mfa", "Admin MFA", "admin", "admin-mfa-token", secret, true, now)
	if err != nil {
		t.Fatal(err)
	}
	nonMFAAdmin, err := BootstrapAccount("admin-no-mfa", "Admin No MFA", "admin", "admin-no-mfa-token", "", false, now)
	if err != nil {
		t.Fatal(err)
	}
	service := NewService(NewMemoryStore(mfaAdmin, nonMFAAdmin))

	if _, err = service.ResetMFA(context.Background(), "admin-mfa", "admin-mfa", "should be blocked", false); !errors.Is(err, ErrAdminLockout) || !errors.Is(err, ErrForbidden) {
		t.Fatalf("ResetMFA last MFA admin error = %v, want ErrAdminLockout wrapping ErrForbidden", err)
	}
	if _, err = service.Disable(context.Background(), "admin-mfa", "admin-mfa", "should be blocked"); !errors.Is(err, ErrAdminLockout) || !errors.Is(err, ErrForbidden) {
		t.Fatalf("Disable last MFA admin error = %v, want ErrAdminLockout wrapping ErrForbidden", err)
	}
}

func TestServiceAllowsAdminDisableWhenAnotherMFAAdminRemains(t *testing.T) {
	now := time.Now().UTC()
	secret := "JBSWY3DPEHPK3PXP"
	first, err := BootstrapAccount("admin-1", "Admin One", "admin", "admin-one-token", secret, true, now)
	if err != nil {
		t.Fatal(err)
	}
	second, err := BootstrapAccount("admin-2", "Admin Two", "admin", "admin-two-token", secret, true, now)
	if err != nil {
		t.Fatal(err)
	}
	service := NewService(NewMemoryStore(first, second))

	account, err := service.Disable(context.Background(), "admin-1", "admin-2", "rotation complete")
	if err != nil {
		t.Fatalf("Disable with another MFA admin remaining error = %v", err)
	}
	if account.Status != "disabled" {
		t.Fatalf("Status = %q, want disabled", account.Status)
	}
}
