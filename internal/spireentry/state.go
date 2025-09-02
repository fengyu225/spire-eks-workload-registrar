package spireentry

import (
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/log"

	spirev1alpha1 "spire-eks-workload-registrar/api/v1alpha1"
	"spire-eks-workload-registrar/pkg/spireapi"
)

// EntriesState manages the state of SPIRE entries
type EntriesState interface {
	AddCurrent(entry spireapi.Entry)
	AddDeclared(entry spireapi.Entry, identity *spirev1alpha1.WorkloadIdentity, cluster *spirev1alpha1.EKSClusterRegistry, pod *corev1.Pod)
	GetChanges() (toCreate, toUpdate, toDelete []spireapi.Entry)
}

type entriesState struct {
	current     map[string]spireapi.Entry
	declared    map[string]spireapi.Entry
	entryPrefix string
}

// NewEntriesState creates a new entries state manager
func NewEntriesState() EntriesState {
	return NewEntriesStateWithPrefix("")
}

// NewEntriesStateWithPrefix creates a new entries state manager with ID prefix
func NewEntriesStateWithPrefix(prefix string) EntriesState {
	return &entriesState{
		current:     make(map[string]spireapi.Entry),
		declared:    make(map[string]spireapi.Entry),
		entryPrefix: prefix,
	}
}

func (s *entriesState) AddCurrent(entry spireapi.Entry) {
	log.Log.V(4).Info("Adding current entry to state", "entryID", entry.ID, "spiffeID", entry.SPIFFEID)
	s.current[entry.ID] = entry
}

func (s *entriesState) AddDeclared(entry spireapi.Entry, identity *spirev1alpha1.WorkloadIdentity, cluster *spirev1alpha1.EKSClusterRegistry, pod *corev1.Pod) {
	// Use the deterministic key from entries.go instead of UUID
	key := MakeEntryKey(entry)

	// Apply prefix if configured
	if s.entryPrefix != "" {
		entry.ID = s.entryPrefix + key
	} else {
		entry.ID = key
	}

	log.Log.V(4).Info("Adding declared entry to state", "entryID", entry.ID, "spiffeID", entry.SPIFFEID)
	s.declared[entry.ID] = entry
}

func (s *entriesState) GetChanges() (toCreate, toUpdate, toDelete []spireapi.Entry) {
	log.Log.V(3).Info("Calculating entry changes", "currentCount", len(s.current), "declaredCount", len(s.declared))

	for id, declared := range s.declared {
		if current, exists := s.current[id]; !exists {
			log.Log.V(4).Info("Entry needs to be created", "entryID", id, "spiffeID", declared.SPIFFEID)
			toCreate = append(toCreate, declared)
		} else if !s.entriesEqual(current, declared) {
			log.Log.V(4).Info("Entry needs to be updated", "entryID", id, "spiffeID", declared.SPIFFEID)
			declared.ID = current.ID
			toUpdate = append(toUpdate, declared)
		} else {
			log.Log.V(4).Info("Entry is up to date", "entryID", id, "spiffeID", declared.SPIFFEID)
		}
	}

	for id, current := range s.current {
		if _, exists := s.declared[id]; !exists {
			log.Log.V(4).Info("Entry needs to be deleted", "entryID", id, "spiffeID", current.SPIFFEID)
			toDelete = append(toDelete, current)
		}
	}

	log.Log.V(3).Info("Entry changes calculated", "toCreate", len(toCreate), "toUpdate", len(toUpdate), "toDelete", len(toDelete))
	return toCreate, toUpdate, toDelete
}

func (s *entriesState) entriesEqual(a, b spireapi.Entry) bool {
	log.Log.V(5).Info("Comparing entries", "aID", a.ID, "bID", b.ID, "aSPIFFEID", a.SPIFFEID, "bSPIFFEID", b.SPIFFEID)

	// Basic comparison for SPIFFE ID, Parent ID, and Selectors
	if a.SPIFFEID != b.SPIFFEID || a.ParentID != b.ParentID {
		log.Log.V(5).Info("Entries differ in SPIFFE ID or Parent ID", "aSPIFFEID", a.SPIFFEID, "bSPIFFEID", b.SPIFFEID, "aParentID", a.ParentID, "bParentID", b.ParentID)
		return false
	}

	if len(a.Selectors) != len(b.Selectors) {
		log.Log.V(5).Info("Entries differ in selector count", "aCount", len(a.Selectors), "bCount", len(b.Selectors))
		return false
	}

	aSelectors := make(map[string]string)
	for _, sel := range a.Selectors {
		aSelectors[sel.Type] = sel.Value
	}

	for _, sel := range b.Selectors {
		if val, ok := aSelectors[sel.Type]; !ok || val != sel.Value {
			log.Log.V(5).Info("Entries differ in selectors", "selectorType", sel.Type, "aValue", val, "bValue", sel.Value)
			return false
		}
	}

	// Use the existing getOutdatedEntryFields function to check all other fields
	outdatedFields := getOutdatedEntryFields(b, a)
	if len(outdatedFields) > 0 {
		log.Log.V(5).Info("Entries differ in additional fields", "outdatedFields", outdatedFields)
		return false
	}

	log.Log.V(5).Info("Entries are equal")
	return true
}
