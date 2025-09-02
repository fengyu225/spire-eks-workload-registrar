/*
Copyright 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package v1alpha1

import (
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	configv1alpha1 "k8s.io/component-base/config/v1alpha1"
)

//+kubebuilder:object:root=true

// ControllerManagerConfig is the configuration for the EKS workload registrar
type ControllerManagerConfig struct {
	metav1.TypeMeta `json:",inline"`

	// ControllerManagerConfigurationSpec returns the configurations for controllers
	ControllerManagerConfigurationSpec `json:",inline"`

	// ClusterName is the name of the management cluster
	ClusterName string `json:"clusterName"`

	// TrustDomain is the SPIFFE trust domain
	TrustDomain string `json:"trustDomain"`

	// IgnoreNamespaces are namespaces to ignore
	IgnoreNamespaces []string `json:"ignoreNamespaces"`

	// SPIREServerSocketPath is the path to the SPIRE Server API socket
	SPIREServerSocketPath string `json:"spireServerSocketPath"`

	// EntryIDPrefix is the prefix for entry IDs
	EntryIDPrefix string `json:"entryIDPrefix,omitempty"`

	// GCInterval is how often to reconcile when idle
	GCInterval time.Duration `json:"gcInterval,omitempty"`

	// AWS specific configuration
	AWSConfig AWSConfig `json:"aws,omitempty"`
}

// AWSConfig contains AWS-specific configuration
type AWSConfig struct {
	// Region is the default AWS region
	Region string `json:"region,omitempty"`

	// AccountIDs is a list of AWS account IDs to manage
	AccountIDs []string `json:"accountIds,omitempty"`

	// AssumeRoleARN is the default role to assume
	AssumeRoleARN string `json:"assumeRoleArn,omitempty"`
}

// ControllerManagerConfigurationSpec defines the desired state
type ControllerManagerConfigurationSpec struct {
	// SyncPeriod determines the minimum frequency at which watched resources are reconciled
	SyncPeriod *metav1.Duration `json:"syncPeriod,omitempty"`

	// LeaderElection is the LeaderElection config
	LeaderElection *configv1alpha1.LeaderElectionConfiguration `json:"leaderElection,omitempty"`

	// CacheNamespace restricts the manager's cache to watch objects in desired namespace
	CacheNamespace string `json:"cacheNamespace,omitempty"`

	// Metrics contains the controller metrics configuration
	Metrics ControllerMetrics `json:"metrics,omitempty"`

	// Health contains the controller health configuration
	Health ControllerHealth `json:"health,omitempty"`

	// Webhook contains the controllers webhook configuration
	Webhook ControllerWebhook `json:"webhook,omitempty"`
}

// ControllerMetrics defines the metrics configs
type ControllerMetrics struct {
	// BindAddress is the TCP address for serving prometheus metrics
	BindAddress string `json:"bindAddress,omitempty"`
}

// ControllerHealth defines the health configs
type ControllerHealth struct {
	// HealthProbeBindAddress is the TCP address for serving health probes
	HealthProbeBindAddress string `json:"healthProbeBindAddress,omitempty"`

	// ReadinessEndpointName, defaults to "readyz"
	ReadinessEndpointName string `json:"readinessEndpointName,omitempty"`

	// LivenessEndpointName, defaults to "healthz"
	LivenessEndpointName string `json:"livenessEndpointName,omitempty"`
}

// ControllerWebhook defines the webhook server for the controller
type ControllerWebhook struct {
	// Port is the port that the webhook server serves at
	Port *int `json:"port,omitempty"`

	// Host is the hostname that the webhook server binds to
	Host string `json:"host,omitempty"`

	// CertDir is the directory that contains the server key and certificate
	CertDir string `json:"certDir,omitempty"`
}

func init() {
	SchemeBuilder.Register(&ControllerManagerConfig{})
}
