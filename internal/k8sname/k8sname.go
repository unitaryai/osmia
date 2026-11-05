// Package k8sname turns Osmia IDs, which can hold uppercase ticket keys such
// as "ENG-123", into valid Kubernetes names.
package k8sname

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"

	"k8s.io/apimachinery/pkg/util/validation"
)

const hashLength = 8

// Label returns prefix+id as a valid RFC 1123 label, as Job names must be.
func Label(prefix, id string) string {
	name := prefix + id
	if len(validation.IsDNS1123Label(name)) == 0 {
		return name
	}
	return sanitise(name, validation.DNS1123LabelMaxLength)
}

// Subdomain returns prefix+id as a valid RFC 1123 subdomain (Secrets, PVCs).
func Subdomain(prefix, id string) string {
	name := prefix + id
	if len(validation.IsDNS1123Subdomain(name)) == 0 {
		return name
	}
	return sanitise(name, validation.DNS1123SubdomainMaxLength)
}

// sanitise lowercases name and replaces invalid characters with '-'. An
// overlong name is cut and given a hash of the original, so names stay distinct.
func sanitise(name string, maxLen int) string {
	clean := strings.Trim(strings.Map(validRune, strings.ToLower(name)), "-")
	if len(clean) <= maxLen {
		return clean
	}
	sum := sha256.Sum256([]byte(name))
	suffix := "-" + hex.EncodeToString(sum[:])[:hashLength]
	return strings.TrimRight(clean[:maxLen-len(suffix)], "-") + suffix
}

// validRune maps a lowercased rune to itself if allowed, else to '-'.
func validRune(r rune) rune {
	if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
		return r
	}
	return '-'
}
