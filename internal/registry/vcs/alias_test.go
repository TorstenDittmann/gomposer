package vcs

import (
	"reflect"
	"sort"
	"testing"
)

func TestExpandAliases(t *testing.T) {
	cases := []struct {
		name    string
		ver     string
		aliases map[string]string
		want    []string
	}{
		{
			name:    "dev-main aliased to 1.x-dev",
			ver:     "dev-main",
			aliases: map[string]string{"dev-main": "1.x-dev"},
			want:    []string{"1.x-dev"},
		},
		{
			name:    "alias key without dev- prefix is tolerated",
			ver:     "dev-main",
			aliases: map[string]string{"main": "2.x-dev"},
			want:    []string{"2.x-dev"},
		},
		{
			name:    "no match",
			ver:     "dev-feature",
			aliases: map[string]string{"dev-main": "1.x-dev"},
			want:    nil,
		},
		{
			name:    "tagged version ignored",
			ver:     "1.0.0",
			aliases: map[string]string{"dev-main": "1.x-dev"},
			want:    nil,
		},
		{
			name:    "empty map",
			ver:     "dev-main",
			aliases: nil,
			want:    nil,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := expandAliases(c.ver, c.aliases)
			sort.Strings(got)
			sort.Strings(c.want)
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("got %v, want %v", got, c.want)
			}
		})
	}
}
