package reconciler

import (
	"context"
	"sync"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/log"
)

// Triggerer is an interface that can trigger reconciliation
type Triggerer interface {
	Trigger()
}

// ReconcileFunc is a function that performs reconciliation
type ReconcileFunc func(ctx context.Context)

// Config holds configuration for the reconciler
type Config struct {
	Kind       string
	Reconcile  ReconcileFunc
	GCInterval time.Duration
}

// Reconciler manages periodic and triggered reconciliation
type Reconciler interface {
	Triggerer
	Run(ctx context.Context) error
}

type reconciler struct {
	config Config

	mu        sync.Mutex
	triggered bool
	stopCh    chan struct{}
}

// New creates a new reconciler
func New(config Config) Reconciler {
	if config.GCInterval == 0 {
		config.GCInterval = 10 * time.Minute
	}

	return &reconciler{
		config: config,
		stopCh: make(chan struct{}),
	}
}

func (r *reconciler) Trigger() {
	r.mu.Lock()
	defer r.mu.Unlock()

	log.Log.V(2).Info("Manual reconciliation triggered", "reconciler", r.config.Kind)
	r.triggered = true

	// Non-blocking send to trigger channel
	select {
	case r.stopCh <- struct{}{}:
		log.Log.V(3).Info("Trigger signal sent successfully", "reconciler", r.config.Kind)
	default:
		log.Log.V(3).Info("Trigger channel busy, reconciliation already pending", "reconciler", r.config.Kind)
	}
}

func (r *reconciler) Run(ctx context.Context) error {
	log := log.FromContext(ctx).WithValues("reconciler", r.config.Kind)

	log.Info("Starting reconciler", "gcInterval", r.config.GCInterval)
	defer log.Info("Reconciler stopped")

	ticker := time.NewTicker(r.config.GCInterval)
	defer ticker.Stop()
	log.V(2).Info("Reconciler ticker started", "interval", r.config.GCInterval)

	reconcileCount := 0
	for {
		select {
		case <-ctx.Done():
			log.V(1).Info("Context cancelled, stopping reconciler", "totalReconciles", reconcileCount)
			return ctx.Err()
		case <-ticker.C:
			log.V(2).Info("Periodic reconciliation triggered by timer")
			reconcileCount++
			r.reconcile(ctx, "periodic", reconcileCount)
		case <-r.stopCh:
			log.V(2).Info("Manual reconciliation triggered by event")
			reconcileCount++
			r.reconcile(ctx, "manual", reconcileCount)
		}
	}
}

func (r *reconciler) reconcile(ctx context.Context, trigger string, count int) {
	r.mu.Lock()
	r.triggered = false
	r.mu.Unlock()

	log := log.FromContext(ctx).WithValues("reconciler", r.config.Kind, "trigger", trigger, "count", count)
	log.V(1).Info("Starting reconciliation")
	start := time.Now()

	if r.config.Reconcile != nil {
		r.config.Reconcile(ctx)
	} else {
		log.V(2).Info("No reconcile function configured")
	}

	duration := time.Since(start)
	log.V(1).Info("Reconciliation completed", "duration", duration)
}
