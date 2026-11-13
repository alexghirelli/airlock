//go:build integration

package tests

import (
	"os/exec"
	"strings"
	"testing"
	"time"
)

// The setting reaches the sandbox: the manifest says how long, a watcher runs, and
// it is pointed at the container's main process. Without the setting there is none.
func TestIdleStopIsWiredIntoTheSandbox(t *testing.T) {
	p := newProject(t, "idle-wired", "[sandbox]\nidle_stop = \"5m\"\n")
	out := p.mustSh(`echo "manifest: $(jq .idle_stop_seconds /etc/airlock/manifest.json)"
echo "watchers: $(ps -eo args= | grep -c '^/bin/bash /usr/local/bin/airlock-idle-watch 300$')"
echo "main: $(ps -o args= -p "$(cat /run/airlock/main-pid)")"`)
	for _, want := range []string{"manifest: 300", "watchers: 1", "main: sleep infinity"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	off := newProject(t, "idle-off", "")
	if out := off.mustSh(`ps -eo args= | grep -c '^/bin/bash /usr/local/bin/airlock-idle-watch' || true`); strings.TrimSpace(out) != "0" {
		t.Errorf("a watcher runs although idle_stop is not set:\n%s", out)
	}
}

// sandbox.idle_stop: the watcher ends the container's main process after a whole
// period without a terminal or command from outside and without an agent working.
// The test points it at a stand-in process (not the real main one) and turns the
// clock up with AIRLOCK_IDLE_STEP.
func TestIdleWatcherStopsAnUnusedSandbox(t *testing.T) {
	p := newProject(t, "idle", "")
	pid := strings.TrimSpace(p.mustSh(`setsid nohup sleep 600 >/dev/null 2>&1 </dev/null &
echo $! > /tmp/fake-main
setsid nohup env AIRLOCK_IDLE_STEP=1 AIRLOCK_IDLE_MAIN_PID_FILE=/tmp/fake-main airlock-idle-watch 3 >/tmp/idle.log 2>&1 </dev/null &
cat /tmp/fake-main`))
	name := p.containerName()
	inBox := func(args ...string) error {
		return exec.Command("docker", append([]string{"exec", name}, args...)...).Run()
	}
	alive := func() bool { return inBox("kill", "-0", pid) == nil }

	// Something started from outside keeps the sandbox in use, however long it takes.
	busy := exec.Command("docker", "exec", name, "sleep", "8")
	if err := busy.Start(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(6 * time.Second)
	if !alive() {
		t.Fatal("the watcher stopped the sandbox while a command was running in it")
	}
	_ = busy.Wait()

	// Then nothing is using it, and it stops.
	deadline := time.Now().Add(90 * time.Second)
	for alive() {
		if time.Now().After(deadline) {
			log, _ := exec.Command("docker", "exec", name, "cat", "/tmp/idle.log").CombinedOutput()
			t.Fatalf("an idle sandbox was not stopped within 90s; watcher log:\n%s", log)
		}
		time.Sleep(2 * time.Second)
	}
	if log, _ := exec.Command("docker", "exec", name, "cat", "/tmp/idle.log").CombinedOutput(); !strings.Contains(string(log), "sandbox.idle_stop") {
		t.Errorf("the watcher did not say why it stopped:\n%s", log)
	}
}
