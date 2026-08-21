package character

import (
	"context"
	"errors"
	"testing"
)

func TestCharacterModuleAssignmentAndLegacyUpdate(t *testing.T) {
	service := NewService(NewMemoryStore())
	ctx := context.Background()

	companion, _, err := service.Create(ctx, "user-1", Input{Name: "小棉"})
	if err != nil {
		t.Fatal(err)
	}
	if companion.Module != "companion" {
		t.Fatalf("default module = %s, want companion", companion.Module)
	}

	worker, persona, err := service.Create(ctx, "user-1", Input{Module: "work", Name: "阿策", Relationship: "工作搭档"})
	if err != nil {
		t.Fatal(err)
	}
	if worker.Module != "work" {
		t.Fatalf("module = %s, want work", worker.Module)
	}
	identity, ok := persona.Compiled["identity"].(map[string]any)
	if !ok || identity["module"] != "work" {
		t.Fatalf("compiled identity module = %v", persona.Compiled["identity"])
	}

	updated, _, err := service.Update(ctx, "user-1", worker.ID, Input{Name: "阿策", Relationship: "长期工作搭档"})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Module != "work" {
		t.Fatalf("legacy update changed module to %s", updated.Module)
	}
}

func TestCharacterRejectsInvalidModule(t *testing.T) {
	service := NewService(NewMemoryStore())
	_, _, err := service.Create(context.Background(), "user-1", Input{Module: "finance", Name: "阿财"})
	if !errors.Is(err, ErrValidation) {
		t.Fatalf("error = %v, want ErrValidation", err)
	}
}

func TestCharacterUsesModuleDefaultsForEmptyCreateInput(t *testing.T) {
	tests := []struct {
		module       string
		name         string
		relationship string
	}{
		{module: "companion", name: "小棉", relationship: "温柔朋友"},
		{module: "life", name: "小满", relationship: "生活管家"},
		{module: "work", name: "阿策", relationship: "工作搭档"},
	}
	for _, test := range tests {
		t.Run(test.module, func(t *testing.T) {
			service := NewService(NewMemoryStore())
			item, _, err := service.Create(context.Background(), "user-1", Input{Module: test.module})
			if err != nil {
				t.Fatal(err)
			}
			if item.Name != test.name || item.Relationship != test.relationship {
				t.Fatalf("defaults = %s/%s, want %s/%s", item.Name, item.Relationship, test.name, test.relationship)
			}
			if item.Personality == "" || item.SpeechStyle == "" || item.Initiative != "balanced" || item.ReplyLength != "short" {
				t.Fatalf("incomplete defaults: %+v", item)
			}
		})
	}
}
