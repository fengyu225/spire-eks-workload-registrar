package spireentry

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"text/template"

	"github.com/spiffe/go-spiffe/v2/spiffeid"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	spirev1alpha1 "spire-eks-workload-registrar/api/v1alpha1"
	"spire-eks-workload-registrar/pkg/spireapi"
)

var defaultParentIDTemplate = template.Must(template.New("defaultParentIDTemplate").Parse("spiffe://{{ .TrustDomain }}/spire/agent/k8s_psat/{{ .ClusterName }}/{{ .NodeMeta.UID }}"))

type TemplateData struct {
	TrustDomain     string
	ClusterName     string
	ApplicationName string
	AccountID       string
	Region          string
	InstanceID      string
	PodMeta         *metav1.ObjectMeta
	PodSpec         *corev1.PodSpec
	NodeMeta        *metav1.ObjectMeta
	NodeSpec        *corev1.NodeSpec
}

func RenderEntry(
	identity *spirev1alpha1.WorkloadIdentity,
	cluster *spirev1alpha1.EKSClusterRegistry,
	pod *corev1.Pod,
	node *corev1.Node,
	trustDomain string,
) (*spireapi.Entry, error) {
	spiffeIDTmpl, err := template.New("spiffeID").Parse(identity.Spec.SPIFFEIDTemplate)
	if err != nil {
		return nil, fmt.Errorf("failed to parse SPIFFE ID template: %w", err)
	}

	var parentIDTmpl *template.Template
	if identity.Spec.ParentIDTemplate != "" {
		var err error
		parentIDTmpl, err = template.New("parentID").Parse(identity.Spec.ParentIDTemplate)
		if err != nil {
			return nil, fmt.Errorf("failed to parse parent ID template: %w", err)
		}
	} else {
		parentIDTmpl = defaultParentIDTemplate
	}

	data := &TemplateData{
		TrustDomain:     trustDomain,
		ClusterName:     cluster.Name,
		ApplicationName: identity.Spec.ApplicationName,
		AccountID:       cluster.Spec.AccountID,
		Region:          cluster.Spec.Region,
		PodMeta:         &pod.ObjectMeta,
		PodSpec:         &pod.Spec,
		NodeMeta:        &node.ObjectMeta,
		NodeSpec:        &node.Spec,
		InstanceID:      extractInstanceID(node.Spec.ProviderID),
	}

	var spiffeIDBuf bytes.Buffer
	if err := spiffeIDTmpl.Execute(&spiffeIDBuf, data); err != nil {
		return nil, fmt.Errorf("failed to render SPIFFE ID: %w", err)
	}
	spiffeID, err := spiffeid.FromString(spiffeIDBuf.String())
	if err != nil {
		return nil, fmt.Errorf("invalid SPIFFE ID: %w", err)
	}

	var parentIDBuf bytes.Buffer
	if err := parentIDTmpl.Execute(&parentIDBuf, data); err != nil {
		return nil, fmt.Errorf("failed to render parent ID: %w", err)
	}
	parentID, err := spiffeid.FromString(parentIDBuf.String())
	if err != nil {
		return nil, fmt.Errorf("invalid parent ID: %w", err)
	}

	selectors := []spireapi.Selector{
		{Type: "k8s", Value: fmt.Sprintf("pod-uid:%s", pod.UID)},
	}

	for _, tmplStr := range identity.Spec.WorkloadSelectorTemplates {
		tmpl, err := template.New("selector").Parse(tmplStr)
		if err != nil {
			return nil, fmt.Errorf("failed to parse selector template: %w", err)
		}

		var buf bytes.Buffer
		if err := tmpl.Execute(&buf, data); err != nil {
			return nil, fmt.Errorf("failed to render selector: %w", err)
		}

		parts := strings.SplitN(buf.String(), ":", 2)
		if len(parts) == 2 {
			selectors = append(selectors, spireapi.Selector{
				Type:  parts[0],
				Value: parts[1],
			})
		}
	}

	var dnsNames []string
	for _, tmplStr := range identity.Spec.DNSNameTemplates {
		tmpl, err := template.New("dnsName").Parse(tmplStr)
		if err != nil {
			return nil, fmt.Errorf("failed to parse DNS name template: %w", err)
		}

		var buf bytes.Buffer
		if err := tmpl.Execute(&buf, data); err != nil {
			return nil, fmt.Errorf("failed to render DNS name: %w", err)
		}
		dnsNames = append(dnsNames, buf.String())
	}

	var federatesWith []spiffeid.TrustDomain
	for _, td := range identity.Spec.FederatesWith {
		trustDomain, err := spiffeid.TrustDomainFromString(td)
		if err != nil {
			return nil, fmt.Errorf("invalid trust domain %s: %w", td, err)
		}
		federatesWith = append(federatesWith, trustDomain)
	}

	// Set hint for tracking with ownership information
	hint := identity.Spec.Hint
	ownerInfo := fmt.Sprintf("owner:WorkloadIdentity/%s", identity.Name)
	if hint == "" {
		hint = fmt.Sprintf("workloadidentity:%s:%s;%s", identity.Name, cluster.Name, ownerInfo)
	} else {
		hint = fmt.Sprintf("%s;%s", hint, ownerInfo)
	}

	return &spireapi.Entry{
		SPIFFEID:      spiffeID,
		ParentID:      parentID,
		Selectors:     selectors,
		X509SVIDTTL:   int32(identity.Spec.TTL.Seconds()),
		JWTSVIDTTL:    int32(identity.Spec.JWTTTL.Seconds()),
		FederatesWith: federatesWith,
		Admin:         identity.Spec.Admin,
		Downstream:    identity.Spec.Downstream,
		DNSNames:      dnsNames,
		Hint:          hint,
	}, nil
}

// parseSelector parses a selector string in format "type:value"
func parseSelector(selector string) (spireapi.Selector, error) {
	parts := strings.SplitN(selector, ":", 2)
	switch {
	case len(parts) == 1:
		return spireapi.Selector{}, errors.New("expected at least one colon to separate type from value")
	case len(parts[0]) == 0:
		return spireapi.Selector{}, errors.New("type cannot be empty")
	case len(parts[1]) == 0:
		return spireapi.Selector{}, errors.New("value cannot be empty")
	}
	return spireapi.Selector{
		Type:  parts[0],
		Value: parts[1],
	}, nil
}

// parseSelectors parses multiple selector strings
func parseSelectors(selectors []string) ([]spireapi.Selector, error) {
	ss := make([]spireapi.Selector, 0, len(selectors))
	for _, selector := range selectors {
		s, err := parseSelector(selector)
		if err != nil {
			return nil, err
		}
		ss = append(ss, s)
	}
	return ss, nil
}

// renderTemplate renders a template with the given data
func renderTemplate(tmpl *template.Template, data interface{}) (string, error) {
	buf := new(bytes.Buffer)
	if err := tmpl.Execute(buf, data); err != nil {
		return "", fmt.Errorf("failed to execute template: %w", err)
	}
	return buf.String(), nil
}

// renderSPIFFEID renders a SPIFFE ID template and validates the trust domain
func renderSPIFFEID(tmpl *template.Template, data *TemplateData, expectTD spiffeid.TrustDomain) (spiffeid.ID, error) {
	rendered, err := renderTemplate(tmpl, data)
	if err != nil {
		return spiffeid.ID{}, err
	}
	id, err := spiffeid.FromString(rendered)
	if err != nil {
		return spiffeid.ID{}, fmt.Errorf("invalid SPIFFE ID: %w", err)
	}
	if id.TrustDomain() != expectTD {
		return spiffeid.ID{}, fmt.Errorf("invalid SPIFFE ID: expected trust domain %q but got %q", expectTD, id.TrustDomain())
	}
	return id, nil
}

// appendIfNotExists appends items to slice if they don't exist in the set
func appendIfNotExists(slice []string, sliceSet map[string]struct{}, items ...string) []string {
	for _, item := range items {
		if _, exists := sliceSet[item]; !exists {
			sliceSet[item] = struct{}{}
			slice = append(slice, item)
		}
	}
	return slice
}

// dnsNamesFromEndpoints generates DNS names from Kubernetes endpoints
func dnsNamesFromEndpoints(endpointsList *corev1.EndpointsList, clusterDomain string) []string {
	var dnsNames []string
	for _, endpoint := range endpointsList.Items {
		dnsNames = append(dnsNames,
			endpoint.Name,
			endpoint.Name+"."+endpoint.Namespace,
			endpoint.Name+"."+endpoint.Namespace+".svc",
		)
		if clusterDomain != "" {
			dnsNames = append(dnsNames, endpoint.Name+"."+endpoint.Namespace+".svc."+clusterDomain)
		}
	}
	sort.Strings(dnsNames)
	return dnsNames
}

func extractInstanceID(providerID string) string {
	// Format: aws:///us-west-2a/i-1234567890abcdef0
	parts := strings.Split(providerID, "/")
	if len(parts) > 0 {
		return parts[len(parts)-1]
	}
	return ""
}

// EntryKey generates a unique key for an entry based on SPIFFE ID, Parent ID, and selectors
func EntryKey(entry *spireapi.Entry) string {
	return makeEntryKey(*entry)
}

// MakeEntryKey generates a unique key for an entry (exported version)
func MakeEntryKey(entry spireapi.Entry) string {
	return makeEntryKey(entry)
}

// makeEntryKey generates a unique key for an entry
func makeEntryKey(entry spireapi.Entry) string {
	h := sha256.New()
	io.WriteString(h, entry.SPIFFEID.String())
	io.WriteString(h, entry.ParentID.String())
	for _, selector := range sortSelectors(entry.Selectors) {
		io.WriteString(h, selector.Type)
		io.WriteString(h, selector.Value)
	}
	sum := h.Sum(nil)
	return hex.EncodeToString(sum)
}

// sortSelectors sorts selectors for consistent key generation
func sortSelectors(unsorted []spireapi.Selector) []spireapi.Selector {
	sorted := append([]spireapi.Selector(nil), unsorted...)
	sort.Slice(sorted, func(i, j int) bool {
		switch {
		case sorted[i].Type < sorted[j].Type:
			return true
		case sorted[i].Type > sorted[j].Type:
			return false
		default:
			return sorted[i].Value < sorted[j].Value
		}
	})
	return sorted
}

// GetOutdatedEntryFields compares two entries and returns fields that are different (exported)
func GetOutdatedEntryFields(newEntry, oldEntry spireapi.Entry) []string {
	return getOutdatedEntryFields(newEntry, oldEntry)
}

// getOutdatedEntryFields compares two entries and returns fields that are different
func getOutdatedEntryFields(newEntry, oldEntry spireapi.Entry) []string {
	var outdated []string
	if oldEntry.X509SVIDTTL != newEntry.X509SVIDTTL {
		outdated = append(outdated, "X509SVIDTTL")
	}
	if oldEntry.JWTSVIDTTL != newEntry.JWTSVIDTTL {
		outdated = append(outdated, "JWTSVIDTTL")
	}
	if !trustDomainsMatch(oldEntry.FederatesWith, newEntry.FederatesWith) {
		outdated = append(outdated, "FederatesWith")
	}
	if oldEntry.Admin != newEntry.Admin {
		outdated = append(outdated, "Admin")
	}
	if oldEntry.Downstream != newEntry.Downstream {
		outdated = append(outdated, "Downstream")
	}
	if !stringsMatch(oldEntry.DNSNames, newEntry.DNSNames) {
		outdated = append(outdated, "DNSNames")
	}
	if oldEntry.Hint != newEntry.Hint {
		outdated = append(outdated, "Hint")
	}
	return outdated
}

// trustDomainsMatch compares two slices of trust domains
func trustDomainsMatch(a, b []spiffeid.TrustDomain) bool {
	if len(a) != len(b) {
		return false
	}

	aStrs := make([]string, len(a))
	for i, td := range a {
		aStrs[i] = td.String()
	}
	sort.Strings(aStrs)

	bStrs := make([]string, len(b))
	for i, td := range b {
		bStrs[i] = td.String()
	}
	sort.Strings(bStrs)

	for i := range aStrs {
		if aStrs[i] != bStrs[i] {
			return false
		}
	}
	return true
}

// stringsMatch compares two string slices
func stringsMatch(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}

	aCopy := make([]string, len(a))
	copy(aCopy, a)
	sort.Strings(aCopy)

	bCopy := make([]string, len(b))
	copy(bCopy, b)
	sort.Strings(bCopy)

	for i := range aCopy {
		if aCopy[i] != bCopy[i] {
			return false
		}
	}
	return true
}
