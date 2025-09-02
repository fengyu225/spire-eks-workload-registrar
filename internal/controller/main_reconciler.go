package controller

import (
	"context"
	"regexp"
	"strings"
	"time"

	"github.com/spiffe/go-spiffe/v2/spiffeid"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	spirev1alpha1 "spire-eks-workload-registrar/api/v1alpha1"
	"spire-eks-workload-registrar/internal/cluster"
	"spire-eks-workload-registrar/internal/reconciler"
	"spire-eks-workload-registrar/internal/spireentry"
	"spire-eks-workload-registrar/pkg/spireapi"
)

type MainReconciler struct {
	client.Client
	Scheme *runtime.Scheme

	TrustDomain      spiffeid.TrustDomain
	ClusterName      string
	IgnoreNamespaces []*regexp.Regexp
	EntryIDPrefix    string
	GCInterval       time.Duration

	ClusterManager *cluster.Manager
	SPIREClient    spireapi.Client

	sharedReconciler reconciler.Reconciler

	targetedReconciler *TargetedReconciler
}

func (r *MainReconciler) SetupWithManager(mgr ctrl.Manager) error {
	r.targetedReconciler = NewTargetedReconciler(r)

	r.ClusterManager.SetPodReconciler(r.targetedReconciler)

	r.sharedReconciler = reconciler.New(reconciler.Config{
		Kind: "main",
		Reconcile: func(ctx context.Context) {
			r.reconcileAll(ctx)
		},
		GCInterval: r.GCInterval,
	})

	r.sharedReconciler.Trigger()

	return nil
}

func (r *MainReconciler) GetSharedReconciler() reconciler.Reconciler {
	return r.sharedReconciler
}

func (r *MainReconciler) reconcileAll(ctx context.Context) {
	log := log.FromContext(ctx)
	log.V(1).Info("Starting main reconciliation cycle")

	log.V(2).Info("Loading EKSClusterRegistry resources")
	clusters := &spirev1alpha1.EKSClusterRegistryList{}
	if err := r.List(ctx, clusters); err != nil {
		log.Error(err, "Failed to list EKSClusterRegistries")
		return
	}
	log.V(2).Info("Found EKSClusterRegistry resources", "count", len(clusters.Items))

	log.V(2).Info("Updating cluster connections")
	for _, cluster := range clusters.Items {
		log.V(3).Info("Processing cluster", "cluster", cluster.Name, "enabled", cluster.Spec.Enabled)
		if err := r.ClusterManager.AddOrUpdateCluster(ctx, &cluster); err != nil {
			log.Error(err, "Failed to update cluster connection", "cluster", cluster.Name)
			continue
		}
		log.V(3).Info("Cluster connection updated successfully", "cluster", cluster.Name)
	}

	// Remove connections for clusters that no longer exist or are disabled
	existingClusters := make(map[string]bool)
	for _, cluster := range clusters.Items {
		if cluster.Spec.Enabled {
			existingClusters[cluster.Name] = true
		}
	}

	for _, conn := range r.ClusterManager.GetAllConnections() {
		if !existingClusters[conn.Cluster.Name] {
			log.V(2).Info("Removing disabled/deleted cluster", "cluster", conn.Cluster.Name)
			r.ClusterManager.RemoveCluster(conn.Cluster.Name)
		}
	}

	log.V(2).Info("Loading WorkloadIdentity resources")
	identities := &spirev1alpha1.WorkloadIdentityList{}
	if err := r.List(ctx, identities); err != nil {
		log.Error(err, "Failed to list WorkloadIdentities")
		return
	}
	log.V(2).Info("Found WorkloadIdentity resources", "count", len(identities.Items))

	// Create entries state with prefix support
	log.V(2).Info("Creating entries state manager", "entryIDPrefix", r.EntryIDPrefix)
	state := spireentry.NewEntriesStateWithPrefix(r.EntryIDPrefix)

	// Load current entries from SPIRE
	log.V(2).Info("Loading current SPIRE entries")
	currentEntries, err := r.SPIREClient.ListEntries(ctx)
	if err != nil {
		log.Error(err, "Failed to list SPIRE entries")
		return
	}
	log.V(2).Info("Loaded current SPIRE entries", "count", len(currentEntries))

	// Add current entries to state (only those managed by this controller)
	processedEntries := 0
	for _, entry := range currentEntries {
		if r.shouldProcessEntry(entry) {
			state.AddCurrent(entry)
			processedEntries++
			log.V(4).Info("Added current entry to state", "entryID", entry.ID, "spiffeID", entry.SPIFFEID)
		} else {
			log.V(4).Info("Skipping entry (not managed by this controller)", "entryID", entry.ID, "spiffeID", entry.SPIFFEID)
		}
	}
	log.V(2).Info("Processed current entries", "total", len(currentEntries), "managed", processedEntries)

	// Process each cluster
	connections := r.ClusterManager.GetAllConnections()
	log.V(2).Info("Processing clusters", "clusterCount", len(connections))
	for _, conn := range connections {
		log.V(3).Info("Processing cluster", "cluster", conn.Cluster.Name)
		r.processCluster(ctx, conn, identities.Items, state)
	}

	// Apply changes
	log.V(2).Info("Applying entry changes")
	r.applyEntryChanges(ctx, state)

	// Update resource statuses
	log.V(2).Info("Updating resource statuses")
	r.updateStatuses(ctx, identities.Items, clusters.Items, state)

	log.V(1).Info("Main reconciliation cycle completed")
}

func (r *MainReconciler) processCluster(
	ctx context.Context,
	conn *cluster.ClusterConnection,
	identities []spirev1alpha1.WorkloadIdentity,
	state spireentry.EntriesState,
) {
	log := log.FromContext(ctx).WithValues("cluster", conn.Cluster.Name)
	log.V(2).Info("Starting cluster processing", "identityCount", len(identities))

	// Process each WorkloadIdentity
	processedIdentities := 0
	for _, identity := range identities {
		if !r.shouldProcessIdentity(&identity, conn.Cluster) {
			log.V(4).Info("Skipping identity (cluster selector mismatch)", "identity", identity.Name)
			continue
		}

		log.V(3).Info("Processing WorkloadIdentity", "identity", identity.Name, "namespace", identity.Spec.Namespace)
		r.processWorkloadIdentity(ctx, &identity, conn, state)
		processedIdentities++
	}
	log.V(2).Info("Completed cluster processing", "processedIdentities", processedIdentities)
}

func (r *MainReconciler) processWorkloadIdentity(
	ctx context.Context,
	identity *spirev1alpha1.WorkloadIdentity,
	conn *cluster.ClusterConnection,
	state spireentry.EntriesState,
) {
	log := log.FromContext(ctx).WithValues(
		"identity", identity.Name,
		"cluster", conn.Cluster.Name,
		"namespace", identity.Spec.Namespace,
	)
	log.V(3).Info("Starting WorkloadIdentity processing")

	// Check if namespace should be ignored
	if r.isNamespaceIgnored(identity.Spec.Namespace) {
		log.V(2).Info("Namespace is ignored, skipping processing")
		return
	}

	// Get pods from informer cache
	log.V(4).Info("Getting pods from informer cache")
	pods, err := r.getPodsFromInformer(conn, identity)
	if err != nil {
		log.Error(err, "Failed to get pods from informer")
		return
	}

	log.V(3).Info("Found matching pods", "count", len(pods))

	// Process each pod
	processedPods := 0
	skippedPods := 0
	for _, pod := range pods {
		// Skip non-running and terminating pods
		if pod.Status.Phase != corev1.PodRunning || pod.DeletionTimestamp != nil {
			log.V(4).Info("Skipping non-running pod", "pod", pod.Name, "phase", pod.Status.Phase)
			skippedPods++
			continue
		}

		// Also check if node name is set
		if pod.Spec.NodeName == "" {
			log.V(4).Info("Skipping pod without node", "pod", pod.Name)
			skippedPods++
			continue
		}

		// Get node
		log.V(4).Info("Getting node information", "pod", pod.Name, "node", pod.Spec.NodeName)
		node, err := conn.K8sClient.CoreV1().Nodes().Get(ctx, pod.Spec.NodeName, metav1.GetOptions{})
		if err != nil {
			log.Error(err, "Failed to get node", "pod", pod.Name, "node", pod.Spec.NodeName)
			skippedPods++
			continue
		}

		// Render entry
		log.V(4).Info("Rendering SPIRE entry", "pod", pod.Name)
		entry, err := spireentry.RenderEntry(
			identity,
			conn.Cluster,
			pod,
			node,
			r.TrustDomain.String(),
		)
		if err != nil {
			log.Error(err, "Failed to render entry", "pod", pod.Name)
			skippedPods++
			continue
		}

		// Add to state
		log.V(4).Info("Adding entry to declared state", "pod", pod.Name, "spiffeID", entry.SPIFFEID)
		state.AddDeclared(*entry, identity, conn.Cluster, pod)
		processedPods++
	}
	log.V(3).Info("Completed WorkloadIdentity processing", "processedPods", processedPods, "skippedPods", skippedPods)
}

func (r *MainReconciler) getPodsFromInformer(
	conn *cluster.ClusterConnection,
	identity *spirev1alpha1.WorkloadIdentity,
) ([]*corev1.Pod, error) {
	// Get pod selector
	podSelector, err := metav1.LabelSelectorAsSelector(identity.Spec.PodSelector)
	if err != nil {
		return nil, err
	}

	// Get all pods from informer
	objs := conn.PodInformer.GetStore().List()

	var pods []*corev1.Pod
	for _, obj := range objs {
		pod, ok := obj.(*corev1.Pod)
		if !ok {
			continue
		}

		// Check namespace
		if pod.Namespace != identity.Spec.Namespace {
			continue
		}

		// Check selector
		if podSelector != nil && !podSelector.Matches(labels.Set(pod.Labels)) {
			continue
		}

		pods = append(pods, pod)
	}

	return pods, nil
}

func (r *MainReconciler) applyEntryChanges(ctx context.Context, state spireentry.EntriesState) {
	log := log.FromContext(ctx)
	log.V(2).Info("Calculating entry changes")

	toCreate, toUpdate, toDelete := state.GetChanges()
	log.V(2).Info("Entry changes calculated", "toCreate", len(toCreate), "toUpdate", len(toUpdate), "toDelete", len(toDelete))

	var createErrors, updateErrors, deleteErrors int

	// Delete entries
	if len(toDelete) > 0 {
		log.Info("Deleting SPIRE entries", "count", len(toDelete))
		for i, entry := range toDelete {
			log.V(3).Info("Deleting entry", "index", i+1, "total", len(toDelete), "id", entry.ID, "spiffeID", entry.SPIFFEID)
			if err := r.SPIREClient.DeleteEntry(ctx, entry.ID); err != nil {
				log.Error(err, "Failed to delete entry", "id", entry.ID, "spiffeID", entry.SPIFFEID)
				deleteErrors++
			} else {
				log.V(3).Info("Successfully deleted entry", "id", entry.ID)
			}
		}
		log.Info("Deleted entries", "success", len(toDelete)-deleteErrors, "failed", deleteErrors)
	}

	// Create entries
	if len(toCreate) > 0 {
		log.Info("Creating SPIRE entries", "count", len(toCreate))
		for i, entry := range toCreate {
			log.V(3).Info("Creating entry", "index", i+1, "total", len(toCreate), "spiffeID", entry.SPIFFEID, "parentID", entry.ParentID)
			if err := r.SPIREClient.CreateEntry(ctx, entry); err != nil {
				log.Error(err, "Failed to create entry", "spiffeID", entry.SPIFFEID)
				createErrors++
			} else {
				log.V(3).Info("Successfully created entry", "spiffeID", entry.SPIFFEID)
			}
		}
		log.Info("Created entries", "success", len(toCreate)-createErrors, "failed", createErrors)
	}

	// Update entries
	if len(toUpdate) > 0 {
		log.Info("Updating SPIRE entries", "count", len(toUpdate))
		for i, entry := range toUpdate {
			log.V(3).Info("Updating entry", "index", i+1, "total", len(toUpdate), "id", entry.ID, "spiffeID", entry.SPIFFEID)
			if err := r.SPIREClient.UpdateEntry(ctx, entry); err != nil {
				log.Error(err, "Failed to update entry", "id", entry.ID, "spiffeID", entry.SPIFFEID)
				updateErrors++
			} else {
				log.V(3).Info("Successfully updated entry", "id", entry.ID)
			}
		}
		log.Info("Updated entries", "success", len(toUpdate)-updateErrors, "failed", updateErrors)
	}

	if len(toCreate) == 0 && len(toUpdate) == 0 && len(toDelete) == 0 {
		log.V(2).Info("No entry changes required")
	} else {
		totalErrors := createErrors + updateErrors + deleteErrors
		if totalErrors > 0 {
			log.Info("Entry changes completed with errors",
				"totalErrors", totalErrors,
				"createErrors", createErrors,
				"updateErrors", updateErrors,
				"deleteErrors", deleteErrors)
		} else {
			log.Info("All entry changes completed successfully")
		}
	}
}

func (r *MainReconciler) shouldProcessIdentity(
	identity *spirev1alpha1.WorkloadIdentity,
	cluster *spirev1alpha1.EKSClusterRegistry,
) bool {
	if identity.Spec.ClusterSelector == nil {
		return true
	}

	selector, err := metav1.LabelSelectorAsSelector(identity.Spec.ClusterSelector)
	if err != nil {
		return false
	}

	return selector.Matches(labels.Set(cluster.Spec.Labels))
}

func (r *MainReconciler) shouldProcessEntry(entry spireapi.Entry) bool {
	if r.EntryIDPrefix == "" {
		return true
	}
	return strings.HasPrefix(entry.ID, r.EntryIDPrefix)
}

func (r *MainReconciler) isNamespaceIgnored(namespace string) bool {
	for _, regex := range r.IgnoreNamespaces {
		if regex.MatchString(namespace) {
			return true
		}
	}
	return false
}

func (r *MainReconciler) updateStatuses(ctx context.Context, identities []spirev1alpha1.WorkloadIdentity, clusters []spirev1alpha1.EKSClusterRegistry, state spireentry.EntriesState) {
	log := log.FromContext(ctx)

	// Calculate statistics from state
	toCreate, toUpdate, toDelete := state.GetChanges()
	totalEntries := len(toCreate) + len(toUpdate) + len(toDelete)

	// Update WorkloadIdentity statuses
	for _, identity := range identities {
		// Calculate stats for this identity
		stats := r.calculateIdentityStats(ctx, &identity, clusters, state)

		// Update status
		identity.Status.Stats = stats
		now := metav1.Now()
		identity.Status.LastReconciliation = &now

		if err := r.Status().Update(ctx, &identity); err != nil {
			log.Error(err, "Failed to update WorkloadIdentity status", "name", identity.Name)
		} else {
			log.V(3).Info("Updated WorkloadIdentity status", "name", identity.Name,
				"clustersTargeted", stats.ClustersTargeted,
				"podsSelected", stats.PodsSelected,
				"entriesRegistered", stats.EntriesRegistered)
		}
	}

	// Update EKSClusterRegistry statuses
	for _, cluster := range clusters {
		// Calculate stats for this cluster
		entriesRegistered := r.calculateClusterEntries(ctx, &cluster, state)

		// Update status
		if cluster.Spec.Enabled {
			now := metav1.Now()
			cluster.Status.LastSync = &now
		}
		cluster.Status.EntriesRegistered = entriesRegistered

		// Update conditions
		connected := r.ClusterManager.IsConnected(cluster.Name)
		r.updateClusterConditions(&cluster, connected)

		if err := r.Status().Update(ctx, &cluster); err != nil {
			log.Error(err, "Failed to update EKSClusterRegistry status", "name", cluster.Name)
		} else {
			log.V(3).Info("Updated EKSClusterRegistry status", "name", cluster.Name,
				"connected", connected,
				"entriesRegistered", entriesRegistered)
		}
	}

	log.V(2).Info("Status updates completed", "totalEntries", totalEntries)
}

func (r *MainReconciler) calculateIdentityStats(ctx context.Context, identity *spirev1alpha1.WorkloadIdentity, clusters []spirev1alpha1.EKSClusterRegistry, state spireentry.EntriesState) spirev1alpha1.WorkloadIdentityStats {
	stats := spirev1alpha1.WorkloadIdentityStats{}

	// Count targeted clusters
	for _, cluster := range clusters {
		if cluster.Spec.Enabled && r.shouldProcessIdentity(identity, &cluster) {
			stats.ClustersTargeted++
		}
	}

	// For now, we'll use simplified calculations
	// In a full implementation, you'd track per-identity statistics
	connections := r.ClusterManager.GetAllConnections()
	for _, conn := range connections {
		if r.shouldProcessIdentity(identity, conn.Cluster) {
			pods, err := r.getPodsFromInformer(conn, identity)
			if err == nil {
				for _, pod := range pods {
					if pod.Status.Phase == corev1.PodRunning && pod.DeletionTimestamp == nil && pod.Spec.NodeName != "" {
						stats.PodsSelected++
					}
				}
			}
		}
	}

	// Note: In a production implementation, you'd want to track entries per identity
	// This is a simplified calculation
	toCreate, toUpdate, _ := state.GetChanges()
	stats.EntriesRegistered = len(toCreate) + len(toUpdate)

	return stats
}

func (r *MainReconciler) calculateClusterEntries(ctx context.Context, cluster *spirev1alpha1.EKSClusterRegistry, state spireentry.EntriesState) int {
	// In a full implementation, you'd track entries per cluster
	// This is a simplified calculation
	toCreate, toUpdate, _ := state.GetChanges()
	return len(toCreate) + len(toUpdate)
}

func (r *MainReconciler) updateClusterConditions(cluster *spirev1alpha1.EKSClusterRegistry, connected bool) {
	now := metav1.Now()

	// Find or create Ready condition
	var readyCondition *metav1.Condition
	for i := range cluster.Status.Conditions {
		if cluster.Status.Conditions[i].Type == "Ready" {
			readyCondition = &cluster.Status.Conditions[i]
			break
		}
	}

	if readyCondition == nil {
		cluster.Status.Conditions = append(cluster.Status.Conditions, metav1.Condition{
			Type: "Ready",
		})
		readyCondition = &cluster.Status.Conditions[len(cluster.Status.Conditions)-1]
	}

	if connected {
		readyCondition.Status = metav1.ConditionTrue
		readyCondition.Reason = "Connected"
		readyCondition.Message = "Successfully connected to cluster"
	} else {
		readyCondition.Status = metav1.ConditionFalse
		readyCondition.Reason = "NotConnected"
		readyCondition.Message = "Unable to connect to cluster"
	}
	readyCondition.LastTransitionTime = now
}

func (r *MainReconciler) Trigger() {
	if r.sharedReconciler != nil {
		r.sharedReconciler.Trigger()
	}
}
