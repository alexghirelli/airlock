package version

import "testing"

func TestFromModuleVersion(t *testing.T) {
	for in, want := range map[string]string{"v0.1.0": "0.1.0", "v1.2.3-rc.1": "1.2.3-rc.1", "v0.1.1-0.20261110165959-abcdef123456": "0.1.1-0.20261110165959-abcdef123456"} {
		if got, ok := fromModuleVersion(in); !ok || got != want {
			t.Errorf("fromModuleVersion(%q) = %q, %v; want %q", in, got, ok, want)
		}
	}
	for _, in := range []string{"", "(devel)", "0.1.0", "master"} {
		if got, ok := fromModuleVersion(in); ok {
			t.Errorf("fromModuleVersion(%q) = %q; builds without a module version must keep the dev version", in, got)
		}
	}
}
