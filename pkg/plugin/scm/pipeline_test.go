package scm

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReadLogTail(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "short log is kept whole", in: "line 1\nline 2\n", want: "line 1\nline 2\n"},
		{name: "colour codes are removed", in: "\x1b[0;31mFAIL\x1b[0m TestFoo\n", want: "FAIL TestFoo\n"},
		{name: "terminal title sequences are removed", in: "\x1b]0;title\x07done\n", want: "done\n"},
		{
			name: "long log keeps only its end",
			in:   strings.Repeat("x", 3*MaxLogTailBytes) + "the failure",
			want: strings.Repeat("x", MaxLogTailBytes-len("the failure")) + "the failure",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ReadLogTail(strings.NewReader(tt.in))
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}
