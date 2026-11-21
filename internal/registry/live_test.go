package registry

import (
	"context"
	"os"
	"testing"

	"github.com/krish-vd/phantom/internal/extract"
)

// Hits the real registries. Run with PHANTOM_LIVE=1 go test ./...
func TestLive(t *testing.T) {
	if os.Getenv("PHANTOM_LIVE") == "" {
		t.Skip("set PHANTOM_LIVE=1 to query real registries")
	}
	c := New()
	ctx := context.Background()
	cases := []struct {
		eco  extract.Ecosystem
		name string
		want bool
	}{
		{extract.PyPI, "requests", true},
		{extract.PyPI, "phantom-surely-not-a-real-package-7c1f", false},
		{extract.NPM, "lodash", true},
		{extract.NPM, "@types/node", true},
		{extract.NPM, "phantom-surely-not-a-real-package-7c1f", false},
	}
	for _, tc := range cases {
		in, err := c.Lookup(ctx, tc.eco, tc.name)
		if err != nil {
			t.Fatalf("%s/%s: %v", tc.eco, tc.name, err)
		}
		if in.Exists != tc.want {
			t.Errorf("%s/%s: exists=%v want %v", tc.eco, tc.name, in.Exists, tc.want)
		}
		if in.Exists && in.Created.IsZero() {
			t.Errorf("%s/%s: no creation date", tc.eco, tc.name)
		}
		t.Logf("%s/%s: %+v", tc.eco, tc.name, in)
	}
}
