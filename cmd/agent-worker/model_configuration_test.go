package main

import (
	"context"
	"errors"
	"testing"

	"github.com/windcry1/ai-companion/internal/controlplane"
)

type staticModelRuntimeSource struct {
	snapshot controlplane.ModelRuntimeSnapshot
	err      error
}

func (s staticModelRuntimeSource) ActiveModelRuntime(context.Context, string) (controlplane.ModelRuntimeSnapshot, error) {
	return s.snapshot, s.err
}

type recordingModelRuntimeTarget struct {
	reloads []map[string]string
}

func (t *recordingModelRuntimeTarget) ReloadEnvironment(environment map[string]string) bool {
	t.reloads = append(t.reloads, environment)
	return true
}

func TestLoadModelConfigurationAppliesNewPublishedVersion(t *testing.T) {
	target := &recordingModelRuntimeTarget{}
	snapshot := controlplane.ModelRuntimeSnapshot{
		VersionID: "version-2", Revision: 2, ConfigVersion: "routing-v2", Fingerprint: "fingerprint-2",
		CredentialReference: "env:MODEL_API_KEY", Variables: map[string]string{"MODEL_CONFIG_VERSION": "routing-v2"},
	}
	current, observation := loadModelConfiguration(context.Background(), staticModelRuntimeSource{snapshot: snapshot}, target, "version-1", func(name string) (string, bool) {
		return "secret", name == "MODEL_API_KEY"
	})
	if current != "version-2" || observation.Outcome != "applied" || observation.Revision != 2 || len(target.reloads) != 1 {
		t.Fatalf("load result = current %q, observation %#v, reloads %#v", current, observation, target.reloads)
	}
}

func TestLoadModelConfigurationRetainsCurrentVersionOnFailure(t *testing.T) {
	target := &recordingModelRuntimeTarget{}
	current, observation := loadModelConfiguration(context.Background(), staticModelRuntimeSource{err: errors.New("database unavailable")}, target, "version-1", func(string) (string, bool) {
		return "secret", true
	})
	if current != "version-1" || observation.Outcome != "error" || observation.Err == nil || len(target.reloads) != 0 {
		t.Fatalf("load result = current %q, observation %#v, reloads %#v", current, observation, target.reloads)
	}
}

func TestLoadModelConfigurationRejectsMissingCredential(t *testing.T) {
	target := &recordingModelRuntimeTarget{}
	snapshot := controlplane.ModelRuntimeSnapshot{
		VersionID: "version-2", CredentialReference: "env:MODEL_API_KEY",
		Variables: map[string]string{"MODEL_CONFIG_VERSION": "routing-v2"},
	}
	current, observation := loadModelConfiguration(context.Background(), staticModelRuntimeSource{snapshot: snapshot}, target, "version-1", func(string) (string, bool) {
		return "", false
	})
	if current != "version-1" || observation.Outcome != "error" || len(target.reloads) != 0 {
		t.Fatalf("load result = current %q, observation %#v, reloads %#v", current, observation, target.reloads)
	}
}
