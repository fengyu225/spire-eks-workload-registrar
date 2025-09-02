package webhookmanager

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"time"

	"spire-eks-workload-registrar/pkg/spireapi"

	"github.com/spiffe/go-spiffe/v2/spiffeid"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

type FinalizerWebhookManager struct {
	trustDomain      spiffeid.TrustDomain
	webhookName      string
	webhookNamespace string
	webhookService   string
	certDir          string

	spireClient spireapi.Client
	k8sClient   kubernetes.Interface

	currentCABundle   []byte
	certificateExpiry time.Time
	caCert            *x509.Certificate
	caKey             *ecdsa.PrivateKey
}

func NewFinalizerWebhookManager(
	trustDomain spiffeid.TrustDomain,
	webhookName string,
	webhookNamespace string,
	webhookService string,
	certDir string,
	spireClient spireapi.Client,
	k8sClient kubernetes.Interface,
) *FinalizerWebhookManager {
	return &FinalizerWebhookManager{
		trustDomain:      trustDomain,
		webhookName:      webhookName,
		webhookNamespace: webhookNamespace,
		webhookService:   webhookService,
		certDir:          certDir,
		spireClient:      spireClient,
		k8sClient:        k8sClient,
	}
}

func (m *FinalizerWebhookManager) Start(ctx context.Context) error {
	log := log.FromContext(ctx)

	if err := os.MkdirAll(m.certDir, 0700); err != nil {
		return fmt.Errorf("failed to create cert directory: %w", err)
	}

	// Generate CA first
	if err := m.generateCA(ctx); err != nil {
		return fmt.Errorf("failed to generate CA: %w", err)
	}

	// Generate webhook certificate
	if err := m.MintCertificate(ctx); err != nil {
		return fmt.Errorf("initial certificate mint failed: %w", err)
	}

	// Update webhook configuration with CA bundle
	if err := m.UpdateWebhookConfiguration(ctx); err != nil {
		return fmt.Errorf("initial webhook update failed: %w", err)
	}

	// Start rotation loops
	go m.rotationLoop(ctx)

	log.Info("Webhook certificate manager started")
	return nil
}

func (m *FinalizerWebhookManager) generateCA(ctx context.Context) error {
	// Generate CA key
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return fmt.Errorf("failed to generate CA key: %w", err)
	}

	// Create CA certificate
	caTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject: pkix.Name{
			CommonName:   fmt.Sprintf("webhook-ca.%s", m.webhookNamespace),
			Organization: []string{"SPIRE Controller"},
		},
		NotBefore:             time.Now(),
		NotAfter:              time.Now().Add(365 * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}

	caCertDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		return fmt.Errorf("failed to create CA certificate: %w", err)
	}

	caCert, err := x509.ParseCertificate(caCertDER)
	if err != nil {
		return fmt.Errorf("failed to parse CA certificate: %w", err)
	}

	// Store CA
	m.caCert = caCert
	m.caKey = caKey

	// Create CA bundle
	m.currentCABundle = pem.EncodeToMemory(&pem.Block{
		Type:  "CERTIFICATE",
		Bytes: caCertDER,
	})

	return nil
}

func (m *FinalizerWebhookManager) MintCertificate(ctx context.Context) error {
	log := log.FromContext(ctx)

	// Generate new private key
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return fmt.Errorf("failed to generate private key: %w", err)
	}

	// DNS names for the webhook service
	dnsNames := []string{
		m.webhookService,
		fmt.Sprintf("%s.%s", m.webhookService, m.webhookNamespace),
		fmt.Sprintf("%s.%s.svc", m.webhookService, m.webhookNamespace),
		fmt.Sprintf("%s.%s.svc.cluster.local", m.webhookService, m.webhookNamespace),
	}

	// Create certificate template
	template := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().Unix()),
		Subject: pkix.Name{
			CommonName:   fmt.Sprintf("%s.%s.svc", m.webhookService, m.webhookNamespace),
			Organization: []string{"SPIRE Controller"},
		},
		NotBefore:             time.Now(),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		DNSNames:              dnsNames,
	}

	// Sign with CA
	certDER, err := x509.CreateCertificate(rand.Reader, template, m.caCert, &key.PublicKey, m.caKey)
	if err != nil {
		return fmt.Errorf("failed to create certificate: %w", err)
	}

	cert, err := x509.ParseCertificate(certDER)
	if err != nil {
		return fmt.Errorf("failed to parse certificate: %w", err)
	}

	// Write both cert and key to separate files
	certPath := filepath.Join(m.certDir, "tls.crt")
	keyPath := filepath.Join(m.certDir, "tls.key")

	// Write certificate
	certOut, err := os.Create(certPath)
	if err != nil {
		return fmt.Errorf("failed to create cert file: %w", err)
	}
	defer certOut.Close()

	if err := pem.Encode(certOut, &pem.Block{Type: "CERTIFICATE", Bytes: certDER}); err != nil {
		return fmt.Errorf("failed to write certificate: %w", err)
	}

	// Write key
	keyOut, err := os.OpenFile(keyPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return fmt.Errorf("failed to create key file: %w", err)
	}
	defer keyOut.Close()

	keyBytes, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return fmt.Errorf("failed to marshal private key: %w", err)
	}

	if err := pem.Encode(keyOut, &pem.Block{Type: "PRIVATE KEY", Bytes: keyBytes}); err != nil {
		return fmt.Errorf("failed to write private key: %w", err)
	}

	m.certificateExpiry = cert.NotAfter
	log.Info("Minted new webhook certificate", "expires", cert.NotAfter, "dnsNames", dnsNames)

	return nil
}

func (m *FinalizerWebhookManager) UpdateWebhookConfiguration(ctx context.Context) error {
	log := log.FromContext(ctx)

	// Get current webhook configuration
	webhook, err := m.k8sClient.AdmissionregistrationV1().
		MutatingWebhookConfigurations().
		Get(ctx, m.webhookName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("failed to get webhook configuration: %w", err)
	}

	// Update CA bundle in all webhooks
	updated := false
	for i := range webhook.Webhooks {
		if !bytes.Equal(webhook.Webhooks[i].ClientConfig.CABundle, m.currentCABundle) {
			webhook.Webhooks[i].ClientConfig.CABundle = m.currentCABundle
			updated = true
		}
	}

	if updated {
		// Apply patch
		_, err = m.k8sClient.AdmissionregistrationV1().
			MutatingWebhookConfigurations().
			Update(ctx, webhook, metav1.UpdateOptions{})
		if err != nil {
			return fmt.Errorf("failed to update webhook: %w", err)
		}

		log.Info("Updated webhook configuration with new CA bundle")
	}

	return nil
}

func (m *FinalizerWebhookManager) ShouldRotate() bool {
	if m.certificateExpiry.IsZero() {
		return true // No certificate yet
	}

	now := time.Now()
	lifetime := m.certificateExpiry.Sub(now)

	// Rotate at 50% of lifetime or when expired
	if lifetime < 0 {
		return true // Expired
	}

	// For 24-hour certs, rotate after 12 hours
	totalLifetime := 24 * time.Hour
	halfLife := totalLifetime / 2
	timeUntilRotation := m.certificateExpiry.Sub(now.Add(halfLife))
	return timeUntilRotation <= 0
}

func (m *FinalizerWebhookManager) rotationLoop(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if m.ShouldRotate() {
				log := log.FromContext(ctx)
				log.Info("Rotating webhook certificate")

				if err := m.MintCertificate(ctx); err != nil {
					log.Error(err, "Failed to rotate certificate")
					continue
				}

				if err := m.UpdateWebhookConfiguration(ctx); err != nil {
					log.Error(err, "Failed to update webhook configuration after rotation")
				}
			}
		}
	}
}
