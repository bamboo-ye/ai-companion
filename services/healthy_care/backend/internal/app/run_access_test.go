package app

import (
	"testing"
	"time"
)

func TestRunScopeAndExpiry(t *testing.T) {
	now := time.Now()
	key := "private-test-signing-key-with-32-characters"
	token := signRunAccess(key, "run-a", now.Add(time.Minute).Unix())
	if !verifyRunAccess(key, "run-a", token, now) || verifyRunAccess(key, "run-b", token, now) || verifyRunAccess(key, "run-a", token, now.Add(2*time.Minute)) || verifyRunAccess("wrong-key", "run-a", token, now) {
		t.Fatal("run scope or expiry was not enforced")
	}
}
