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

// WorkloadIdentitySpec defines the desired state of WorkloadIdentity
type WorkloadIdentitySpec struct {
	// ApplicationName identifies the application
	ApplicationName string `json:"applicationName"`

	// Namespace is the target namespace in workload clusters
	Namespace string `json:"namespace"`

	// SPIFFEIDTemplate is the SPIFFE ID template
	SPIFFEIDTemplate string `json:"spiffeIDTemplate"`

	// ParentIDTemplate is the parent ID template (optional)
	// +optional
	ParentIDTemplate string `json:"parentIDTemplate,omitempty"`

	// PodSelector selects the pods that are targeted
	// +optional
	PodSelector *metav1.LabelSelector `json:"podSelector,omitempty"`

	// TTL is the TTL for X509 SVIDs
	// +optional
	TTL metav1.Duration `json:"ttl,omitempty"`

	// JWTTTL is the TTL for JWT SVIDs
	// +optional
	JWTTTL metav1.Duration `json:"jwtTtl,omitempty"`

	// DNSNameTemplates are templates for DNS names
	// +optional
	DNSNameTemplates []string `json:"dnsNameTemplates,omitempty"`

	// WorkloadSelectorTemplates are templates for workload selectors
	// +optional
	WorkloadSelectorTemplates []string `json:"workloadSelectorTemplates,omitempty"`

	// FederatesWith is a list of trust domains to federate with
	// +optional
	FederatesWith []string `json:"federatesWith,omitempty"`

	// Admin indicates if this is an admin workload
	// +optional
	Admin bool `json:"admin,omitempty"`

	// Downstream indicates if this is a downstream SPIRE server
	// +optional
	Downstream bool `json:"downstream,omitempty"`

	// AutoPopulateDNSNames enables automatic DNS name population
	// +optional
	AutoPopulateDNSNames bool `json:"autoPopulateDNSNames,omitempty"`

	// ClusterSelector selects which clusters to apply this to
	// +optional
	ClusterSelector *metav1.LabelSelector `json:"clusterSelector,omitempty"`

	// Hint is an optional hint for the entry
	// +optional
	Hint string `json:"hint,omitempty"`
}

// WorkloadIdentityStats contains registration statistics
type WorkloadIdentityStats struct {
	// ClustersTargeted is the number of clusters targeted
	ClustersTargeted int `json:"clustersTargeted"`

	// NamespacesFound is the number of namespaces found
	NamespacesFound int `json:"namespacesFound"`

	// PodsSelected is the number of pods selected
	PodsSelected int `json:"podsSelected"`

	// EntriesRegistered is the number of entries registered
	EntriesRegistered int `json:"entriesRegistered"`

	// EntryFailures is the number of entry failures
	EntryFailures int `json:"entryFailures"`
}

// WorkloadIdentityStatus defines the observed state of WorkloadIdentity
type WorkloadIdentityStatus struct {
	// Stats contains registration statistics
	// +optional
	Stats WorkloadIdentityStats `json:"stats,omitempty"`

	// LastReconciliation is the last reconciliation time
	// +optional
	LastReconciliation *metav1.Time `json:"lastReconciliation,omitempty"`
}

//+kubebuilder:object:root=true
//+kubebuilder:subresource:status
//+kubebuilder:resource:scope=Cluster
//+kubebuilder:printcolumn:name="Application",type=string,JSONPath=`.spec.applicationName`
//+kubebuilder:printcolumn:name="Namespace",type=string,JSONPath=`.spec.namespace`
//+kubebuilder:printcolumn:name="Pods",type=integer,JSONPath=`.status.stats.podsSelected`
//+kubebuilder:printcolumn:name="Entries",type=integer,JSONPath=`.status.stats.entriesRegistered`

// WorkloadIdentity is the Schema for the workloadidentities API
type WorkloadIdentity struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   WorkloadIdentitySpec   `json:"spec,omitempty"`
	Status WorkloadIdentityStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// WorkloadIdentityList contains a list of WorkloadIdentity
type WorkloadIdentityList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []WorkloadIdentity `json:"items"`
}

func init() {
	SchemeBuilder.Register(&WorkloadIdentity{}, &WorkloadIdentityList{})
}
