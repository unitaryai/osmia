package k8sname

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"k8s.io/apimachinery/pkg/util/validation"
)

func TestLabel(t *testing.T) {
	long := "tr-incident-01hzy3k9q2r8m4n6p7s5t1v0wx-created-1793880552756"

	tests := []struct {
		name string
		id   string
		want string
	}{
		{name: "valid name is unchanged", id: "tr-42-1793880552756", want: "osmia-tr-42-1793880552756"},
		{name: "uppercase Linear key is lowercased", id: "tr-ENG-123-1793880552756", want: "osmia-tr-eng-123-1793880552756"},
		{name: "invalid characters become hyphens", id: "tr-PROJ_7.x-1", want: "osmia-tr-proj-7-x-1"},
		{name: "long name is cut with a hash", id: long},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Label("osmia-", tt.id)
			assert.Empty(t, validation.IsDNS1123Label(got), "%q must be a valid label", got)
			if tt.want != "" {
				assert.Equal(t, tt.want, got)
			}
		})
	}

	// A run and its retry differ only past the cut; their Jobs must not clash.
	assert.NotEqual(t, Label("osmia-", long), Label("osmia-", long+"-r1"))
}

func TestSubdomain(t *testing.T) {
	// Dots are valid in a subdomain, so an existing Secret or PVC name keeps
	// its name across the upgrade and is found again.
	assert.Equal(t, "osmia-session-tr-foo.bar-1", Subdomain("osmia-session-", "tr-foo.bar-1"))

	got := Subdomain("osmia-session-", "tr-ENG-123-1")
	assert.Equal(t, "osmia-session-tr-eng-123-1", got)
	assert.Empty(t, validation.IsDNS1123Subdomain(got))
}
