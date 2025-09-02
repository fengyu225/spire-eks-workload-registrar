package controller

import (
	"context"

	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	spirev1alpha1 "spire-eks-workload-registrar/api/v1alpha1"
)

// WorkloadIdentityReconciler reconciles a WorkloadIdentity object
type WorkloadIdentityReconciler struct {
	client.Client
	Scheme *runtime.Scheme

	// Reference to main reconciler
	MainReconciler *MainReconciler
}

func (r *WorkloadIdentityReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := log.FromContext(ctx).WithValues("workloadidentity", req.NamespacedName)
	log.V(1).Info("Starting WorkloadIdentity reconciliation")

	identity := &spirev1alpha1.WorkloadIdentity{}
	err := r.Get(ctx, req.NamespacedName, identity)

	if err != nil {
		if client.IgnoreNotFound(err) == nil {
			log.V(2).Info("WorkloadIdentity resource deleted")
			if r.MainReconciler != nil && r.MainReconciler.targetedReconciler != nil {
				r.MainReconciler.targetedReconciler.ReconcileWorkloadIdentity(ctx, identity, true)
			}
		}
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Check if the resource is being deleted
	if !identity.DeletionTimestamp.IsZero() {
		log.Info("WorkloadIdentity is being deleted, skipping targeted reconciliation", "identity", identity.Name)
		// The deletion controller will handle cleanup via finalizer
		return ctrl.Result{}, nil
	}

	log.Info("WorkloadIdentity resource changed, using targeted reconciliation", "identity", identity.Name)

	if r.MainReconciler != nil && r.MainReconciler.targetedReconciler != nil {
		r.MainReconciler.targetedReconciler.ReconcileWorkloadIdentity(ctx, identity, false)
	}

	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *WorkloadIdentityReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&spirev1alpha1.WorkloadIdentity{}).
		Complete(r)
}
