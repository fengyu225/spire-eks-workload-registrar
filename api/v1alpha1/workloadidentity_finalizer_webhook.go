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
type WorkloadIdentityFinalizerWebhook struct {
	Client client.Client
}

func (w *WorkloadIdentityFinalizerWebhook) SetupWebhookWithManager(mgr ctrl.Manager) error {
	w.Client = mgr.GetClient()
	return ctrl.NewWebhookManagedBy(mgr).
		For(&WorkloadIdentity{}).
		WithDefaulter(w).
		Complete()
}

//+kubebuilder:webhook:path=/mutate-eksclusterregistry-spire-spiffe-io-v1alpha1-workloadidentity,mutating=true,failurePolicy=fail,sideEffects=None,groups=eksclusterregistry.spire.spiffe.io,resources=workloadidentities,verbs=create;update,versions=v1alpha1,name=mworkloadidentity.kb.io,admissionReviewVersions=v1

var _ webhook.CustomDefaulter = &WorkloadIdentityFinalizerWebhook{}

// Default implements webhook.CustomDefaulter
func (w *WorkloadIdentityFinalizerWebhook) Default(ctx context.Context, obj runtime.Object) error {
	entry, ok := obj.(*WorkloadIdentity)
	if !ok {
		return fmt.Errorf("expected WorkloadIdentity, got %T", obj)
	}

	// Add finalizer if not being deleted
	if entry.DeletionTimestamp.IsZero() {
		entry.Finalizers = finalizer.AddFinalizer(entry.Finalizers, finalizer.SPIRECleanupFinalizer)
	}

	return nil
}
