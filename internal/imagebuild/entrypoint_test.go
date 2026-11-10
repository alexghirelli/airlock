package imagebuild

import (
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/alexghirelli/airlock"
)

// dnsServers runs the entrypoint's dns_servers function on a resolv.conf.
func dnsServers(t *testing.T, resolvConf string) []string {
	t.Helper()
	dir := t.TempDir()
	script, err := airlock.Assets.ReadFile("image/entrypoint.sh")
	if err != nil {
		t.Fatal(err)
	}
	sp, rp := filepath.Join(dir, "entrypoint.sh"), filepath.Join(dir, "resolv.conf")
	if err := os.WriteFile(sp, script, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(rp, []byte(resolvConf), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("bash", "-c", `source "$1"; dns_servers "$2" | sort -u`, "bash", sp, rp).CombinedOutput()
	if err != nil {
		t.Fatalf("dns_servers failed: %v\n%s", err, out)
	}
	got := strings.Fields(string(out))
	sort.Strings(got)
	return got
}

func TestDNSServersAcrossRuntimes(t *testing.T) {
	cases := map[string]struct {
		resolv string
		want   []string
	}{
		// Docker Desktop (macOS, Windows): the header wraps the address in host(...).
		// This exact line made the sandbox refuse to start on macOS.
		"docker desktop": {"nameserver 127.0.0.11\noptions ndots:0\n\n# Based on host file: '/etc/resolv.conf' (internal resolver)\n# ExtServers: [host(192.168.65.7)]\n# Overrides: []\n",
			[]string{"127.0.0.11", "192.168.65.7"}},
		"docker engine on linux":       {"nameserver 127.0.0.11\n# ExtServers: [172.17.0.1]\n", []string{"127.0.0.11", "172.17.0.1"}},
		"several upstreams":            {"nameserver 127.0.0.11\n# ExtServers: [172.17.0.1 8.8.8.8]\n", []string{"127.0.0.11", "172.17.0.1", "8.8.8.8"}},
		"mixed forms":                  {"# ExtServers: [host(192.168.5.2) 1.1.1.1]\n", []string{"1.1.1.1", "192.168.5.2"}},
		"ipv6 upstream":                {"# ExtServers: [fd00::1 10.0.0.2]\n", []string{"10.0.0.2", "fd00::1"}},
		"podman puts it in nameserver": {"nameserver 10.89.0.1\n", []string{"10.89.0.1"}},
		"nothing":                      {"options edns0\n", nil},
		"garbage is dropped":           {"# ExtServers: [host(nonsense) ; rm -rf /]\n", nil},
	}
	for name, c := range cases {
		got := dnsServers(t, c.resolv)
		if strings.Join(got, ",") != strings.Join(c.want, ",") {
			t.Errorf("%s: got %q, want %q", name, got, c.want)
		}
	}
}
