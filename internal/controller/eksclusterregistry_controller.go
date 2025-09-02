package controller

import (
	"context"

	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	spirev1alpha1 "spire-eks-workload-registrar/api/v1alpha1"
)

// EKSClusterRegistryReconciler reconciles a EKSClusterRegistry object
type EKSClusterRegistryReconciler struct {
	client.Client
	Scheme *runtime.Scheme

	MainReconciler *MainReconciler
}

func (r *EKSClusterRegistryReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := log.FromContext(ctx).WithValues("eksclusterregistry", req.NamespacedName)
	log.V(1).Info("Starting EKSClusterRegistry reconciliation")

	cluster := &spirev1alpha1.EKSClusterRegistry{}
	err := r.Get(ctx, req.NamespacedName, cluster)

	if err != nil {
		if client.IgnoreNotFound(err) == nil {
			log.V(2).Info("EKSClusterRegistry resource deleted")
			if r.MainReconciler != nil && r.MainReconciler.targetedReconciler != nil {
				r.MainReconciler.targetedReconciler.ReconcileEKSCluster(ctx, cluster, true)
			}
		}
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Check if the resource is being deleted
	if !cluster.DeletionTimestamp.IsZero() {
		log.Info("EKSClusterRegistry is being deleted, skipping targeted reconciliation", "cluster", cluster.Name)
		// The deletion controller will handle cleanup via finalizer
		return ctrl.Result{}, nil
	}

	log.Info("EKSClusterRegistry resource changed, using targeted reconciliation", "cluster", cluster.Name)

	if r.MainReconciler != nil && r.MainReconciler.targetedReconciler != nil {
		r.MainReconciler.targetedReconciler.ReconcileEKSCluster(ctx, cluster, false)
	}

	return ctrl.Result{}, nil
}

func (r *EKSClusterRegistryReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&spirev1alpha1.EKSClusterRegistry{}).
		Complete(r)
}
