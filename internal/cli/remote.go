package cli

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// runRemote re-runs the same command on another machine over ssh. Paths in the
// command refer to the remote machine. A TTY is requested so the session works
// from any terminal, including minimal SSH clients.
func runRemote(e *env, argv []string) int {
	var rest []string
	for i := 0; i < len(argv); i++ {
		switch {
		case argv[i] == "--remote":
			i++ // skip the host
		case strings.HasPrefix(argv[i], "--remote="):
		default:
			rest = append(rest, argv[i])
		}
	}
	if strings.HasPrefix(e.a.remote, "-") {
		fmt.Fprintf(e.stderr, "airlock: invalid remote host %q\n", e.a.remote)
		return 2
	}
	ssh, err := sshPath()
	if err != nil {
		fmt.Fprintln(e.stderr, "airlock: --remote needs the ssh client on this machine")
		return 1
	}
	remoteCmd := "airlock"
	for _, a := range rest {
		remoteCmd += " " + shellQuote(a)
	}
	cmd := exec.Command(ssh, "-t", e.a.remote, remoteCmd)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, e.stdout, e.stderr
	if err := cmd.Run(); err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return ee.ExitCode()
		}
		fmt.Fprintf(e.stderr, "airlock: ssh: %v\n", err)
		return 1
	}
	return 0
}

func shellQuote(s string) string {
	if s != "" && strings.IndexFunc(s, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("@%+=:,./-_", r))
	}) < 0 {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
