//go:build integration

package tests

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Test 3: isolation in open-no-lan mode.
func TestIsolationOpenNoLAN(t *testing.T) {
	p := newProject(t, "iso", "")

	// A file in the host home must be invisible inside the box.
	home, _ := os.UserHomeDir()
	sentinel := filepath.Join(home, ".airlock-it-sentinel")
	if err := os.WriteFile(sentinel, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Remove(sentinel) })

	out := p.mustSh(`
reach() { if curl -sS -m 4 -o /dev/null "$2" 2>/dev/null; then echo "REACHABLE $1"; else echo "blocked $1"; fi; }
GW=$(ip route | awk '/default/{print $3}')
reach gateway "http://$GW"
reach lan-192 http://192.168.1.1
reach lan-10 http://10.0.0.1
reach lan-172 http://172.20.0.1
reach metadata http://169.254.169.254
reach host.docker.internal http://host.docker.internal
reach cgnat http://100.64.0.1
sudo iptables -F >/dev/null 2>&1; echo "flush-exit=$?"
reach gateway-after-flush "http://$GW"
test -e /var/run/docker.sock && echo "DOCKER-SOCKET-PRESENT" || echo "no docker socket"
test -e '` + sentinel + `' && echo "HOST-HOME-VISIBLE" || echo "host home invisible"
ls /home
`)
	for _, bad := range []string{"REACHABLE", "DOCKER-SOCKET-PRESENT", "HOST-HOME-VISIBLE"} {
		if strings.Contains(out, bad) {
			t.Errorf("isolation breach (%s):\n%s", bad, out)
		}
	}
	if strings.Contains(out, "flush-exit=0") {
		t.Errorf("sudo iptables -F succeeded:\n%s", out)
	}
	for _, want := range []string{"blocked gateway", "blocked metadata", "blocked gateway-after-flush", "no docker socket", "host home invisible"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}

	// Public HTTPS works (skip if the machine has no internet at all).
	if o, code := p.sh(`curl -sS -m 10 -o /dev/null -w '%{http_code}' https://example.com`); code != 0 || !strings.HasPrefix(o, "200") {
		if _, err := os.Stat("/etc/resolv.conf"); err == nil {
			t.Errorf("public HTTPS failed (%d): %s", code, o)
		}
	}
}

// The capability bounding set must exclude NET_ADMIN/NET_RAW for every session,
// including root obtained through sudo.
func TestCapabilitiesAreDropped(t *testing.T) {
	p := newProject(t, "caps", "")
	out := p.mustSh(`
echo "user: $(grep CapBnd /proc/self/status | awk '{print $2}')"
echo "sudo: $(sudo grep CapBnd /proc/self/status | awk '{print $2}')"
echo "sudo-works: $(sudo -n id -u)"
sudo apt-get update -qq >/dev/null 2>&1 && echo "apt-works"
`)
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "user:") || strings.HasPrefix(line, "sudo:") {
			mask := strings.TrimSpace(strings.SplitN(line, ":", 2)[1])
			var v uint64
			for _, c := range mask {
				v = v*16 + uint64(strings.IndexRune("0123456789abcdef", c))
			}
			for bit, name := range map[uint]string{12: "NET_ADMIN", 13: "NET_RAW", 21: "SYS_ADMIN", 19: "SYS_PTRACE", 16: "SYS_MODULE"} {
				if v&(1<<bit) != 0 {
					t.Errorf("%s has %s in its bounding set (%s)", strings.SplitN(line, ":", 2)[0], name, mask)
				}
			}
		}
	}
	if !strings.Contains(out, "sudo-works: 0") || !strings.Contains(out, "apt-works") {
		t.Errorf("sudo/apt must keep working with the reduced set:\n%s", out)
	}
}

// Test 4: ownership, protected git files, commits and worktrees.
func TestOwnershipAndGit(t *testing.T) {
	p := newProject(t, "gitproj", "")
	out := p.mustSh(`
echo hello > made-in-box.txt
echo "owner=$(stat -c %u made-in-box.txt)"
git add made-in-box.txt && git commit -qm "from the box" && echo commit-ok
(echo x > .git/hooks/pre-commit) 2>/dev/null && echo HOOK-WRITABLE || echo hooks-readonly
(echo '[alias]' >> .git/config) 2>/dev/null && echo CONFIG-WRITABLE || echo config-readonly
git config core.hooksPath /tmp/h >/dev/null 2>&1 && echo HOOKSPATH-SET || echo hookspath-blocked
git worktree add .worktrees/feature -b feature >/dev/null 2>&1 && echo worktree-ok
echo wt > .worktrees/feature/wt.txt && git -C .worktrees/feature add wt.txt && git -C .worktrees/feature commit -qm "in worktree" && echo worktree-commit-ok
`)
	uid := os.Getuid()
	for _, want := range []string{"owner=" + strconv.Itoa(uid), "commit-ok", "hooks-readonly", "config-readonly", "hookspath-blocked", "worktree-ok", "worktree-commit-ok"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	for _, bad := range []string{"HOOK-WRITABLE", "CONFIG-WRITABLE", "HOOKSPATH-SET"} {
		if strings.Contains(out, bad) {
			t.Errorf("breach %s:\n%s", bad, out)
		}
	}
	// The host sees the commits and can use the worktree directly.
	if log := p.git("log", "--all", "--oneline"); !strings.Contains(log, "from the box") || !strings.Contains(log, "in worktree") {
		t.Errorf("host git log:\n%s", log)
	}
	if st := p.git("-C", filepath.Join(p.Dir, ".worktrees", "feature"), "status", "--short"); st != "" {
		t.Errorf("worktree dirty on the host: %q", st)
	}
	if ex, _ := os.ReadFile(filepath.Join(p.Dir, ".git", "info", "exclude")); !strings.Contains(string(ex), "/.worktrees/") {
		t.Errorf(".worktrees not git-excluded: %q", ex)
	}
	if fi, err := os.Stat(filepath.Join(p.Dir, "made-in-box.txt")); err != nil {
		t.Error(err)
	} else if fi.Mode().Perm()&0o200 == 0 {
		t.Error("file made in the box is not writable by the host user")
	}
}

// Test 6: a project config cannot add mounts or loosen the network.
func TestProjectConfigIsUntrusted(t *testing.T) {
	p := newProject(t, "untrusted", "")
	evil := t.TempDir()
	if err := os.WriteFile(filepath.Join(p.Dir, ".airlock.toml"), []byte(`
[network]
mode = "open"
allow_cidrs = ["192.168.0.0/16"]
[[mount]]
name = "evil"
host = "`+evil+`"
mode = "rw"
[git]
mode = "push"
token_env = "HOME"
[sandbox]
memory = "2g"
`), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	_, stderr, code := p.run(ctx, nil, "config", "show")
	if code != 0 {
		t.Fatalf("config show failed: %s", stderr)
	}
	for _, want := range []string{"mount", "git.mode", "network.allow_cidrs", "would loosen"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("rejected keys not reported (%q):\n%s", want, stderr)
		}
	}

	out := p.mustSh(`
echo "net-mode=$(cat /run/airlock/net-mode)"
test -e /mnt/evil && echo EVIL-MOUNTED || echo no-evil-mount
curl -sS -m 3 -o /dev/null http://169.254.169.254 2>/dev/null && echo METADATA-REACHABLE || echo metadata-blocked
env | grep -c '^AIRLOCK_GIT_TOKEN=' || true
`)
	for _, want := range []string{"net-mode=open-no-lan", "no-evil-mount", "metadata-blocked"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "EVIL-MOUNTED") || strings.Contains(out, "METADATA-REACHABLE") {
		t.Errorf("untrusted project config took effect:\n%s", out)
	}
}

// Test 7: same-name projects in different places get different sandboxes.
func TestSameNameProjectsAreSeparate(t *testing.T) {
	a := newProject(t, "same", "")
	b := newProject(t, "same", "")
	a.mustSh("echo A > marker.txt; hostname")
	hostB := b.mustSh("test -e marker.txt && echo LEAK || echo clean; hostname")
	if strings.Contains(hostB, "LEAK") {
		t.Errorf("project B sees project A's files:\n%s", hostB)
	}
	hostA, _ := a.sh("hostname")
	if hostA == strings.Split(hostB, "\n")[len(strings.Split(hostB, "\n"))-1] {
		t.Errorf("both projects share container %q", hostA)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	ls, _, _ := a.run(ctx, nil, "ls", "--json")
	if strings.Count(ls, `"project"`) < 2 {
		t.Errorf("ls does not list both sandboxes:\n%s", ls)
	}
}

// Test 5: declared mounts exist with the right mode and are described to agents.
func TestMountsAndAgentInstructions(t *testing.T) {
	notes := t.TempDir()
	skills := t.TempDir()
	os.WriteFile(filepath.Join(notes, "README.md"), []byte("notes"), 0o644)
	os.MkdirAll(filepath.Join(skills, "demo"), 0o755)
	os.WriteFile(filepath.Join(skills, "demo", "SKILL.md"), []byte("---\nname: demo\ndescription: demo skill\n---\nhi\n"), 0o644)
	p := newProject(t, "mounts", `
[[mount]]
name = "notes"
host = "`+notes+`"
mode = "rw"
description = "Personal knowledge base."
[[mount]]
name = "skills"
host = "`+skills+`"
mode = "ro"
skills = "."
`)
	out := p.mustSh(`
cat /mnt/notes/README.md; echo
echo w > /mnt/notes/written.txt && echo notes-writable
(echo w > /mnt/skills/x.txt) 2>/dev/null && echo SKILLS-WRITABLE || echo skills-readonly
grep -c 'Personal knowledge base' /AGENTS.md
grep -c '/mnt/skills' /AGENTS.md
cat /etc/airlock/pi.args
`)
	for _, want := range []string{"notes-writable", "skills-readonly", "--skill", "/mnt/skills"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "SKILLS-WRITABLE") {
		t.Error("read-only mount is writable")
	}
	if _, err := os.Stat(filepath.Join(notes, "written.txt")); err != nil {
		t.Errorf("write to the rw mount did not reach the host: %v", err)
	}
}

// Test 2 (without the UI): persistent keeps installed tools, ephemeral starts clean.
func TestPersistentAndEphemeral(t *testing.T) {
	p := newProject(t, "persist", "")
	p.mustSh("sudo apt-get update -qq >/dev/null 2>&1; sudo apt-get install -y -qq cowsay >/dev/null 2>&1; echo installed > /opt/airlock-marker 2>/dev/null || sudo sh -c 'echo installed > /opt/airlock-marker'")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if _, se, code := p.run(ctx, nil, "stop"); code != 0 {
		t.Fatalf("stop: %s", se)
	}
	out := p.mustSh("test -x /usr/games/cowsay && echo cowsay-kept; cat /opt/airlock-marker")
	if !strings.Contains(out, "cowsay-kept") || !strings.Contains(out, "installed") {
		t.Errorf("persistent sandbox lost its state across stop/start:\n%s", out)
	}

	e := newProject(t, "ephem", "[sandbox]\nlifetime = \"ephemeral\"\n")
	e.mustSh("sudo sh -c 'echo here > /opt/airlock-marker'; cat /opt/airlock-marker")
	out = e.mustSh("test -e /opt/airlock-marker && echo STALE || echo clean-start")
	if !strings.Contains(out, "clean-start") {
		t.Errorf("ephemeral sandbox kept state:\n%s", out)
	}
}

// siblingService starts a container on the airlock network that answers every
// connection with a banner, and returns its IP. It sits on a private address,
// like a LAN machine or the host would: a sandbox that can reach it can reach
// private address space.
func siblingService(t *testing.T) string {
	t.Helper()
	name := "airlock-it-listener-" + strconv.Itoa(os.Getpid())
	_ = exec.Command("docker", "network", "create", "airlock").Run() // ok if it exists
	out, err := exec.Command("docker", "run", "-d", "--rm", "--name", name, "--network", "airlock", "alpine:latest",
		"sh", "-c", "while true; do echo HOSTSERVICE | nc -l -p 9999; done").CombinedOutput()
	if err != nil {
		t.Skipf("cannot start the listener container (alpine image unavailable?): %v\n%s", err, out)
	}
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "-f", name).Run() })
	ip, err := exec.Command("docker", "inspect", "-f", "{{(index .NetworkSettings.Networks \"airlock\").IPAddress}}", name).Output()
	if err != nil || strings.TrimSpace(string(ip)) == "" {
		t.Fatalf("listener has no IP: %v", err)
	}
	time.Sleep(time.Second)
	return strings.TrimSpace(string(ip))
}

// fromBox tries to read the banner of ip:9999 from inside the sandbox.
func fromBox(p *project, ip string) string {
	out, _ := p.sh(`timeout 6 bash -c 'exec 3<>/dev/tcp/` + ip + `/9999 && read -t 3 -u 3 line && echo "got:$line"' 2>/dev/null || echo "no-connection"`)
	return out
}

// A real oracle for "private address space is unreachable": a service on a
// private address that the sandbox must not be able to connect to. The positive
// control in `open` mode proves the test would notice a missing firewall.
func TestPrivateAddressesUnreachable(t *testing.T) {
	ip := siblingService(t)

	open := newProject(t, "ctl", "[network]\nmode = \"open\"\n")
	if got := fromBox(open, ip); !strings.Contains(got, "got:HOSTSERVICE") {
		t.Fatalf("positive control failed: in open mode the sibling at %s must be reachable, got %q", ip, got)
	}

	p := newProject(t, "fw", "")
	if got := fromBox(p, ip); strings.Contains(got, "HOSTSERVICE") {
		t.Fatalf("the sandbox reached a private-range service at %s: %q", ip, got)
	} else if !strings.Contains(got, "no-connection") {
		t.Fatalf("unexpected result: %q", got)
	}

	// The rules are really there for every blocked range (read as real root).
	rules := p.rootExec("iptables", "-S", "OUTPUT")
	for _, cidr := range []string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "169.254.0.0/16", "100.64.0.0/10", "224.0.0.0/4", "198.18.0.0/15"} {
		if !strings.Contains(rules, "-d "+cidr+" -j REJECT") {
			t.Errorf("no REJECT rule for %s:\n%s", cidr, rules)
		}
	}
	if !strings.Contains(rules, "ESTABLISHED") || !strings.Contains(rules, "-o lo -j ACCEPT") {
		t.Errorf("loopback/established rules missing:\n%s", rules)
	}
	if !strings.Contains(rules, "--dport 53") {
		t.Errorf("DNS exception for the upstream resolvers is missing:\n%s", rules)
	}
}

// allow_cidrs punches a hole in the LAN block.
func TestAllowCIDRsPunchesHole(t *testing.T) {
	ip := siblingService(t)
	p := newProject(t, "hole", "[network]\nallow_cidrs = [\""+ip+"/32\"]\n")
	if got := fromBox(p, ip); !strings.Contains(got, "got:HOSTSERVICE") {
		t.Errorf("allow_cidrs did not open %s: %q", ip, got)
	}
	other := newProject(t, "hole2", "[network]\nallow_cidrs = [\"203.0.113.9/32\"]\n")
	if got := fromBox(other, ip); strings.Contains(got, "HOSTSERVICE") {
		t.Errorf("an unrelated allow_cidrs entry opened %s: %q", ip, got)
	}
}
