package controller

import (
	"context"
	"fmt"
	"strings"

	spirev1alpha1 "spire-eks-workload-registrar/api/v1alpha1"
	"spire-eks-workload-registrar/pkg/finalizer"
	"spire-eks-workload-registrar/pkg/spireapi"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

type DeletionReconciler struct {
	client.Client
	Scheme      *runtime.Scheme
	SPIREClient spireapi.Client
}

// ReconcileWorkloadIdentity handles deletion of WorkloadIdentity
func (r *DeletionReconciler) ReconcileWorkloadIdentity(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := log.FromContext(ctx)

	var identity spirev1alpha1.WorkloadIdentity
	if err := r.Get(ctx, req.NamespacedName, &identity); err != nil {
		if errors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	// Check if being deleted
	if !identity.DeletionTimestamp.IsZero() {
		if finalizer.ContainsFinalizer(identity.Finalizers, finalizer.SPIRECleanupFinalizer) {
			// Delete associated SPIRE entries
			if err := r.deleteEntriesForOwner(ctx, "WorkloadIdentity", identity.Name); err != nil {
				log.Error(err, "Failed to delete SPIRE entries")
				return ctrl.Result{}, err
			}

			// Remove finalizer
			identity.Finalizers = finalizer.RemoveFinalizer(identity.Finalizers, finalizer.SPIRECleanupFinalizer)
			if err := r.Update(ctx, &identity); err != nil {
				return ctrl.Result{}, err
			}

			log.Info("Cleaned up SPIRE entries for deleted WorkloadIdentity", "name", identity.Name)
		}
	}

	return ctrl.Result{}, nil
}

// ReconcileEKSClusterRegistry handles deletion of EKSClusterRegistry
func (r *DeletionReconciler) ReconcileEKSClusterRegistry(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := log.FromContext(ctx)

	var cluster spirev1alpha1.EKSClusterRegistry
	if err := r.Get(ctx, req.NamespacedName, &cluster); err != nil {
		if errors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	// Check if being deleted
	if !cluster.DeletionTimestamp.IsZero() {
		if finalizer.ContainsFinalizer(cluster.Finalizers, finalizer.SPIRECleanupFinalizer) {
			// Delete associated SPIRE entries
			if err := r.deleteEntriesForOwner(ctx, "EKSClusterRegistry", cluster.Name); err != nil {
				log.Error(err, "Failed to delete SPIRE entries")
				return ctrl.Result{}, err
			}

			// Remove finalizer
			cluster.Finalizers = finalizer.RemoveFinalizer(cluster.Finalizers, finalizer.SPIRECleanupFinalizer)
			if err := r.Update(ctx, &cluster); err != nil {
				return ctrl.Result{}, err
			}

			log.Info("Cleaned up SPIRE entries for deleted EKSClusterRegistry", "name", cluster.Name)
		}
	}

	return ctrl.Result{}, nil
}

func (r *DeletionReconciler) deleteEntriesForOwner(ctx context.Context, ownerType, ownerName string) error {
	log := log.FromContext(ctx)

	// List all SPIRE entries
	entries, err := r.SPIREClient.ListEntries(ctx)
	if err != nil {
		return fmt.Errorf("failed to list SPIRE entries: %w", err)
	}

	var toDelete []string

	if ownerType == "EKSClusterRegistry" {
		// For EKSClusterRegistry, look for entries that reference this cluster
		// Pattern: workloadidentity:<identity>:<cluster-name>
		for _, entry := range entries {
			// Check if entry hint contains reference to this cluster
			if strings.Contains(entry.Hint, ":"+ownerName+";") || strings.Contains(entry.Hint, ":"+ownerName) {
				// Additional check to ensure it's in the right format
				if strings.HasPrefix(entry.Hint, "workloadidentity:") {
					parts := strings.Split(entry.Hint, ":")
					if len(parts) >= 3 {
						// Extract cluster name from position after identity name
						clusterPart := strings.Split(parts[2], ";")[0]
						if clusterPart == ownerName {
							toDelete = append(toDelete, entry.ID)
							log.Info("Found entry to delete for cluster", "entryID", entry.ID, "cluster", ownerName, "hint", entry.Hint)
						}
					}
				}
			}
		}
	} else {
		// For WorkloadIdentity, look for owner tag
		ownerKey := fmt.Sprintf("owner:%s/%s", ownerType, ownerName)
		for _, entry := range entries {
			if strings.Contains(entry.Hint, ownerKey) {
				toDelete = append(toDelete, entry.ID)
				log.Info("Found entry to delete", "entryID", entry.ID, "owner", ownerKey)
			}
		}
	}

	// Delete the entries
	if len(toDelete) > 0 {
		log.Info("Deleting SPIRE entries", "count", len(toDelete), "ownerType", ownerType, "ownerName", ownerName)
		statuses, err := r.SPIREClient.BatchDeleteEntries(ctx, toDelete)
		if err != nil {
			return fmt.Errorf("failed to delete entries: %w", err)
		}

		// Check for failures
		for i, status := range statuses {
			if status.Code != 0 {
				log.Error(status.Err(), "Failed to delete entry", "entryID", toDelete[i])
			}
		}
	} else {
		log.Info("No SPIRE entries found to delete", "ownerType", ownerType, "ownerName", ownerName)
	}

	return nil
}

func (r *DeletionReconciler) SetupWithManager(mgr ctrl.Manager) error {
	// Setup for WorkloadIdentity with unique controller name
	if err := ctrl.NewControllerManagedBy(mgr).
		For(&spirev1alpha1.WorkloadIdentity{}).
		Named("workloadidentity-finalizer").
		Complete(&reconcileWrapper{r: r, reconcileFunc: r.ReconcileWorkloadIdentity}); err != nil {
		return err
	}

	// Setup for EKSClusterRegistry with unique controller name
	if err := ctrl.NewControllerManagedBy(mgr).
		For(&spirev1alpha1.EKSClusterRegistry{}).
		Named("eksclusterregistry-finalizer").
		Complete(&reconcileWrapper{r: r, reconcileFunc: r.ReconcileEKSClusterRegistry}); err != nil {
		return err
	}

	return nil
}

type reconcileWrapper struct {
	r             *DeletionReconciler
	reconcileFunc func(context.Context, ctrl.Request) (ctrl.Result, error)
}

func (w *reconcileWrapper) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	return w.reconcileFunc(ctx, req)
}
