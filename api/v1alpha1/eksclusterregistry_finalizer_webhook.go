package v1alpha1

import (
	"context"
	"fmt"

	"spire-eks-workload-registrar/pkg/finalizer"

	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
)

// +kubebuilder:object:generate=false
type EKSClusterRegistryFinalizerWebhook struct {
	Client client.Client
}

func (w *EKSClusterRegistryFinalizerWebhook) SetupWebhookWithManager(mgr ctrl.Manager) error {
	w.Client = mgr.GetClient()
	return ctrl.NewWebhookManagedBy(mgr).
		For(&EKSClusterRegistry{}).
		WithDefaulter(w).
		Complete()
}

//+kubebuilder:webhook:path=/mutate-eksclusterregistry-spire-spiffe-io-v1alpha1-eksclusterregistry,mutating=true,failurePolicy=fail,sideEffects=None,groups=eksclusterregistry.spire.spiffe.io,resources=eksclusterregistries,verbs=create;update,versions=v1alpha1,name=meksclusterregistry.kb.io,admissionReviewVersions=v1

var _ webhook.CustomDefaulter = &EKSClusterRegistryFinalizerWebhook{}

// Default implements webhook.CustomDefaulter
func (w *EKSClusterRegistryFinalizerWebhook) Default(ctx context.Context, obj runtime.Object) error {
	cluster, ok := obj.(*EKSClusterRegistry)
	if !ok {
		return fmt.Errorf("expected EKSClusterRegistry, got %T", obj)
	}

	// Add finalizer if not being deleted
	if cluster.DeletionTimestamp.IsZero() {
		cluster.Finalizers = finalizer.AddFinalizer(cluster.Finalizers, finalizer.SPIRECleanupFinalizer)
	}

	return nil
}
