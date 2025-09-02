package finalizer

import (
	"slices"
)

const (
	// SPIRECleanupFinalizer ensures SPIRE entries are cleaned up before CRD deletion
	SPIRECleanupFinalizer = "spire.spiffe.io/cleanup"
)

// ContainsFinalizer checks if the finalizer exists in the list
func ContainsFinalizer(finalizers []string, finalizer string) bool {
	return slices.Contains(finalizers, finalizer)
}

// AddFinalizer adds a finalizer if it doesn't exist
func AddFinalizer(finalizers []string, finalizer string) []string {
	if !ContainsFinalizer(finalizers, finalizer) {
		return append(finalizers, finalizer)
	}
	return finalizers
}

// RemoveFinalizer removes a finalizer from the list
func RemoveFinalizer(finalizers []string, finalizer string) []string {
	return slices.DeleteFunc(finalizers, func(s string) bool {
		return s == finalizer
	})
}
