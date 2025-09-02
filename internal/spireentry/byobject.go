package spireentry

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"

	spirev1alpha1 "spire-eks-workload-registrar/api/v1alpha1"
)

// byObject interface tracks which resource created an entry
type byObject interface {
	GetObjectKind() schema.ObjectKind
	GetUID() types.UID
	GetCreationTimestamp() metav1.Time
	GetDeletionTimestamp() *metav1.Time
	IncrementEntriesToSet()
	IncrementEntriesMasked()
	IncrementEntrySuccess()
	IncrementEntryFailures()
}

// workloadIdentityBy implements byObject for WorkloadIdentity tracking
type workloadIdentityBy struct {
	identity *spirev1alpha1.WorkloadIdentity
	cluster  *spirev1alpha1.EKSClusterRegistry
	pod      *corev1.Pod
	stats    *spirev1alpha1.WorkloadIdentityStats
}

func newWorkloadIdentityBy(identity *spirev1alpha1.WorkloadIdentity, cluster *spirev1alpha1.EKSClusterRegistry, pod *corev1.Pod, stats *spirev1alpha1.WorkloadIdentityStats) byObject {
	return &workloadIdentityBy{
		identity: identity,
		cluster:  cluster,
		pod:      pod,
		stats:    stats,
	}
}

func (w *workloadIdentityBy) GetObjectKind() schema.ObjectKind {
	return w.identity.GetObjectKind()
}

func (w *workloadIdentityBy) GetUID() types.UID {
	return w.identity.UID
}

func (w *workloadIdentityBy) GetCreationTimestamp() metav1.Time {
	return w.identity.CreationTimestamp
}

func (w *workloadIdentityBy) GetDeletionTimestamp() *metav1.Time {
	return w.identity.DeletionTimestamp
}

func (w *workloadIdentityBy) IncrementEntriesToSet() {
	// This would be called when an entry is about to be set
}

func (w *workloadIdentityBy) IncrementEntriesMasked() {
	// Track masked entries if needed
}

func (w *workloadIdentityBy) IncrementEntrySuccess() {
	w.stats.EntriesRegistered++
}

func (w *workloadIdentityBy) IncrementEntryFailures() {
	w.stats.EntryFailures++
}

// objectCmp compares two byObject instances for sorting
func objectCmp(a, b byObject) int {
	// Sort ascending by creation timestamp
	creationDiff := a.GetCreationTimestamp().UnixNano() - b.GetCreationTimestamp().UnixNano()
	switch {
	case creationDiff < 0:
		return -1
	case creationDiff > 0:
		return 1
	}

	// Sort _descending_ by deletion timestamp (those with no timestamp sort first)
	switch {
	case a.GetDeletionTimestamp() == nil && b.GetDeletionTimestamp() == nil:
		// fallthrough to next criteria
	case a.GetDeletionTimestamp() != nil && b.GetDeletionTimestamp() == nil:
		return 1
	case a.GetDeletionTimestamp() == nil && b.GetDeletionTimestamp() != nil:
		return -1
	case a.GetDeletionTimestamp() != nil && b.GetDeletionTimestamp() != nil:
		deleteDiff := a.GetDeletionTimestamp().UnixNano() - b.GetDeletionTimestamp().UnixNano()
		switch {
		case deleteDiff < 0:
			return 1
		case deleteDiff > 0:
			return -1
		}
	}

	// Tie-break with UID
	switch {
	case a.GetUID() < b.GetUID():
		return -1
	case a.GetUID() > b.GetUID():
		return 1
	default:
		return 0
	}
}
