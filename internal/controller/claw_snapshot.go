package controller

import (
	"context"
	"fmt"
	"sort"
	"time"

	snapshotv1 "github.com/kubernetes-csi/external-snapshotter/client/v8/apis/volumesnapshot/v1"
	"github.com/robfig/cron/v3"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	clawv1alpha1 "github.com/Prismer-AI/k8s4claw/api/v1alpha1"
)

const lastSnapshotTimeAnnotation = "claw.prismer.ai/last-snapshot-time"

// reconcileSnapshots checks each volume's snapshot schedule and creates
// VolumeSnapshot CRs when due. Returns a non-zero RequeueAfter if any
// snapshot schedule is active.
func (r *ClawReconciler) reconcileSnapshots(ctx context.Context, claw *clawv1alpha1.Claw) (ctrl.Result, error) { //nolint:gocyclo // justified: snapshot reconciler state machine
	if claw.Spec.Persistence == nil {
		return ctrl.Result{}, nil
	}

	var nextRequeue time.Duration

	type volSnap struct {
		volumeName string
		spec       *clawv1alpha1.SnapshotSpec
		pvcName    string
	}

	// Collect volumes with snapshot enabled.
	var snapConfigs []volSnap
	if s := claw.Spec.Persistence.Session; s != nil && s.Snapshot != nil && s.Snapshot.Enabled {
		snapConfigs = append(snapConfigs, volSnap{"session", s.Snapshot, fmt.Sprintf("session-%s-0", claw.Name)})
	}
	if o := claw.Spec.Persistence.Output; o != nil && o.Snapshot != nil && o.Snapshot.Enabled {
		snapConfigs = append(snapConfigs, volSnap{"output", o.Snapshot, fmt.Sprintf("output-%s-0", claw.Name)})
	}
	if w := claw.Spec.Persistence.Workspace; w != nil && w.Snapshot != nil && w.Snapshot.Enabled {
		snapConfigs = append(snapConfigs, volSnap{"workspace", w.Snapshot, fmt.Sprintf("workspace-%s-0", claw.Name)})
	}

	if len(snapConfigs) == 0 {
		return ctrl.Result{}, nil
	}

	logger := log.FromContext(ctx)

	for _, vs := range snapConfigs {
		annotationKey := fmt.Sprintf("%s-%s", lastSnapshotTimeAnnotation, vs.volumeName)
		lastTimeStr := ""
		if claw.Annotations != nil {
			lastTimeStr = claw.Annotations[annotationKey]
		}

		schedule, err := cron.ParseStandard(vs.spec.Schedule)
		if err != nil {
			logger.Error(err, "invalid cron schedule", "volume", vs.volumeName, "schedule", vs.spec.Schedule)
			continue
		}

		now := time.Now()
		var lastTime time.Time
		if lastTimeStr != "" {
			lastTime, err = time.Parse(time.RFC3339, lastTimeStr)
			if err != nil {
				logger.Error(err, "failed to parse last snapshot time", "volume", vs.volumeName)
				lastTime = time.Time{} // treat as never snapshotted
			}
		}

		nextTime := schedule.Next(lastTime)
		if now.Before(nextTime) {
			// Not yet due; calculate requeue.
			untilNext := nextTime.Sub(now)
			if nextRequeue == 0 || untilNext < nextRequeue {
				nextRequeue = untilNext
			}
			continue
		}

		// Snapshot is due — create it.
		snapName := fmt.Sprintf("%s-%s-%s", claw.Name, vs.volumeName, now.Format("20060102-150405"))
		snapshot := buildVolumeSnapshot(claw, snapName, vs.pvcName, vs.spec.VolumeSnapshotClass)

		if err := r.Create(ctx, snapshot); err != nil {
			if !apierrors.IsAlreadyExists(err) {
				return ctrl.Result{}, fmt.Errorf("failed to create VolumeSnapshot %s: %w", snapName, err)
			}
		} else {
			logger.Info("created VolumeSnapshot", "name", snapName, "volume", vs.volumeName)
		}

		// Update annotation with current time.
		if claw.Annotations == nil {
			claw.Annotations = make(map[string]string)
		}
		claw.Annotations[annotationKey] = now.Format(time.RFC3339)
		if err := r.Update(ctx, claw); err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to update snapshot annotation: %w", err)
		}

		// Prune old snapshots.
		if err := r.pruneSnapshots(ctx, claw, vs.volumeName, vs.spec.Retain); err != nil {
			logger.Error(err, "failed to prune old snapshots", "volume", vs.volumeName)
		}

		// Schedule next check.
		untilNext := schedule.Next(now).Sub(now)
		if nextRequeue == 0 || untilNext < nextRequeue {
			nextRequeue = untilNext
		}
	}

	if nextRequeue > 0 {
		return ctrl.Result{RequeueAfter: nextRequeue}, nil
	}
	return ctrl.Result{}, nil
}

// buildVolumeSnapshot constructs a VolumeSnapshot CR.
func buildVolumeSnapshot(claw *clawv1alpha1.Claw, name, pvcName, snapshotClass string) *snapshotv1.VolumeSnapshot {
	labels := map[string]string{
		"claw.prismer.ai/instance": claw.Name,
	}

	vs := &snapshotv1.VolumeSnapshot{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: claw.Namespace,
			Labels:    labels,
		},
		Spec: snapshotv1.VolumeSnapshotSpec{
			Source: snapshotv1.VolumeSnapshotSource{
				PersistentVolumeClaimName: &pvcName,
			},
		},
	}

	if snapshotClass != "" {
		vs.Spec.VolumeSnapshotClassName = &snapshotClass
	}

	return vs
}

// pruneSnapshots deletes the oldest VolumeSnapshots beyond the retain count.
func (r *ClawReconciler) pruneSnapshots(ctx context.Context, claw *clawv1alpha1.Claw, volumeName string, retain int) error {
	if retain <= 0 {
		retain = 5
	}

	logger := log.FromContext(ctx)

	var snapList snapshotv1.VolumeSnapshotList
	if err := r.List(ctx, &snapList,
		client.InNamespace(claw.Namespace),
		client.MatchingLabels{"claw.prismer.ai/instance": claw.Name},
	); err != nil {
		return fmt.Errorf("failed to list VolumeSnapshots: %w", err)
	}

	// Filter snapshots for this volume by name prefix.
	prefix := fmt.Sprintf("%s-%s-", claw.Name, volumeName)
	var matching []snapshotv1.VolumeSnapshot
	for i := range snapList.Items {
		if len(snapList.Items[i].Name) > len(prefix) && snapList.Items[i].Name[:len(prefix)] == prefix {
			matching = append(matching, snapList.Items[i])
		}
	}

	if len(matching) <= retain {
		return nil
	}

	// Sort by creation time ascending (oldest first).
	sort.Slice(matching, func(i, j int) bool {
		return matching[i].CreationTimestamp.Before(&matching[j].CreationTimestamp)
	})

	// Delete excess snapshots.
	toDelete := matching[:len(matching)-retain]
	for i := range toDelete {
		logger.Info("pruning old VolumeSnapshot", "name", toDelete[i].Name)
		if err := r.Delete(ctx, &toDelete[i]); err != nil {
			if client.IgnoreNotFound(err) != nil {
				return fmt.Errorf("failed to delete VolumeSnapshot %s: %w", toDelete[i].Name, err)
			}
		}
	}

	return nil
}
