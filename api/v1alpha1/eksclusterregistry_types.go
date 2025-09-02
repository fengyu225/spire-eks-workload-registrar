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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// EKSClusterRegistrySpec defines the desired state of EKSClusterRegistry
type EKSClusterRegistrySpec struct {
	// ClusterName is the name of the EKS cluster
	ClusterName string `json:"clusterName"`

	// Region is the AWS region where the cluster resides
	Region string `json:"region"`

	// AccountID is the AWS account ID
	AccountID string `json:"accountId"`

	// Enabled determines if this cluster should be processed
	Enabled bool `json:"enabled"`

	// AssumeRoleARN is the optional IAM role to assume for this cluster
	// +optional
	AssumeRoleARN string `json:"assumeRoleArn,omitempty"`

	// Labels for cluster selection
	// +optional
	Labels map[string]string `json:"labels,omitempty"`
}

// EKSClusterRegistryStatus defines the observed state of EKSClusterRegistry
type EKSClusterRegistryStatus struct {
	// LastSync is the last time the cluster was successfully synced
	// +optional
	LastSync *metav1.Time `json:"lastSync,omitempty"`

	// Conditions represent the latest available observations
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// NamespaceCount is the number of namespaces found
	// +optional
	NamespaceCount int `json:"namespaceCount,omitempty"`

	// EntriesRegistered is the number of SPIRE entries registered
	// +optional
	EntriesRegistered int `json:"entriesRegistered,omitempty"`
}

//+kubebuilder:object:root=true
//+kubebuilder:subresource:status
//+kubebuilder:resource:scope=Cluster
//+kubebuilder:printcolumn:name="Cluster",type=string,JSONPath=`.spec.clusterName`
//+kubebuilder:printcolumn:name="Region",type=string,JSONPath=`.spec.region`
//+kubebuilder:printcolumn:name="Enabled",type=boolean,JSONPath=`.spec.enabled`
//+kubebuilder:printcolumn:name="Last Sync",type=string,JSONPath=`.status.lastSync`

// EKSClusterRegistry is the Schema for the eksclusterregistries API
type EKSClusterRegistry struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   EKSClusterRegistrySpec   `json:"spec,omitempty"`
	Status EKSClusterRegistryStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// EKSClusterRegistryList contains a list of EKSClusterRegistry
type EKSClusterRegistryList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []EKSClusterRegistry `json:"items"`
}

func init() {
	SchemeBuilder.Register(&EKSClusterRegistry{}, &EKSClusterRegistryList{})
}
