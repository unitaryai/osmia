// Package k8sname builds Kubernetes object names from Osmia identifiers.
//
// TaskRun IDs embed ticket IDs, which come from external trackers and are
// not always valid Kubernetes names: Linear and Jira keys such as "ENG-123"
// are uppercase, and Kubernetes rejects any name with an uppercase letter.
package k8sname

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"

	"k8s.io/apimachinery/pkg/util/validation"
)

const hashLength = 8

// Label returns prefix+id as a valid RFC 1123 label (at most 63
// characters). Job names must be labels, because Kubernetes copies the Job
// name into a pod label.
func Label(prefix, id string) string {
	name := prefix + id
	if len(validation.IsDNS1123Label(name)) == 0 {
		return name
	}
	return sanitise(name, validation.DNS1123LabelMaxLength)
}

// Subdomain returns prefix+id as a valid RFC 1123 subdomain (at most 253
// characters), the rule for most objects, such as Secrets and PVCs.
func Subdomain(prefix, id string) string {
	name := prefix + id
	if len(validation.IsDNS1123Subdomain(name)) == 0 {
		return name
	}
	return sanitise(name, validation.DNS1123SubdomainMaxLength)
}

// sanitise makes an invalid name valid. Names that are already valid never
// reach it, so objects created before this existed keep their names.
//
// It lowercases the name and replaces every character other than a-z, 0-9
// and '-' with '-'. A name that is still too long is cut and given a short
// hash of the original, so names that differ only past the cut (a run and
// its "-r1" retry, say) stay different.
func sanitise(name string, maxLen int) string {
	clean := strings.Trim(strings.Map(validRune, strings.ToLower(name)), "-")
	if len(clean) <= maxLen {
		return clean
	}
	sum := sha256.Sum256([]byte(name))
	suffix := "-" + hex.EncodeToString(sum[:])[:hashLength]
	return strings.TrimRight(clean[:maxLen-len(suffix)], "-") + suffix
}

// validRune maps a lowercased rune to itself if it may appear in an RFC 1123
// label, and to '-' otherwise.
func validRune(r rune) rune {
	if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
		return r
	}
	return '-'
}
