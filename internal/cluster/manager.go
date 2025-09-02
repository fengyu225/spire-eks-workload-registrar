package cluster

import (
	"context"
	"fmt"
	"sync"
	"time"

	"spire-eks-workload-registrar/internal/aws"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
	"sigs.k8s.io/controller-runtime/pkg/log"

	spirev1alpha1 "spire-eks-workload-registrar/api/v1alpha1"
	"spire-eks-workload-registrar/internal/reconciler"
)

// PodReconciler interface for handling pod events
type PodReconciler interface {
	ReconcileSinglePod(ctx context.Context, pod *corev1.Pod, cluster *spirev1alpha1.EKSClusterRegistry, action string)
}

type Manager struct {
	awsManager *aws.Manager

	mu       sync.RWMutex
	clusters map[string]*ClusterConnection

	// Triggered to notify about pod changes
	triggerer reconciler.Triggerer

	// Pod reconciler for targeted reconciliation
	podReconciler PodReconciler
}

type ClusterConnection struct {
	Cluster       *spirev1alpha1.EKSClusterRegistry
	K8sClient     kubernetes.Interface
	PodInformer   cache.SharedIndexInformer
	StopCh        chan struct{}
	LastConnected time.Time
}

func NewManager(triggerer reconciler.Triggerer) (*Manager, error) {
	awsManager, err := aws.NewManager()
	if err != nil {
		return nil, err
	}

	return &Manager{
		awsManager: awsManager,
		clusters:   make(map[string]*ClusterConnection),
		triggerer:  triggerer,
	}, nil
}

// SetPodReconciler sets the pod reconciler for targeted reconciliation
func (m *Manager) SetPodReconciler(reconciler PodReconciler) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.podReconciler = reconciler
}

func (m *Manager) AddOrUpdateCluster(ctx context.Context, cluster *spirev1alpha1.EKSClusterRegistry) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	log := log.FromContext(ctx).WithValues("cluster", cluster.Name)
	log.V(1).Info("Processing cluster update", "enabled", cluster.Spec.Enabled, "region", cluster.Spec.Region, "clusterName", cluster.Spec.ClusterName)

	if existing, ok := m.clusters[cluster.Name]; ok {
		log.V(2).Info("Cluster already exists, checking for changes")
		if existing.Cluster.Spec.ClusterName == cluster.Spec.ClusterName &&
			existing.Cluster.Spec.Region == cluster.Spec.Region &&
			existing.Cluster.Spec.AssumeRoleARN == cluster.Spec.AssumeRoleARN {
			log.V(2).Info("No significant changes detected, updating cluster reference only")
			existing.Cluster = cluster.DeepCopy()
			return nil
		}

		log.Info("Cluster configuration changed, reconnecting")
		m.stopClusterConnection(existing)
		delete(m.clusters, cluster.Name)
	}

	if !cluster.Spec.Enabled {
		log.Info("Cluster is disabled, skipping connection setup")
		return nil
	}

	log.V(1).Info("Creating new cluster connection")
	conn, err := m.createClusterConnection(ctx, cluster)
	if err != nil {
		log.Error(err, "Failed to create cluster connection")
		return fmt.Errorf("failed to create cluster connection: %w", err)
	}

	m.clusters[cluster.Name] = conn

	log.V(1).Info("Starting pod informer for cluster")
	go conn.PodInformer.Run(conn.StopCh)

	log.V(2).Info("Waiting for pod informer cache to sync")
	if !cache.WaitForCacheSync(conn.StopCh, conn.PodInformer.HasSynced) {
		log.Error(nil, "Failed to sync pod cache")
		return fmt.Errorf("failed to sync pod cache")
	}
	log.V(2).Info("Pod informer cache synced successfully")

	log.Info("Successfully connected to cluster")
	return nil
}

func (m *Manager) RemoveCluster(clusterName string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	log.Log.V(1).Info("Removing cluster", "cluster", clusterName)
	if conn, ok := m.clusters[clusterName]; ok {
		log.Log.V(2).Info("Stopping cluster connection", "cluster", clusterName)
		m.stopClusterConnection(conn)
		delete(m.clusters, clusterName)
		log.Log.Info("Cluster removed successfully", "cluster", clusterName)
	} else {
		log.Log.V(2).Info("Cluster not found for removal", "cluster", clusterName)
	}
}

func (m *Manager) GetClusterConnection(clusterName string) (*ClusterConnection, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	conn, ok := m.clusters[clusterName]
	return conn, ok
}

func (m *Manager) GetAllConnections() []*ClusterConnection {
	m.mu.RLock()
	defer m.mu.RUnlock()

	log.Log.V(3).Info("Getting all cluster connections", "count", len(m.clusters))
	connections := make([]*ClusterConnection, 0, len(m.clusters))
	for name, conn := range m.clusters {
		log.Log.V(4).Info("Including cluster connection", "cluster", name, "lastConnected", conn.LastConnected)
		connections = append(connections, conn)
	}
	return connections
}

func (m *Manager) IsConnected(clusterName string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()

	_, ok := m.clusters[clusterName]
	return ok
}

func (m *Manager) StartCleanupLoop(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Minute)
	go func() {
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				log.Log.V(2).Info("Cleanup loop stopping due to context cancellation")
				return
			case <-ticker.C:
				log.Log.V(3).Info("Running periodic cleanup of stale AWS clients")
				m.awsManager.CleanupStaleClients()
			}
		}
	}()
}

func (m *Manager) createClusterConnection(ctx context.Context, cluster *spirev1alpha1.EKSClusterRegistry) (*ClusterConnection, error) {
	k8sClient, err := m.createK8sClient(ctx, cluster)
	if err != nil {
		return nil, err
	}

	podInformer := m.createPodInformer(k8sClient, cluster.Name)

	conn := &ClusterConnection{
		Cluster:       cluster.DeepCopy(),
		K8sClient:     k8sClient,
		PodInformer:   podInformer,
		StopCh:        make(chan struct{}),
		LastConnected: time.Now(),
	}

	return conn, nil
}

func (m *Manager) createK8sClient(ctx context.Context, cluster *spirev1alpha1.EKSClusterRegistry) (kubernetes.Interface, error) {
	return m.awsManager.GetClusterClient(ctx, cluster)
}

func (m *Manager) createPodInformer(k8sClient kubernetes.Interface, clusterName string) cache.SharedIndexInformer {
	listWatcher := cache.NewListWatchFromClient(
		k8sClient.CoreV1().RESTClient(),
		"pods",
		corev1.NamespaceAll,
		fields.Everything(),
	)

	informer := cache.NewSharedIndexInformer(
		listWatcher,
		&corev1.Pod{},
		30*time.Second,
		cache.Indexers{
			cache.NamespaceIndex: cache.MetaNamespaceIndexFunc,
		},
	)

	informer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			m.handlePodChange("add", obj, clusterName)
		},
		UpdateFunc: func(oldObj, newObj interface{}) {
			m.handlePodChange("update", newObj, clusterName)
		},
		DeleteFunc: func(obj interface{}) {
			m.handlePodChange("delete", obj, clusterName)
		},
	})

	return informer
}

func (m *Manager) handlePodChange(action string, obj interface{}, clusterName string) {
	pod, ok := obj.(*corev1.Pod)
	if !ok {
		tombstone, ok := obj.(cache.DeletedFinalStateUnknown)
		if !ok {
			log.Log.V(4).Info("Received unknown object type in pod change handler")
			return
		}
		pod, ok = tombstone.Obj.(*corev1.Pod)
		if !ok {
			log.Log.V(4).Info("Tombstone object is not a pod")
			return
		}
		log.Log.V(3).Info("Processing tombstone pod", "namespace", pod.Namespace, "name", pod.Name)
	}

	log.Log.V(2).Info("Pod event received",
		"action", action,
		"namespace", pod.Namespace,
		"name", pod.Name,
		"phase", pod.Status.Phase,
		"node", pod.Spec.NodeName,
		"cluster", clusterName,
	)

	// Use targeted reconciliation if available
	if m.podReconciler != nil {
		m.mu.RLock()
		conn, ok := m.clusters[clusterName]
		m.mu.RUnlock()

		if ok {
			ctx := context.Background()
			log.Log.V(3).Info("Using targeted reconciliation for pod change", "action", action, "pod", pod.Namespace+"/"+pod.Name, "cluster", clusterName)
			m.podReconciler.ReconcileSinglePod(ctx, pod, conn.Cluster, action)
		}
	} else if m.triggerer != nil {
		// Fallback to full reconciliation
		log.Log.V(3).Info("Triggering full reconciliation due to pod change", "action", action, "pod", pod.Namespace+"/"+pod.Name)
		m.triggerer.Trigger()
	} else {
		log.Log.V(4).Info("No reconciler available, skipping reconciliation")
	}
}

func (m *Manager) stopClusterConnection(conn *ClusterConnection) {
	close(conn.StopCh)
}

func (m *Manager) Shutdown() {
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, conn := range m.clusters {
		m.stopClusterConnection(conn)
	}
	m.clusters = make(map[string]*ClusterConnection)
}
