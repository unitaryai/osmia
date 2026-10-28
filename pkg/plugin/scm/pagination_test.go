package scm

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNextPageURL(t *testing.T) {
	const current = "https://gitlab.com/api/v4/projects/1/merge_requests/2/notes?page=1"
	tests := []struct {
		name  string
		links []string
		want  string
	}{
		{name: "no link header", want: ""},
		{
			name:  "gitlab style with next",
			links: []string{`<https://gitlab.com/api/v4/projects/1/merge_requests/2/notes?page=2>; rel="next", <https://gitlab.com/api/v4/projects/1/merge_requests/2/notes?page=1>; rel="first", <https://gitlab.com/api/v4/projects/1/merge_requests/2/notes?page=3>; rel="last"`},
			want:  "https://gitlab.com/api/v4/projects/1/merge_requests/2/notes?page=2",
		},
		{
			name:  "last page has no next",
			links: []string{`<https://gitlab.com/api/v4/x?page=1>; rel="first", <https://gitlab.com/api/v4/x?page=3>; rel="prev"`},
			want:  "",
		},
		{
			name:  "next in a second header value",
			links: []string{`<https://gitlab.com/api/v4/x?page=1>; rel="prev"`, `<https://gitlab.com/api/v4/x?page=3>; rel="next"`},
			want:  "https://gitlab.com/api/v4/x?page=3",
		},
		{
			name:  "rel with several values",
			links: []string{`<https://gitlab.com/api/v4/x?page=2>; rel="next last"`},
			want:  "https://gitlab.com/api/v4/x?page=2",
		},
		{
			name:  "unquoted rel",
			links: []string{`<https://gitlab.com/api/v4/x?page=2>; rel=next`},
			want:  "https://gitlab.com/api/v4/x?page=2",
		},
		{
			name:  "other host is refused",
			links: []string{`<https://evil.example/api/v4/x?page=2>; rel="next"`},
			want:  "",
		},
		{
			name:  "scheme downgrade is refused",
			links: []string{`<http://gitlab.com/api/v4/x?page=2>; rel="next"`},
			want:  "",
		},
		{
			name:  "malformed target ignored",
			links: []string{`https://gitlab.com/api/v4/x?page=2; rel="next"`},
			want:  "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := http.Header{}
			for _, l := range tt.links {
				h.Add("Link", l)
			}
			assert.Equal(t, tt.want, NextPageURL(h, current))
		})
	}
}
