package sdk

import (
	"fmt"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

const (
	apiVersion = "claw.prismer.ai/v1alpha1"
	kind       = "Claw"
)

func toUnstructured(spec *ClawSpec) *unstructured.Unstructured {
	obj := &unstructured.Unstructured{}
	obj.SetAPIVersion(apiVersion)
	obj.SetKind(kind)
	obj.SetName(spec.Name)
	obj.SetNamespace(spec.Namespace)
	if len(spec.Labels) > 0 {
		obj.SetLabels(spec.Labels)
	}

	s := map[string]interface{}{
		"runtime": string(spec.Runtime),
	}

	if spec.Replicas > 0 {
		s["replicas"] = int64(spec.Replicas)
	}

	if spec.Config != nil && len(spec.Config.Environment) > 0 {
		env := make(map[string]interface{}, len(spec.Config.Environment))
		for k, v := range spec.Config.Environment {
			env[k] = v
		}
		s["config"] = map[string]interface{}{
			"environment": env,
		}
	}

	if err := unstructured.SetNestedField(obj.Object, s, "spec"); err != nil {
		// Should never happen with well-formed maps.
		panic(fmt.Sprintf("failed to set spec: %v", err))
	}

	return obj
}

func fromUnstructured(obj *unstructured.Unstructured) *ClawInstance {
	inst := &ClawInstance{
		Name:      obj.GetName(),
		Namespace: obj.GetNamespace(),
		CreatedAt: obj.GetCreationTimestamp().Time,
	}

	if rt, ok, _ := unstructured.NestedString(obj.Object, "spec", "runtime"); ok {
		inst.Runtime = RuntimeType(rt)
	}

	if phase, ok, _ := unstructured.NestedString(obj.Object, "status", "phase"); ok {
		inst.Phase = phase
	}

	if conditions, ok, _ := unstructured.NestedSlice(obj.Object, "status", "conditions"); ok {
		for _, c := range conditions {
			cm, ok := c.(map[string]interface{})
			if !ok {
				continue
			}
			cond := Condition{}
			if t, ok := cm["type"].(string); ok {
				cond.Type = t
			}
			if s, ok := cm["status"].(string); ok {
				cond.Status = s
			}
			if m, ok := cm["message"].(string); ok {
				cond.Message = m
			}
			if lt, ok := cm["lastTransitionTime"].(string); ok {
				if t, err := time.Parse(time.RFC3339, lt); err == nil {
					cond.LastTransitionTime = t
				}
			}
			inst.Conditions = append(inst.Conditions, cond)
		}
	}

	return inst
}
