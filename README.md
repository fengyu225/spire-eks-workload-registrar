# SPIRE EKS Workload Registrar

A Kubernetes controller that automates SPIRE workload registration for pods running across multiple Amazon EKS clusters. This operator enables centralized management of SPIFFE workload registrations for workloads distributed across a fleet of EKS clusters, enabling zero-trust security architectures.

## Table of Contents
- [Overview](#overview)
- [Architecture](#architecture)
- [How It Works](#how-it-works)
- [Custom Resource Definitions](#custom-resource-definitions)
- [Installation](#installation)
- [Configuration](#configuration)
- [Usage Examples](#usage-examples)
- [Reconciliation](#reconciliation)
- [Monitoring](#monitoring)
- [Troubleshooting](#troubleshooting)
- [Security Considerations](#security-considerations)
- [Contributing](#contributing)
- [License](#license)

## Overview

The SPIRE EKS Workload Registrar solves the challenge of managing SPIFFE identities across multiple Kubernetes clusters in AWS. It provides:

- **Automated Discovery**: Automatically discovers and connects to EKS clusters based on CRD configuration
- **Dynamic Registration**: Creates SPIRE workload registration entries for pods matching specified selectors
- **Lifecycle Management**: Handles the lifecycle of SPIFFE identities as workloads scale
- **Template-based Identity**: Flexible SPIFFE ID generation using Go templates
- **Multi-cluster Support**: Manages workloads across multiple AWS accounts
- **Efficient Reconciliation**: Uses targeted reconciliation for immediate response to changes

## Architecture

```
┌──────────────────────────────────────────────────────────────┐
│                    Management Cluster                        │
│                                                              │
│  ┌────────────────────────────────────────────────────────┐  │
│  │         SPIRE EKS Workload Registrar Controller        │  │
│  │                                                        │  │
│  │  ┌──────────────────┐      ┌────────────────────┐      │  │
│  │  │                  │      │                    │      │  │
│  │  │EKSClusterRegistry│◄─────┤ Cluster Manager    │      │  │
│  │  │   Controller     │      │                    │      │  │
│  │  └──────────────────┘      └────────┬───────────┘      │  │
│  │                                     │                  │  │
│  │  ┌──────────────────┐      ┌────────▼───────────┐      │  │
│  │  │                  │      │                    │      │  │
│  │  │ WorkloadIdentity │◄─────┤ Pod Informers      │      │  │
│  │  │   Controller     │      │ (per cluster)      │      │  │
│  │  └──────────────────┘      └────────────────────┘      │  │
│  │                                     │                  │  │
│  │  ┌──────────────────────────────────▼───────────────┐  │  │
│  │  │           SPIRE Entry State Manager              │  │  │
│  │  └─────────────────────────┬────────────────────────┘  │  │
│  └────────────────────────────┼───────────────────────────┘  │
│                               │                              │
│  ┌────────────────────────────▼─────────────────────────┐    │
│  │               SPIRE Server                           │    │
│  │         Unix Socket: /run/spire/sockets              │    │
│  └──────────────────────────────────────────────────────┘    │
└─────────────────┬──────────────────────────┬─────────────────┘
                  │                          │
                  ▼                          ▼
     ┌─────────────────────┐      ┌─────────────────────┐
     │   EKS Cluster 1     │      │   EKS Cluster 2     │
     │  Region: us-west-2  │      │  Region: us-east-1  │
     │                     │      │                     │
     │  ┌──────┐ ┌──────┐  │      │  ┌──────┐ ┌──────┐  │
     │  │ Pod  │ │ Pod  │  │      │  │ Pod  │ │ Pod  │  │
     │  └──────┘ └──────┘  │      │  └──────┘ └──────┘  │
     └─────────────────────┘      └─────────────────────┘
```

## How It Works

### 1. Cluster Registration
Define EKS clusters to monitor using `EKSClusterRegistry` resources. The controller establishes secure connections using AWS IAM authentication.

### 2. Workload Identity Definition
Create `WorkloadIdentity` resources that specify:
- Which pods to select (namespace and labels)
- How to generate SPIFFE IDs (templates)
- Which clusters to target (cluster selector)
- TTL and federation settings

### 3. Automatic Entry Creation
The controller:
- Monitors pods across all registered clusters
- Matches pods against WorkloadIdentity selectors
- Generates SPIRE entries with unique IDs based on pod metadata
- Updates entries when pod metadata changes
- Cleans up entries when pods are deleted

### 4. State Management
- Maintains current state of all SPIRE entries
- Compares desired state with actual state
- Performs minimal operations to achieve desired state

## Custom Resource Definitions

### EKSClusterRegistry

Defines an EKS cluster to monitor:

```yaml
apiVersion: eksclusterregistry.spire.spiffe.io/v1alpha1
kind: EKSClusterRegistry
metadata:
  name: workload-cluster
spec:
  clusterName: spire-agent-cluster    # EKS cluster name in AWS
  region: us-west-2                    # AWS region
  accountId: "072422391281"            # AWS account ID
  enabled: true                        # Enable/disable monitoring
  assumeRoleArn: ""                    # Optional: IAM role for cross-account access
  labels:                              # Labels for cluster selection
    environment: test
    type: workload
```

**Status Fields:**
- `lastSync`: Last successful synchronization time
- `conditions`: Current connection status
- `namespacesCount`: Number of namespaces discovered
- `entriesRegistered`: Number of SPIRE entries created

### WorkloadIdentity

Defines how to create SPIFFE identities:

```yaml
apiVersion: eksclusterregistry.spire.spiffe.io/v1alpha1
kind: WorkloadIdentity
metadata:
  name: test-app-identity
spec:
  applicationName: test-app
  namespace: test-app                  # Target namespace in workload clusters
  
  # SPIFFE ID template using Go template syntax
  spiffeIDTemplate: "spiffe://{{ .TrustDomain }}/ns/{{ .PodMeta.Namespace }}/sa/{{ .PodSpec.ServiceAccountName }}"
  
  # Optional: Custom parent ID template (defaults to node attestation)
  parentIDTemplate: ""
  
  # Pod selection
  podSelector:
    matchLabels:
      app: test-app
  
  # Cluster selection (matches EKSClusterRegistry labels)
  clusterSelector:
    matchLabels:
      type: workload
  
  # TTL settings
  ttl: 1h                              # X509-SVID TTL
  jwtTtl: 5m                           # JWT-SVID TTL
  
  # DNS names for certificates
  dnsNameTemplates:
    - "{{ .PodMeta.Name }}.{{ .PodMeta.Namespace }}.svc"
    - "{{ .PodMeta.Name }}.{{ .PodMeta.Namespace }}.svc.cluster.local"
  
  # Workload selectors for SPIRE
  workloadSelectorTemplates:
    - "k8s:ns:{{ .PodMeta.Namespace }}"
    - "k8s:sa:{{ .PodSpec.ServiceAccountName }}"
  
  # Federation
  federatesWith:
    - example.org
  
  # Special flags
  admin: false                         # Admin workload flag
  downstream: false                    # Downstream SPIRE server flag
  autoPopulateDNSNames: true           # Auto-generate DNS names
```

**Status Fields:**
- `stats.clustersTargeted`: Number of clusters where this identity applies
- `stats.namespacesFound`: Number of matching namespaces
- `stats.podsSelected`: Number of pods selected
- `stats.entriesRegistered`: Number of SPIRE entries created
- `lastReconciliation`: Last reconciliation timestamp

## Installation

### Prerequisites

1. **Kubernetes Cluster** (v1.24+) for the management plane
2. **SPIRE Server** deployed with accessible Unix socket
3. **AWS Credentials** with permissions to:
    - Describe EKS clusters
    - Generate EKS authentication tokens
    - Assume IAM roles (for cross-account access)
4. **kubectl** configured for the management cluster

### Quick Install

```bash
# Clone the repository
cd spire-eks-workload-registrar

# Install CRDs
kubectl apply -f config/crd/bases/

# Create namespace
kubectl create namespace spire-system

# Deploy the controller
kubectl apply -k config/default
```

### Production Deployment

1. **Create Configuration ConfigMap:**

```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: controller-manager-config
  namespace: spire-system
data:
  config.yaml: |
    apiVersion: eksclusterregistry.spire.spiffe.io/v1alpha1
    kind: ControllerManagerConfig
    
    # Core Configuration
    trustDomain: example.org
    clusterName: management-cluster
    spireServerSocketPath: /run/spire/sockets/server.sock
    entryIDPrefix: eks-
    gcInterval: 10m
    
    # Namespace Filtering (regex patterns)
    ignoreNamespaces:
      - "^kube-system$"
      - "^kube-public$"
      - "^kube-node-lease$"
      - "^spire-.*"
    
    # AWS Configuration
    aws:
      region: us-west-2
      accountIds:
        - "072422391281"
        - "123456789012"
      assumeRoleArn: ""  # Optional default role
    
    # Controller Settings
    syncPeriod: 30s
    leaderElection:
      leaderElect: true
      resourceName: spire-eks-workload-registrar.spire.io
      resourceNamespace: spire-system
      leaseDuration: 15s
      renewDeadline: 10s
      retryPeriod: 2s
    
    # Metrics & Health
    metrics:
      bindAddress: :8080
    health:
      healthProbeBindAddress: :8081
      readinessEndpointName: readyz
      livenessEndpointName: healthz
    
    # Webhook Configuration
    webhook:
      port: 9443
      certDir: /tmp/k8s-webhook-server/serving-certs
```

## Configuration

### Controller Configuration

| Parameter | Description | Default | Required |
|-----------|-------------|---------|----------|
| `trustDomain` | SPIFFE trust domain | - | Yes |
| `clusterName` | Management cluster identifier | `default` | No |
| `spireServerSocketPath` | Path to SPIRE server Unix socket | `/run/spire/sockets/server.sock` | No |
| `entryIDPrefix` | Prefix for generated entry IDs | `eks-workload-` | No |
| `gcInterval` | Garbage collection interval | `10m` | No |
| `ignoreNamespaces` | Regex patterns for namespaces to ignore | `[]` | No |

### AWS Configuration

| Parameter | Description | Default | Required |
|-----------|-------------|---------|----------|
| `aws.region` | Default AWS region | - | No |
| `aws.accountIds` | List of AWS account IDs to manage | `[]` | No |
| `aws.assumeRoleArn` | Default IAM role ARN for cross-account access | - | No |

### Template Variables

Available in SPIFFE ID and selector templates:

| Variable | Description | Example |
|----------|-------------|---------|
| `.TrustDomain` | SPIFFE trust domain | `example.org` |
| `.ClusterName` | EKS cluster name | `workload-cluster` |
| `.ApplicationName` | Application name from WorkloadIdentity | `test-app` |
| `.AccountID` | AWS account ID | `072422391281` |
| `.Region` | AWS region | `us-west-2` |
| `.InstanceID` | EC2 instance ID from node | `i-1234567890abcdef0` |
| `.PodMeta` | Pod metadata object | `{Name, Namespace, Labels, UID}` |
| `.PodSpec` | Pod specification | `{ServiceAccountName, NodeName}` |
| `.NodeMeta` | Node metadata | `{Name, UID, Labels}` |
| `.NodeSpec` | Node specification | `{ProviderID}` |

## Usage Examples

### Example 1: Basic Web Application

```yaml
# Register the production cluster
apiVersion: eksclusterregistry.spire.spiffe.io/v1alpha1
kind: EKSClusterRegistry
metadata:
  name: production-cluster
spec:
  clusterName: prod-eks-cluster
  region: us-west-2
  accountId: "123456789012"
  enabled: true
  labels:
    environment: production
    team: platform
---
# Define workload identity
apiVersion: eksclusterregistry.spire.spiffe.io/v1alpha1
kind: WorkloadIdentity
metadata:
  name: web-app
spec:
  applicationName: web-app
  namespace: default
  spiffeIDTemplate: "spiffe://{{ .TrustDomain }}/ns/{{ .PodMeta.Namespace }}/sa/{{ .PodSpec.ServiceAccountName }}"
  podSelector:
    matchLabels:
      app: web-app
  ttl: 1h
  jwtTtl: 5m
  workloadSelectorTemplates:
    - "k8s:ns:{{ .PodMeta.Namespace }}"
    - "k8s:pod-name:{{ .PodMeta.Name }}"
```

### Example 2: Multi-cluster Microservices

```yaml
# Define multiple clusters
apiVersion: eksclusterregistry.spire.spiffe.io/v1alpha1
kind: EKSClusterRegistry
metadata:
  name: us-west-cluster
spec:
  clusterName: microservices-west-1
  region: us-west-2
  accountId: "111111111111"
  enabled: true
  labels:
    region: us-west
    tier: compute
---
apiVersion: eksclusterregistry.spire.spiffe.io/v1alpha1
kind: EKSClusterRegistry
metadata:
  name: us-east-cluster
spec:
  clusterName: microservices-west-2
  region: us-west-2
  accountId: "222222222222"
  enabled: true
  labels:
    region: us-west
    tier: compute
---
# Identity that applies to both clusters
apiVersion: eksclusterregistry.spire.spiffe.io/v1alpha1
kind: WorkloadIdentity
metadata:
  name: api-gateway
spec:
  applicationName: api-gateway
  namespace: gateway
  spiffeIDTemplate: "spiffe://{{ .TrustDomain }}/ns/{{ .Namespace }}/sa/{{ .ServiceAccount }}"
  podSelector:
    matchLabels:
      app: api-gateway
  clusterSelector:
    matchLabels:
      tier: compute
  ttl: 30m
```

### Example 3: Cross-Account Setup

```yaml
apiVersion: eksclusterregistry.spire.spiffe.io/v1alpha1
kind: EKSClusterRegistry
metadata:
  name: dev-account-cluster
spec:
  clusterName: dev-cluster
  region: us-west-2
  accountId: "333333333333"
  assumeRoleArn: "arn:aws:iam::333333333333:role/SpireControllerRole"
  enabled: true
  labels:
    environment: development
```

## Reconciliation

The controller uses a hybrid approach for optimal performance:

### Full Reconciliation
- **Frequency**: Every 10 minutes (configurable via `gcInterval`)
- **Purpose**: Ensure eventual consistency, cleanup orphaned entries
- **Triggers**: Periodic timer, manual trigger

### Targeted Reconciliation
- **Frequency**: Immediate (event-driven)
- **Purpose**: Fast response to changes
- **Triggers**:
    - Pod lifecycle events (create/update/delete)
    - WorkloadIdentity CRD changes
    - EKSClusterRegistry CRD changes
    - Cluster connection/disconnection

### Example Logs

```
2025-09-02T08:48:28Z  INFO  WorkloadIdentity resource changed, using targeted reconciliation
2025-09-02T08:48:28Z  DEBUG Successfully created SPIRE entry {"spiffeID": "spiffe://example.org/ns/test-app/sa/test-app", "entryID": "eks-1acf83a6..."}
```

## Troubleshooting

### Check Controller Status

```bash
# Controller logs
kubectl logs -n spire-system deployment/spire-eks-workload-registrar

# Check with increased verbosity
kubectl logs -n spire-system deployment/spire-eks-workload-registrar --v=2
```

### Verify Resources

```bash
# List registered clusters
kubectl get eksclusterregistries

# Check specific cluster status
kubectl describe eksclusterregistry workload-cluster

# List workload identities
kubectl get workloadidentities

# Check identity status
kubectl describe workloadidentity test-app-identity
```

### Verify SPIRE Entries

```bash
# List all entries
kubectl exec -n spire-system spire-server-0 -- \
  /opt/spire/bin/spire-server entry list

# Show specific entry
kubectl exec -n spire-system spire-server-0 -- \
  /opt/spire/bin/spire-server entry show -entryID eks-1acf83a6...
```

### Common Issues

#### Cluster Connection Failures

```bash
# Check AWS credentials
kubectl describe pod -n spire-system -l app=spire-eks-workload-registrar

# Verify IAM permissions
aws eks describe-cluster --name spire-agent-cluster --region us-west-2
```

#### No Entries Created

1. Check pod selectors match:
```bash
kubectl get pods -n test-app -l app=test-app
```

2. Verify cluster labels match:
```bash
kubectl get eksclusterregistry -o jsonpath='{.items[*].spec.labels}'
```

3. Check namespace isn't ignored:
```bash
kubectl get cm -n spire-system controller-manager-config -o yaml | grep ignoreNamespaces -A5
```

#### Stale Entries

```bash
# Force reconciliation
kubectl annotate workloadidentity test-app-identity reconcile=now --overwrite

# Check garbage collection
kubectl logs -n spire-system deployment/spire-eks-workload-registrar | grep "garbage collection"
```

### RBAC Configuration

The controller requires these Kubernetes permissions:

```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: spire-eks-workload-registrar
rules:
- apiGroups: ["eksclusterregistry.spire.spiffe.io"]
  resources: ["eksclusterregistries", "workloadidentities"]
  verbs: ["get", "list", "watch", "update", "patch"]
- apiGroups: [""]
  resources: ["pods", "nodes", "namespaces"]
  verbs: ["get", "list", "watch"]
```

### Network Security

- Restrict access to SPIRE server Unix socket
- Use network policies to limit controller egress
- Enable audit logging for API access