package caphealth

import (
	"reflect"
	"testing"

	"github.com/felag-engineering/gleipnir/plugin-sdk/manifestv2"
)

func TestAttestedEventKindsFromManifest(t *testing.T) {
	tests := []struct {
		name  string
		decls []manifestv2.EventKindDecl
		want  []string
	}{
		{
			name:  "no declarations",
			decls: nil,
			want:  nil,
		},
		{
			name:  "empty slice",
			decls: []manifestv2.EventKindDecl{},
			want:  nil,
		},
		{
			name: "extracts kind in declared order",
			decls: []manifestv2.EventKindDecl{
				{Kind: "message", Description: "a chat message"},
				{Kind: "reaction"},
			},
			want: []string{"message", "reaction"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := AttestedEventKindsFromManifest(tc.decls)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("AttestedEventKindsFromManifest(%+v) = %v, want %v", tc.decls, got, tc.want)
			}
		})
	}
}
