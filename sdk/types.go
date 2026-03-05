package sdk

import "time"

// RuntimeType mirrors the CRD runtime type for SDK consumers.
type RuntimeType string

const (
	OpenClaw RuntimeType = "openclaw"
	NanoClaw RuntimeType = "nanoclaw"
	ZeroClaw RuntimeType = "zeroclaw"
	PicoClaw RuntimeType = "picoclaw"
	Custom   RuntimeType = "custom"
)

// ClawSpec defines the desired state for creating a Claw via the SDK.
type ClawSpec struct {
	// Name is the Claw resource name. Auto-generated if empty.
	Name string

	// Runtime is the agent runtime type (required).
	Runtime RuntimeType

	// Config holds runtime-specific configuration.
	Config *RuntimeConfig

	// Namespace to create the Claw in. Defaults to "default".
	Namespace string

	// Labels are additional labels applied to the Claw CR.
	Labels map[string]string

	// Replicas is the number of StatefulSet replicas. Defaults to 1.
	Replicas int32
}

// RuntimeConfig provides typed runtime configuration.
type RuntimeConfig struct {
	// Environment contains key-value pairs for the runtime.
	Environment map[string]string
}

// ClawInstance represents a running Claw agent.
type ClawInstance struct {
	// Name of the Claw resource.
	Name string

	// Namespace of the Claw resource.
	Namespace string

	// Runtime is the runtime type.
	Runtime RuntimeType

	// Phase is the current lifecycle phase.
	Phase string

	// Conditions are the status conditions.
	Conditions []Condition

	// CreatedAt is the creation timestamp.
	CreatedAt time.Time
}

// Condition represents a Claw status condition.
type Condition struct {
	Type               string
	Status             string
	Message            string
	LastTransitionTime time.Time
}

// UpdateSpec defines fields that can be updated on a Claw.
type UpdateSpec struct {
	// Environment replaces the runtime environment vars.
	Environment map[string]string

	// Replicas updates the replica count.
	Replicas *int32
}

// ListOptions configures List behavior.
type ListOptions struct {
	// LabelSelector filters by label (e.g. "app=myagent").
	LabelSelector string

	// Limit caps the number of results.
	Limit int64
}

// Result represents the output from a Claw agent.
type Result struct {
	// Content is the text response from the agent.
	Content string

	// Metadata contains additional response information.
	Metadata map[string]string
}
