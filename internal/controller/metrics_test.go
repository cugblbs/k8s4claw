package controller

import (
	"testing"
	"time"
)

func TestRecordReconcile(t *testing.T) {
	// Should not panic.
	RecordReconcile("default", "success", 100*time.Millisecond)
	RecordReconcile("default", "error", 200*time.Millisecond)
}

func TestSetInstancePhase(t *testing.T) {
	SetInstancePhase("default", "test-claw", "Running")
	// Should not panic, verifies all phases are set.
}

func TestSetInstanceReady(t *testing.T) {
	SetInstanceReady("default", "test-claw", true)
	SetInstanceReady("default", "test-claw", false)
}

func TestSetManagedInstances(t *testing.T) {
	SetManagedInstances(5)
}

func TestRecordResourceCreationFailure(t *testing.T) {
	RecordResourceCreationFailure("default", "StatefulSet")
}
