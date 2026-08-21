package postgresstore

import (
	"encoding/json"
	"testing"
)

func TestToolWaitInterruptRequiresTypedTaskAndInterruptIdentity(t *testing.T) {
	output := json.RawMessage(`{
		"interrupts":[{
			"type":"tool_wait",
			"task_id":"task-1",
			"interrupt_id":"interrupt-1"
		}]
	}`)
	_, metadata, err := toolWaitInterrupt(output)
	if err != nil || metadata.TaskID != "task-1" || metadata.InterruptID != "interrupt-1" {
		t.Fatalf("toolWaitInterrupt() = %#v, %v", metadata, err)
	}
}

func TestToolWaitInterruptRejectsMissingInterruptIdentity(t *testing.T) {
	output := json.RawMessage(`{"interrupts":[{"type":"tool_wait","task_id":"task-1"}]}`)
	if _, _, err := toolWaitInterrupt(output); err == nil {
		t.Fatal("toolWaitInterrupt() expected an error")
	}
}
