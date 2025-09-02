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
	"fmt"
	"os"
	"reflect"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
)

// LoadOptionsFromFile loads controller options from a configuration file
func LoadOptionsFromFile(path string, scheme *runtime.Scheme, options *ctrl.Options, config *ControllerManagerConfig, expandEnv bool) error {
	if err := loadFile(path, scheme, config, expandEnv); err != nil {
		return err
	}

	return addOptionsFromConfigSpec(options, config.ControllerManagerConfigurationSpec)
}

// loadFile loads configuration from a file
func loadFile(path string, scheme *runtime.Scheme, config *ControllerManagerConfig, expandEnv bool) error {
	content, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("could not read file at %s: %w", path, err)
	}

	if expandEnv {
		content = []byte(os.ExpandEnv(string(content)))
	}

	codecs := serializer.NewCodecFactory(scheme)

	if err = runtime.DecodeInto(codecs.UniversalDecoder(), content, config); err != nil {
		return fmt.Errorf("could not decode file into runtime.Object: %w", err)
	}

	return nil
}

// addOptionsFromConfigSpec adds configuration options to controller options
func addOptionsFromConfigSpec(o *ctrl.Options, configSpec ControllerManagerConfigurationSpec) error {
	setLeaderElectionConfig(o, configSpec)

	if o.Cache.SyncPeriod == nil && configSpec.SyncPeriod != nil {
		o.Cache.SyncPeriod = &configSpec.SyncPeriod.Duration
	}

	if configSpec.CacheNamespace != "" {
		o.Cache.DefaultNamespaces = map[string]cache.Config{
			configSpec.CacheNamespace: {},
		}
	}

	if o.Metrics.BindAddress == "" && configSpec.Metrics.BindAddress != "" {
		o.Metrics.BindAddress = configSpec.Metrics.BindAddress
	}

	if o.HealthProbeBindAddress == "" && configSpec.Health.HealthProbeBindAddress != "" {
		o.HealthProbeBindAddress = configSpec.Health.HealthProbeBindAddress
	}

	if o.ReadinessEndpointName == "" && configSpec.Health.ReadinessEndpointName != "" {
		o.ReadinessEndpointName = configSpec.Health.ReadinessEndpointName
	}

	if o.LivenessEndpointName == "" && configSpec.Health.LivenessEndpointName != "" {
		o.LivenessEndpointName = configSpec.Health.LivenessEndpointName
	}

	return nil
}

// setLeaderElectionConfig sets leader election configuration
func setLeaderElectionConfig(o *ctrl.Options, obj ControllerManagerConfigurationSpec) {
	if obj.LeaderElection == nil {
		return
	}

	if !o.LeaderElection && obj.LeaderElection.LeaderElect != nil {
		o.LeaderElection = *obj.LeaderElection.LeaderElect
	}

	if o.LeaderElectionResourceLock == "" && obj.LeaderElection.ResourceLock != "" {
		o.LeaderElectionResourceLock = obj.LeaderElection.ResourceLock
	}

	if o.LeaderElectionNamespace == "" && obj.LeaderElection.ResourceNamespace != "" {
		o.LeaderElectionNamespace = obj.LeaderElection.ResourceNamespace
	}

	if o.LeaderElectionID == "" && obj.LeaderElection.ResourceName != "" {
		o.LeaderElectionID = obj.LeaderElection.ResourceName
	}

	if o.LeaseDuration == nil && !reflect.DeepEqual(obj.LeaderElection.LeaseDuration, metav1.Duration{}) {
		o.LeaseDuration = &obj.LeaderElection.LeaseDuration.Duration
	}

	if o.RenewDeadline == nil && !reflect.DeepEqual(obj.LeaderElection.RenewDeadline, metav1.Duration{}) {
		o.RenewDeadline = &obj.LeaderElection.RenewDeadline.Duration
	}

	if o.RetryPeriod == nil && !reflect.DeepEqual(obj.LeaderElection.RetryPeriod, metav1.Duration{}) {
		o.RetryPeriod = &obj.LeaderElection.RetryPeriod.Duration
	}
}
