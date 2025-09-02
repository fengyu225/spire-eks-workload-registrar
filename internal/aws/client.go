package aws

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/eks"
	"k8s.io/client-go/kubernetes"

	spirev1alpha1 "spire-eks-workload-registrar/api/v1alpha1"
)

type Manager struct {
	session *session.Session

	mu      sync.RWMutex
	clients map[string]*clusterClient
}

type clusterClient struct {
	kubernetes.Interface
	lastUsed time.Time
}

func NewManager() (*Manager, error) {
	sess, err := session.NewSession()
	if err != nil {
		return nil, err
	}

	return &Manager{
		session: sess,
		clients: make(map[string]*clusterClient),
	}, nil
}

func (m *Manager) GetClusterClient(
	ctx context.Context,
	cluster *spirev1alpha1.EKSClusterRegistry,
) (kubernetes.Interface, error) {
	m.mu.RLock()
	if client, ok := m.clients[cluster.Name]; ok {
		client.lastUsed = time.Now()
		m.mu.RUnlock()
		return client, nil
	}
	m.mu.RUnlock()

	client, err := m.createClusterClient(ctx, cluster)
	if err != nil {
		return nil, err
	}

	m.mu.Lock()
	m.clients[cluster.Name] = &clusterClient{
		Interface: client,
		lastUsed:  time.Now(),
	}
	m.mu.Unlock()

	return client, nil
}

func (m *Manager) createClusterClient(
	ctx context.Context,
	cluster *spirev1alpha1.EKSClusterRegistry,
) (kubernetes.Interface, error) {
	eksClient := eks.New(m.session, &aws.Config{
		Region: aws.String(cluster.Spec.Region),
	})

	describeResp, err := eksClient.DescribeCluster(&eks.DescribeClusterInput{
		Name: aws.String(cluster.Spec.ClusterName),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to describe cluster: %w", err)
	}

	// Create kubeconfig with IAM authentication using aws eks get-token
	kubeConfig, err := NewKubeConfigForCluster(cluster, describeResp.Cluster)
	if err != nil {
		return nil, fmt.Errorf("failed to create kubeconfig: %w", err)
	}

	clientConfig, err := kubeConfig.GetRestConfig()
	if err != nil {
		return nil, fmt.Errorf("failed to get rest config: %w", err)
	}

	config, err := clientConfig.ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("failed to build client config: %w", err)
	}

	return kubernetes.NewForConfig(config)
}

// GenerateKubeConfig generates a kubeconfig YAML for a given cluster
func (m *Manager) GenerateKubeConfig(
	ctx context.Context,
	cluster *spirev1alpha1.EKSClusterRegistry,
) ([]byte, error) {
	eksClient := eks.New(m.session, &aws.Config{
		Region: aws.String(cluster.Spec.Region),
	})

	describeResp, err := eksClient.DescribeCluster(&eks.DescribeClusterInput{
		Name: aws.String(cluster.Spec.ClusterName),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to describe cluster: %w", err)
	}

	// Create kubeconfig with IAM authentication using aws eks get-token
	kubeConfig, err := NewKubeConfigForCluster(cluster, describeResp.Cluster)
	if err != nil {
		return nil, fmt.Errorf("failed to create kubeconfig: %w", err)
	}

	return kubeConfig.ToYAML()
}

// CleanupStaleClients removes clients that haven't been used recently
func (m *Manager) CleanupStaleClients() {
	m.mu.Lock()
	defer m.mu.Unlock()

	cutoff := time.Now().Add(-30 * time.Minute)
	for name, client := range m.clients {
		if client.lastUsed.Before(cutoff) {
			delete(m.clients, name)
		}
	}
}
