package controller

import (
	"context"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"sigs.k8s.io/controller-runtime/pkg/log"

	spirev1alpha1 "spire-eks-workload-registrar/api/v1alpha1"
	"spire-eks-workload-registrar/internal/cluster"
	"spire-eks-workload-registrar/internal/spireentry"
	"spire-eks-workload-registrar/pkg/spireapi"
)

// TargetedReconciler handles specific pod/identity/cluster reconciliation
type TargetedReconciler struct {
	*MainReconciler

	// Cache of entry ID -> entry for faster lookups
	entryCache map[string]spireapi.Entry
}

// NewTargetedReconciler creates a new targeted reconciler
func NewTargetedReconciler(main *MainReconciler) *TargetedReconciler {
	return &TargetedReconciler{
		MainReconciler: main,
		entryCache:     make(map[string]spireapi.Entry),
	}
}

// ReconcileSinglePod handles a single pod event
func (r *TargetedReconciler) ReconcileSinglePod(
	ctx context.Context,
	pod *corev1.Pod,
	cluster *spirev1alpha1.EKSClusterRegistry,
	action string,
) {
	log := log.FromContext(ctx).WithValues("pod", pod.Namespace+"/"+pod.Name, "action", action)

	if r.isNamespaceIgnored(pod.Namespace) {
		return
	}

	if action == "delete" {
		r.deleteEntriesForPod(ctx, pod)
		return
	}

	identities := r.findMatchingIdentities(ctx, pod, cluster)
	if len(identities) == 0 {
		return
	}

	if pod.Status.Phase != corev1.PodRunning || pod.DeletionTimestamp != nil || pod.Spec.NodeName == "" {
		r.deleteEntriesForPod(ctx, pod)
		return
	}

	conn, ok := r.ClusterManager.GetClusterConnection(cluster.Name)
	if !ok {
		return
	}

	node, err := conn.K8sClient.CoreV1().Nodes().Get(ctx, pod.Spec.NodeName, metav1.GetOptions{})
	if err != nil {
		log.Error(err, "Failed to get node")
		return
	}

	for _, identity := range identities {
		r.reconcilePodEntry(ctx, pod, node, &identity, cluster)
	}
}

// ReconcileWorkloadIdentity handles changes to a WorkloadIdentity
func (r *TargetedReconciler) ReconcileWorkloadIdentity(
	ctx context.Context,
	identity *spirev1alpha1.WorkloadIdentity,
	deleted bool,
) {
	if deleted {
		r.deleteEntriesForIdentity(ctx, identity)
		return
	}

	// Find all clusters that match this identity's cluster selector
	clusters := r.findMatchingClusters(ctx, identity)

	for _, cluster := range clusters {
		conn, ok := r.ClusterManager.GetClusterConnection(cluster.Name)
		if !ok {
			continue
		}

		// Get only pods that match this identity
		pods := r.getMatchingPods(conn, identity)

		// Reconcile each pod
		for _, pod := range pods {
			if pod.Status.Phase != corev1.PodRunning || pod.DeletionTimestamp != nil || pod.Spec.NodeName == "" {
				continue
			}

			node, err := conn.K8sClient.CoreV1().Nodes().Get(ctx, pod.Spec.NodeName, metav1.GetOptions{})
			if err != nil {
				continue
			}

			r.reconcilePodEntry(ctx, pod, node, identity, &cluster)
		}
	}

	r.cleanupOrphanedEntriesForIdentity(ctx, identity)
}

// ReconcileEKSCluster handles changes to an EKSClusterRegistry
func (r *TargetedReconciler) ReconcileEKSCluster(
	ctx context.Context,
	cluster *spirev1alpha1.EKSClusterRegistry,
	deleted bool,
) {
	log := log.FromContext(ctx).WithValues("cluster", cluster.Name)

	if deleted || !cluster.Spec.Enabled {
		r.deleteEntriesForCluster(ctx, cluster)
		r.ClusterManager.RemoveCluster(cluster.Name)
		return
	}

	if err := r.ClusterManager.AddOrUpdateCluster(ctx, cluster); err != nil {
		log.Error(err, "Failed to update cluster")
		return
	}

	conn, ok := r.ClusterManager.GetClusterConnection(cluster.Name)
	if !ok {
		return
	}

	identities := r.findIdentitiesForCluster(ctx, cluster)

	for _, identity := range identities {
		pods := r.getMatchingPods(conn, &identity)

		for _, pod := range pods {
			if pod.Status.Phase != corev1.PodRunning || pod.DeletionTimestamp != nil || pod.Spec.NodeName == "" {
				continue
			}

			node, err := conn.K8sClient.CoreV1().Nodes().Get(ctx, pod.Spec.NodeName, metav1.GetOptions{})
			if err != nil {
				continue
			}

			r.reconcilePodEntry(ctx, pod, node, &identity, cluster)
		}
	}

	// Clean up orphaned entries for this cluster
	r.cleanupOrphanedEntriesForCluster(ctx, cluster)
}

// Reconcile a single pod entry
func (r *TargetedReconciler) reconcilePodEntry(
	ctx context.Context,
	pod *corev1.Pod,
	node *corev1.Node,
	identity *spirev1alpha1.WorkloadIdentity,
	cluster *spirev1alpha1.EKSClusterRegistry,
) {
	entry, err := spireentry.RenderEntry(identity, cluster, pod, node, r.TrustDomain.String())
	if err != nil {
		log.FromContext(ctx).Error(err, "Failed to render entry")
		return
	}

	entryKey := spireentry.MakeEntryKey(*entry)
	if r.EntryIDPrefix != "" {
		entry.ID = r.EntryIDPrefix + entryKey
	} else {
		entry.ID = entryKey
	}

	entries, _ := r.SPIREClient.ListEntries(ctx)
	var existingEntry *spireapi.Entry
	for _, e := range entries {
		if e.ID == entry.ID {
			existingEntry = &e
			break
		}
	}

	if existingEntry == nil {
		r.SPIREClient.CreateEntry(ctx, *entry)
	} else if len(spireentry.GetOutdatedEntryFields(*entry, *existingEntry)) > 0 {
		entry.ID = existingEntry.ID
		r.SPIREClient.UpdateEntry(ctx, *entry)
	}
}

// Delete entries for a specific pod
func (r *TargetedReconciler) deleteEntriesForPod(ctx context.Context, pod *corev1.Pod) {
	entries, err := r.SPIREClient.ListEntries(ctx)
	if err != nil {
		return
	}

	podUIDSelector := fmt.Sprintf("pod-uid:%s", pod.UID)
	for _, entry := range entries {
		if !r.shouldProcessEntry(entry) {
			continue
		}

		for _, selector := range entry.Selectors {
			if selector.Type == "k8s" && selector.Value == podUIDSelector {
				r.SPIREClient.DeleteEntry(ctx, entry.ID)
				break
			}
		}
	}
}

// Delete entries for a WorkloadIdentity
func (r *TargetedReconciler) deleteEntriesForIdentity(ctx context.Context, identity *spirev1alpha1.WorkloadIdentity) {
	entries, err := r.SPIREClient.ListEntries(ctx)
	if err != nil {
		return
	}

	for _, entry := range entries {
		if r.entryBelongsToIdentity(entry, identity) {
			r.SPIREClient.DeleteEntry(ctx, entry.ID)
		}
	}
}

// Delete entries for a cluster
func (r *TargetedReconciler) deleteEntriesForCluster(ctx context.Context, cluster *spirev1alpha1.EKSClusterRegistry) {
	entries, err := r.SPIREClient.ListEntries(ctx)
	if err != nil {
		return
	}

	for _, entry := range entries {
		expectedPrefix := "workloadidentity:"
		if len(entry.Hint) > len(expectedPrefix) {
			hintParts := entry.Hint[len(expectedPrefix):]
			if len(hintParts) > 0 {
				parts := strings.Split(hintParts, ":")
				if len(parts) >= 2 && parts[1] == cluster.Name {
					r.SPIREClient.DeleteEntry(ctx, entry.ID)
				}
			}
		}
	}
}

// Find matching identities for a pod
func (r *TargetedReconciler) findMatchingIdentities(
	ctx context.Context,
	pod *corev1.Pod,
	cluster *spirev1alpha1.EKSClusterRegistry,
) []spirev1alpha1.WorkloadIdentity {
	identities := &spirev1alpha1.WorkloadIdentityList{}
	r.List(ctx, identities)

	var matching []spirev1alpha1.WorkloadIdentity
	for _, identity := range identities.Items {
		if identity.Spec.Namespace != pod.Namespace {
			continue
		}

		if !r.shouldProcessIdentity(&identity, cluster) {
			continue
		}

		if identity.Spec.PodSelector != nil {
			selector, _ := metav1.LabelSelectorAsSelector(identity.Spec.PodSelector)
			if !selector.Matches(labels.Set(pod.Labels)) {
				continue
			}
		}

		matching = append(matching, identity)
	}

	return matching
}

// Find matching clusters for an identity
func (r *TargetedReconciler) findMatchingClusters(ctx context.Context, identity *spirev1alpha1.WorkloadIdentity) []spirev1alpha1.EKSClusterRegistry {
	clusters := &spirev1alpha1.EKSClusterRegistryList{}
	r.List(ctx, clusters)

	var matching []spirev1alpha1.EKSClusterRegistry
	for _, cluster := range clusters.Items {
		if cluster.Spec.Enabled && r.shouldProcessIdentity(identity, &cluster) {
			matching = append(matching, cluster)
		}
	}
	return matching
}

// Find identities for a cluster
func (r *TargetedReconciler) findIdentitiesForCluster(ctx context.Context, cluster *spirev1alpha1.EKSClusterRegistry) []spirev1alpha1.WorkloadIdentity {
	identities := &spirev1alpha1.WorkloadIdentityList{}
	r.List(ctx, identities)

	var matching []spirev1alpha1.WorkloadIdentity
	for _, identity := range identities.Items {
		if r.shouldProcessIdentity(&identity, cluster) {
			matching = append(matching, identity)
		}
	}
	return matching
}

// Get matching pods from cluster connection
func (r *TargetedReconciler) getMatchingPods(conn *cluster.ClusterConnection, identity *spirev1alpha1.WorkloadIdentity) []*corev1.Pod {
	// Get pod selector
	podSelector, err := metav1.LabelSelectorAsSelector(identity.Spec.PodSelector)
	if err != nil {
		return nil
	}

	// Get all pods from informer
	objs := conn.PodInformer.GetStore().List()

	var pods []*corev1.Pod
	for _, obj := range objs {
		pod, ok := obj.(*corev1.Pod)
		if !ok {
			continue
		}

		if pod.Namespace != identity.Spec.Namespace {
			continue
		}

		if podSelector != nil && !podSelector.Matches(labels.Set(pod.Labels)) {
			continue
		}

		pods = append(pods, pod)
	}

	return pods
}

// Clean up orphaned entries for identity
func (r *TargetedReconciler) cleanupOrphanedEntriesForIdentity(ctx context.Context, identity *spirev1alpha1.WorkloadIdentity) {
	entries, err := r.SPIREClient.ListEntries(ctx)
	if err != nil {
		return
	}

	// Find all valid pod UIDs for this identity across all clusters
	validPodUIDs := make(map[string]bool)
	clusters := r.findMatchingClusters(ctx, identity)
	for _, cluster := range clusters {
		conn, ok := r.ClusterManager.GetClusterConnection(cluster.Name)
		if !ok {
			continue
		}

		pods := r.getMatchingPods(conn, identity)
		for _, pod := range pods {
			if pod.Status.Phase == corev1.PodRunning && pod.DeletionTimestamp == nil && pod.Spec.NodeName != "" {
				validPodUIDs[string(pod.UID)] = true
			}
		}
	}

	// Delete entries that don't have valid pods
	for _, entry := range entries {
		if !r.entryBelongsToIdentity(entry, identity) {
			continue
		}

		// Check if entry has a valid pod UID
		hasValidPod := false
		for _, selector := range entry.Selectors {
			if selector.Type == "k8s" && strings.HasPrefix(selector.Value, "pod-uid:") {
				podUID := strings.TrimPrefix(selector.Value, "pod-uid:")
				if validPodUIDs[podUID] {
					hasValidPod = true
					break
				}
			}
		}

		if !hasValidPod {
			r.SPIREClient.DeleteEntry(ctx, entry.ID)
		}
	}
}

// Clean up orphaned entries for cluster
func (r *TargetedReconciler) cleanupOrphanedEntriesForCluster(ctx context.Context, cluster *spirev1alpha1.EKSClusterRegistry) {
	// Get all entries for this cluster
	entries, err := r.SPIREClient.ListEntries(ctx)
	if err != nil {
		return
	}

	// Find all valid pod UIDs for this cluster
	validPodUIDs := make(map[string]bool)
	conn, ok := r.ClusterManager.GetClusterConnection(cluster.Name)
	if ok {
		identities := r.findIdentitiesForCluster(ctx, cluster)
		for _, identity := range identities {
			pods := r.getMatchingPods(conn, &identity)
			for _, pod := range pods {
				if pod.Status.Phase == corev1.PodRunning && pod.DeletionTimestamp == nil && pod.Spec.NodeName != "" {
					validPodUIDs[string(pod.UID)] = true
				}
			}
		}
	}

	// Delete entries that don't have valid pods
	for _, entry := range entries {
		if !r.entryBelongsToCluster(entry, cluster) {
			continue
		}

		// Check if entry has a valid pod UID
		hasValidPod := false
		for _, selector := range entry.Selectors {
			if selector.Type == "k8s" && strings.HasPrefix(selector.Value, "pod-uid:") {
				podUID := strings.TrimPrefix(selector.Value, "pod-uid:")
				if validPodUIDs[podUID] {
					hasValidPod = true
					break
				}
			}
		}

		if !hasValidPod {
			r.SPIREClient.DeleteEntry(ctx, entry.ID)
		}
	}
}

// Check if entry belongs to identity
func (r *TargetedReconciler) entryBelongsToIdentity(entry spireapi.Entry, identity *spirev1alpha1.WorkloadIdentity) bool {
	expectedHint := identity.Spec.Hint
	if expectedHint == "" {
		return strings.Contains(entry.Hint, fmt.Sprintf("workloadidentity:%s:", identity.Name))
	}
	return entry.Hint == expectedHint
}

// Check if entry belongs to cluster
func (r *TargetedReconciler) entryBelongsToCluster(entry spireapi.Entry, cluster *spirev1alpha1.EKSClusterRegistry) bool {
	expectedPrefix := "workloadidentity:"
	if len(entry.Hint) > len(expectedPrefix) {
		hintParts := entry.Hint[len(expectedPrefix):]
		if len(hintParts) > 0 {
			parts := strings.Split(hintParts, ":")
			if len(parts) >= 2 && parts[1] == cluster.Name {
				return true
			}
		}
	}
	return false
}
