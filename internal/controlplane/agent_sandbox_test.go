package controlplane

import (
	"strings"
	"testing"
)

func TestSandboxEnvironmentDoesNotInheritProductionConnectionsOrSecrets(t *testing.T) {
	t.Setenv("MODEL_API_KEY", "must-not-leak")
	t.Setenv("POSTGRES_DSN", "must-not-leak")
	t.Setenv("KAFKA_BROKERS", "must-not-leak")

	items := sandboxEnvironment("workers/python/src")
	joined := strings.Join(items, "\n")
	for _, secret := range []string{"MODEL_API_KEY", "POSTGRES_DSN", "KAFKA_BROKERS", "must-not-leak"} {
		if strings.Contains(joined, secret) {
			t.Fatalf("sandbox environment leaked %s: %s", secret, joined)
		}
	}
	for _, required := range []string{
		"AI_COMPANION_SANDBOX_MODE=synthetic",
		"PYTHONDONTWRITEBYTECODE=1",
		"PYTHONPATH=workers/python/src",
	} {
		if !strings.Contains(joined, required) {
			t.Fatalf("sandbox environment missing %s: %s", required, joined)
		}
	}
}

func TestSandboxBufferBoundsOutputWithoutShortWrites(t *testing.T) {
	buffer := &sandboxBuffer{limit: 4}
	value := []byte("123456")
	written, err := buffer.Write(value)
	if err != nil || written != len(value) || buffer.String() != "1234" || !buffer.exceeded {
		t.Fatalf("sandboxBuffer.Write() = %d, %v, %q, exceeded=%t", written, err, buffer.String(), buffer.exceeded)
	}
}
