package controlplane

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"strings"
)

// AgentRuntimeSnapshot is the immutable runtime projection of one published
// Agent Studio definition. The payload is validated again before it is bound
// to a Run so a corrupted deployment can never reach a worker.
type AgentRuntimeSnapshot struct {
	Environment  string                 `json:"environment"`
	Key          string                 `json:"key"`
	Revision     int                    `json:"revision"`
	VersionID    string                 `json:"version_id"`
	Version      int                    `json:"version"`
	Fingerprint  string                 `json:"fingerprint"`
	ModelProfile string                 `json:"model_profile"`
	Definition   AgentDefinitionPayload `json:"definition"`
}

// ActiveAgentRuntime resolves the single published definition assigned to a
// module. Ambiguous assignments fail closed instead of selecting by database
// ordering, which keeps routing deterministic and auditable.
func (s *Service) ActiveAgentRuntime(ctx context.Context, module string) (AgentRuntimeSnapshot, error) {
	module = strings.ToLower(strings.TrimSpace(module))
	if module != "companion" && module != "life" && module != "work" {
		return AgentRuntimeSnapshot{}, ErrValidation
	}
	deployments, err := s.Active(ctx, KindAgentDefinition)
	if err != nil {
		return AgentRuntimeSnapshot{}, err
	}
	var result AgentRuntimeSnapshot
	for _, deployment := range deployments {
		validated, validateErr := validateAgentDefinition(deployment.Version.Payload)
		if validateErr != nil {
			return AgentRuntimeSnapshot{}, validateErr
		}
		var definition AgentDefinitionPayload
		if decodeErr := json.Unmarshal(validated, &definition); decodeErr != nil {
			return AgentRuntimeSnapshot{}, validationError("decode active Agent definition: %v", decodeErr)
		}
		if !containsAgentModule(definition.Modules, module) {
			continue
		}
		if result.VersionID != "" {
			return AgentRuntimeSnapshot{}, ErrConflict
		}
		result = AgentRuntimeSnapshot{
			Environment: s.environment, Key: deployment.Key, Revision: deployment.Revision,
			VersionID: deployment.Version.ID, Version: deployment.Version.Version,
			Fingerprint: deployment.Version.Fingerprint, ModelProfile: definition.ModelProfile,
			Definition: definition,
		}
	}
	if result.VersionID == "" {
		return AgentRuntimeSnapshot{}, ErrNotFound
	}
	return result, nil
}

// RoutedAgentRuntime keeps one routing key in a stable rollout cohort. Only
// running or ready rollouts are eligible; paused and aborted rollouts fail
// closed to the currently published baseline without changing existing Runs.
func (s *Service) RoutedAgentRuntime(ctx context.Context, module, routingKey string) (AgentRuntimeSnapshot, error) {
	stable, err := s.ActiveAgentRuntime(ctx, module)
	if err != nil {
		return AgentRuntimeSnapshot{}, err
	}
	rollout, err := s.store.ActiveAgentRollout(ctx, s.environment)
	if errors.Is(err, ErrNotFound) || strings.TrimSpace(routingKey) == "" {
		return stable, nil
	}
	if err != nil {
		return AgentRuntimeSnapshot{}, err
	}
	if rollout.AgentKey != stable.Key || !agentRolloutBucket(rollout, routingKey) {
		return stable, nil
	}
	version, err := s.store.GetConfigVersion(ctx, rollout.AgentVersionID)
	if err != nil {
		return AgentRuntimeSnapshot{}, err
	}
	validated, err := validateAgentDefinition(version.Payload)
	if err != nil {
		return AgentRuntimeSnapshot{}, err
	}
	var definition AgentDefinitionPayload
	if err = json.Unmarshal(validated, &definition); err != nil {
		return AgentRuntimeSnapshot{}, validationError("decode rollout Agent definition: %v", err)
	}
	if version.Kind != KindAgentDefinition || version.Key != rollout.AgentKey ||
		version.Fingerprint != rollout.AgentFingerprint {
		return AgentRuntimeSnapshot{}, ErrConflict
	}
	if !containsAgentModule(definition.Modules, module) {
		return stable, nil
	}
	return AgentRuntimeSnapshot{
		Environment: s.environment, Key: version.Key, Revision: rollout.RoutingRevision,
		VersionID: version.ID, Version: version.Version, Fingerprint: version.Fingerprint,
		ModelProfile: definition.ModelProfile, Definition: definition,
	}, nil
}

func agentRolloutBucket(rollout AgentRollout, routingKey string) bool {
	digest := sha256.Sum256([]byte(rollout.Environment + "\x00" + rollout.ID + "\x00" + strings.TrimSpace(routingKey)))
	bucket := binary.BigEndian.Uint64(digest[:8]) % 100
	return bucket < uint64(rollout.TrafficPercent)
}

func containsAgentModule(modules []string, module string) bool {
	for _, candidate := range modules {
		if candidate == module {
			return true
		}
	}
	return false
}
