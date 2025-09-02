package main

import (
	"crypto/tls"
	"flag"
	"fmt"
	"os"
	"regexp"
	"time"

	"github.com/spiffe/go-spiffe/v2/spiffeid"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/kubernetes"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	_ "k8s.io/client-go/plugin/pkg/client/auth"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/webhook"

	spirev1alpha1 "spire-eks-workload-registrar/api/v1alpha1"
	"spire-eks-workload-registrar/internal/cluster"
	"spire-eks-workload-registrar/internal/controller"
	"spire-eks-workload-registrar/pkg/spireapi"
	"spire-eks-workload-registrar/pkg/webhookmanager"
	//+kubebuilder:scaffold:imports
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(spirev1alpha1.AddToScheme(scheme))
	//+kubebuilder:scaffold:scheme
}

func main() {
	var configFile string
	var expandEnv bool

	flag.StringVar(&configFile, "config", "", "Path to configuration file (required)")
	flag.BoolVar(&expandEnv, "expand-env", false, "Expand environment variables in config file")

	opts := zap.Options{
		Development: true,
	}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))

	if configFile == "" {
		setupLog.Error(nil, "config file is required (use --config flag)")
		os.Exit(1)
	}

	options := ctrl.Options{
		Scheme:           scheme,
		LeaderElectionID: "spire-eks-workload-registrar.spire.io",
	}
	config := &spirev1alpha1.ControllerManagerConfig{}

	if err := spirev1alpha1.LoadOptionsFromFile(configFile, scheme, &options, config, expandEnv); err != nil {
		setupLog.Error(err, "unable to load config file")
		os.Exit(1)
	}

	if config.TrustDomain == "" {
		setupLog.Error(nil, "trustDomain is required in config file")
		os.Exit(1)
	}

	trustDomain, err := spiffeid.TrustDomainFromString(config.TrustDomain)
	if err != nil {
		setupLog.Error(err, "invalid trust domain")
		os.Exit(1)
	}

	if config.SPIREServerSocketPath == "" {
		config.SPIREServerSocketPath = "/run/spire/sockets/server.sock"
	}

	if config.GCInterval == 0 {
		config.GCInterval = 10 * time.Minute
	}

	if config.ClusterName == "" {
		config.ClusterName = "default"
	}

	if config.EntryIDPrefix == "" {
		config.EntryIDPrefix = "eks-workload-"
	}

	ignoreNamespaces, err := parseIgnoreNamespaces(config.IgnoreNamespaces)
	if err != nil {
		setupLog.Error(err, "unable to parse ignore namespaces")
		os.Exit(1)
	}

	spireClient, err := spireapi.NewClient(config.SPIREServerSocketPath)
	if err != nil {
		setupLog.Error(err, "unable to create SPIRE client")
		os.Exit(1)
	}
	defer spireClient.Close()

	// Create temporary directory for certificates
	certDir, err := os.MkdirTemp("", "spire-controller-finalizer-")
	if err != nil {
		setupLog.Error(err, "Failed to create cert directory")
		os.Exit(1)
	}
	defer os.RemoveAll(certDir)

	k8sClient, err := kubernetes.NewForConfig(ctrl.GetConfigOrDie())
	if err != nil {
		setupLog.Error(err, "unable to create kubernetes client")
		os.Exit(1)
	}

	// Setup webhook server with proper TLS config
	webhookServer := webhook.NewServer(webhook.Options{
		Port:     9443,
		CertDir:  certDir,
		CertName: "tls.crt",
		KeyName:  "tls.key",
		TLSOpts: []func(*tls.Config){
			func(cfg *tls.Config) {
				cfg.MinVersion = tls.VersionTLS12
			},
		},
	})

	options.WebhookServer = webhookServer

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), options)
	if err != nil {
		setupLog.Error(err, "unable to start manager")
		os.Exit(1)
	}

	webhookManager := webhookmanager.NewFinalizerWebhookManager(
		trustDomain,
		"spire-controller-finalizer-webhook",
		"spire-system",
		"spire-controller-webhook-service",
		certDir,
		spireClient,
		k8sClient,
	)

	signalCtx := ctrl.SetupSignalHandler()

	// Start webhook manager before setting up controllers
	if err := webhookManager.Start(signalCtx); err != nil {
		setupLog.Error(err, "Failed to start webhook manager")
		os.Exit(1)
	}

	// Wait a bit for certificates to be ready
	time.Sleep(5 * time.Second)

	mainReconciler := &controller.MainReconciler{
		Client:           mgr.GetClient(),
		Scheme:           mgr.GetScheme(),
		TrustDomain:      trustDomain,
		ClusterName:      config.ClusterName,
		IgnoreNamespaces: ignoreNamespaces,
		EntryIDPrefix:    config.EntryIDPrefix,
		GCInterval:       config.GCInterval,
		SPIREClient:      spireClient,
	}

	clusterManager, err := cluster.NewManager(mainReconciler)
	if err != nil {
		setupLog.Error(err, "unable to create cluster manager")
		os.Exit(1)
	}
	defer clusterManager.Shutdown()

	mainReconciler.ClusterManager = clusterManager

	if err := mainReconciler.SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to setup main reconciler")
		os.Exit(1)
	}

	clusterManager.StartCleanupLoop(signalCtx)

	go func() {
		if err := mainReconciler.GetSharedReconciler().Run(signalCtx); err != nil {
			setupLog.Error(err, "Shared reconciler stopped")
		}
	}()

	if err = (&controller.EKSClusterRegistryReconciler{
		Client:         mgr.GetClient(),
		Scheme:         mgr.GetScheme(),
		MainReconciler: mainReconciler,
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "EKSClusterRegistry")
		os.Exit(1)
	}

	if err = (&controller.WorkloadIdentityReconciler{
		Client:         mgr.GetClient(),
		Scheme:         mgr.GetScheme(),
		MainReconciler: mainReconciler,
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "WorkloadIdentity")
		os.Exit(1)
	}

	// Setup deletion controller
	deletionReconciler := &controller.DeletionReconciler{
		Client:      mgr.GetClient(),
		Scheme:      mgr.GetScheme(),
		SPIREClient: spireClient,
	}

	if err := deletionReconciler.SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create deletion controller")
		os.Exit(1)
	}

	// Setup webhooks for finalizers
	workloadIdentityWebhook := &spirev1alpha1.WorkloadIdentityFinalizerWebhook{}
	if err := workloadIdentityWebhook.SetupWebhookWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create WorkloadIdentity finalizer webhook")
		os.Exit(1)
	}

	eksClusterWebhook := &spirev1alpha1.EKSClusterRegistryFinalizerWebhook{}
	if err := eksClusterWebhook.SetupWebhookWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create EKSClusterRegistry finalizer webhook")
		os.Exit(1)
	}
	//+kubebuilder:scaffold:builder

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up health check")
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up ready check")
		os.Exit(1)
	}

	setupLog.Info("starting manager",
		"trust-domain", config.TrustDomain,
		"cluster-name", config.ClusterName,
		"spire-socket", config.SPIREServerSocketPath,
		"entry-id-prefix", config.EntryIDPrefix,
		"gc-interval", config.GCInterval,
		"ignore-namespaces", config.IgnoreNamespaces,
	)

	if err := mgr.Start(signalCtx); err != nil {
		setupLog.Error(err, "problem running manager")
		os.Exit(1)
	}
}

// parseIgnoreNamespaces converts string patterns to compiled regular expressions
func parseIgnoreNamespaces(namespaces []string) ([]*regexp.Regexp, error) {
	var ignoreNamespaces []*regexp.Regexp
	for _, ns := range namespaces {
		regex, err := regexp.Compile(ns)
		if err != nil {
			return nil, fmt.Errorf("invalid namespace pattern %q: %w", ns, err)
		}
		ignoreNamespaces = append(ignoreNamespaces, regex)
	}
	return ignoreNamespaces, nil
}
