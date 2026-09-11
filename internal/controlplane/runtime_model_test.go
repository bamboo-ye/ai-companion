package controlplane

import (
	"context"
	"errors"
	"testing"
)

func TestActiveModelRuntimeProjectsPublishedProfile(t *testing.T) {
	service := NewService(NewMemoryStore(), "staging")
	snapshot, err := service.ActiveModelRuntime(context.Background(), DefaultModelProfileKey)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Revision != 1 || snapshot.Version != 1 || snapshot.Fingerprint == "" || snapshot.ConfigVersion != "bootstrap-v1" {
		t.Fatalf("snapshot metadata = %#v", snapshot)
	}
	variables := snapshot.Variables
	if variables["MODEL_PROVIDER"] != "openrouter" || variables["MODEL_PLANNER_NAME"] != "deepseek/deepseek-v4-flash-0731" {
		t.Fatalf("runtime variables = %#v", variables)
	}
	if variables["MODEL_COMPANION_RESPONDER_FALLBACK_NAMES"] != "google/gemma-4-31b-it:free" || variables["MODEL_TRANSLATION_MAX_TOKENS"] != "8000" {
		t.Fatalf("role variables = %#v", variables)
	}
	if variables["MODEL_REQUIRE_PINNED"] != "true" || variables["MODEL_DATA_COLLECTION"] != "deny" {
		t.Fatalf("policy variables = %#v", variables)
	}
	if err = snapshot.ValidateCredentialEnvironment(func(name string) (string, bool) {
		return "secret", name == "MODEL_API_KEY"
	}); err != nil {
		t.Fatalf("ValidateCredentialEnvironment() error = %v", err)
	}
}

func TestActiveModelRuntimeRequiresKnownDeploymentAndCredential(t *testing.T) {
	service := NewService(NewMemoryStore(), "test")
	if _, err := service.ActiveModelRuntime(context.Background(), "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing profile error = %v", err)
	}
	snapshot, err := service.ActiveModelRuntime(context.Background(), DefaultModelProfileKey)
	if err != nil {
		t.Fatal(err)
	}
	if err = snapshot.ValidateCredentialEnvironment(func(string) (string, bool) { return "", false }); !errors.Is(err, ErrValidation) {
		t.Fatalf("missing credential error = %v", err)
	}
}
