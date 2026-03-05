package controller

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	reconcileTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "claw_reconcile_total",
		Help: "Total Claw reconcile invocations.",
	}, []string{"namespace", "result"})

	reconcileDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "claw_reconcile_duration_seconds",
		Help:    "Claw reconcile latency.",
		Buckets: prometheus.DefBuckets,
	}, []string{"namespace"})

	managedInstances = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "claw_managed_instances",
		Help: "Total managed Claw instances.",
	})

	instancePhase = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "claw_instance_phase",
		Help: "Claw instance phase (1=active in this phase).",
	}, []string{"namespace", "instance", "phase"})

	instanceReady = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "claw_instance_ready",
		Help: "Claw instance readiness (1=ready, 0=not ready).",
	}, []string{"namespace", "instance"})

	resourceCreationFailures = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "claw_resource_creation_failures_total",
		Help: "Sub-resource creation failures.",
	}, []string{"namespace", "resource"})
)

// RecordReconcile records a reconcile invocation with its duration and result.
func RecordReconcile(namespace, result string, duration time.Duration) {
	reconcileTotal.WithLabelValues(namespace, result).Inc()
	reconcileDuration.WithLabelValues(namespace).Observe(duration.Seconds())
}

// SetManagedInstances sets the total count of managed Claw instances.
func SetManagedInstances(count int) {
	managedInstances.Set(float64(count))
}

// SetInstancePhase sets the phase gauge for a Claw instance.
// It clears all other phases for this instance first.
func SetInstancePhase(namespace, instance, phase string) {
	for _, p := range []string{"Pending", "Provisioning", "Running", "Degraded", "Updating", "Failed", "Terminating"} {
		val := float64(0)
		if p == phase {
			val = 1
		}
		instancePhase.WithLabelValues(namespace, instance, p).Set(val)
	}
}

// SetInstanceReady sets the readiness gauge for a Claw instance.
func SetInstanceReady(namespace, instance string, ready bool) {
	val := float64(0)
	if ready {
		val = 1
	}
	instanceReady.WithLabelValues(namespace, instance).Set(val)
}

// RecordResourceCreationFailure increments the resource creation failure counter.
func RecordResourceCreationFailure(namespace, resource string) {
	resourceCreationFailures.WithLabelValues(namespace, resource).Inc()
}
