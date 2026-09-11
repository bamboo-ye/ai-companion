package main

import (
	"context"
	"strings"
	"time"

	"github.com/windcry1/ai-companion/internal/controlplane"
)

type modelRuntimeSource interface {
	ActiveModelRuntime(context.Context, string) (controlplane.ModelRuntimeSnapshot, error)
}

type modelRuntimeEnvironmentTarget interface {
	ReloadEnvironment(map[string]string) bool
}

type modelConfigurationObservation struct {
	Outcome       string
	Revision      int
	VersionID     string
	ConfigVersion string
	Fingerprint   string
	Err           error
}

func loadModelConfiguration(
	ctx context.Context,
	source modelRuntimeSource,
	target modelRuntimeEnvironmentTarget,
	currentVersionID string,
	lookupCredential func(string) (string, bool),
) (string, modelConfigurationObservation) {
	if source == nil || target == nil {
		return currentVersionID, modelConfigurationObservation{Outcome: "error", Err: controlplane.ErrValidation}
	}
	snapshot, err := source.ActiveModelRuntime(ctx, controlplane.DefaultModelProfileKey)
	if err == nil {
		err = snapshot.ValidateCredentialEnvironment(lookupCredential)
	}
	observation := modelConfigurationObservation{
		Outcome: "unchanged", Revision: snapshot.Revision, VersionID: snapshot.VersionID,
		ConfigVersion: snapshot.ConfigVersion, Fingerprint: snapshot.Fingerprint, Err: err,
	}
	if err != nil {
		observation.Outcome = "error"
		observation.VersionID = currentVersionID
		observation.Fingerprint = ""
		return currentVersionID, observation
	}
	if snapshot.VersionID == currentVersionID {
		return currentVersionID, observation
	}
	target.ReloadEnvironment(snapshot.Variables)
	observation.Outcome = "applied"
	return snapshot.VersionID, observation
}

func runModelConfigurationWatcher(
	ctx context.Context,
	interval time.Duration,
	source modelRuntimeSource,
	target modelRuntimeEnvironmentTarget,
	initialVersionID string,
	lookupCredential func(string) (string, bool),
	observe func(modelConfigurationObservation),
) error {
	if interval < time.Second || source == nil || target == nil || lookupCredential == nil {
		return controlplane.ErrValidation
	}
	currentVersionID := strings.TrimSpace(initialVersionID)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			var observation modelConfigurationObservation
			currentVersionID, observation = loadModelConfiguration(ctx, source, target, currentVersionID, lookupCredential)
			if observe != nil {
				observe(observation)
			}
		}
	}
}
