package aws

import (
	"encoding/base64"
	"fmt"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/service/eks"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/clientcmd/api"

	spirev1alpha1 "spire-eks-workload-registrar/api/v1alpha1"
)

// KubeConfig represents Kubernetes configuration with IAM authentication
type KubeConfig struct {
	config *api.Config
}

// NewKubeConfigForCluster creates a new Kubernetes configuration for IAM authentication
func NewKubeConfigForCluster(cluster *spirev1alpha1.EKSClusterRegistry, eksCluster *eks.Cluster) (*KubeConfig, error) {
	caData, err := base64.StdEncoding.DecodeString(aws.StringValue(eksCluster.CertificateAuthority.Data))
	if err != nil {
		return nil, fmt.Errorf("failed to decode CA data: %w", err)
	}

	config := &api.Config{
		APIVersion: "v1",
		Kind:       "Config",
		Clusters: map[string]*api.Cluster{
			cluster.Spec.ClusterName: {
				Server:                   aws.StringValue(eksCluster.Endpoint),
				CertificateAuthorityData: caData,
			},
		},
		Contexts: map[string]*api.Context{
			cluster.Spec.ClusterName: {
				Cluster:  cluster.Spec.ClusterName,
				AuthInfo: "spire-server-sa",
			},
		},
		CurrentContext: cluster.Spec.ClusterName,
		AuthInfos: map[string]*api.AuthInfo{
			"spire-server-sa": {
				Exec: &api.ExecConfig{
					APIVersion:      "client.authentication.k8s.io/v1beta1",
					Command:         "aws",
					Args:            buildAwsArgs(cluster),
					InteractiveMode: api.NeverExecInteractiveMode,
				},
			},
		},
	}

	return &KubeConfig{config: config}, nil
}

// buildAwsArgs builds the arguments for aws eks get-token command
func buildAwsArgs(cluster *spirev1alpha1.EKSClusterRegistry) []string {
	args := []string{
		"eks",
		"get-token",
		"--cluster-name",
		cluster.Spec.ClusterName,
	}

	if cluster.Spec.Region != "" {
		args = append(args, "--region", cluster.Spec.Region)
	}

	if cluster.Spec.AssumeRoleARN != "" {
		args = append(args, "--role-arn", cluster.Spec.AssumeRoleARN)
	}

	return args
}

// GetRestConfig returns a rest.Config from the kubeconfig
func (k *KubeConfig) GetRestConfig() (clientcmd.ClientConfig, error) {
	clientConfig := clientcmd.NewDefaultClientConfig(*k.config, &clientcmd.ConfigOverrides{})
	return clientConfig, nil
}

// ToYAML returns the kubeconfig as YAML bytes
func (k *KubeConfig) ToYAML() ([]byte, error) {
	return clientcmd.Write(*k.config)
}
