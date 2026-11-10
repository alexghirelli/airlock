package doctor

import (
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/alexghirelli/airlock/internal/ignore"
)

func touch(t *testing.T, root, rel string) {
	t.Helper()
	p := filepath.Join(root, rel)
	os.MkdirAll(filepath.Dir(p), 0o755)
	if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestFindSecrets(t *testing.T) {
	root := t.TempDir()
	for _, f := range []string{".env", ".env.example", "app/.env.production", "certs/server.pem", "id_rsa", "src/main.go", "node_modules/x/.env", ".git/config", "wp-config.php"} {
		touch(t, root, f)
	}
	got := FindSecrets(root, nil, 50)
	sort.Strings(got)
	want := []string{".env", "app/.env.production", "certs/server.pem", "id_rsa", "wp-config.php"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
	masked := ignore.FromLines(".env*", "*.pem")
	got = FindSecrets(root, masked, 50)
	sort.Strings(got)
	if want := []string{"id_rsa", "wp-config.php"}; !reflect.DeepEqual(got, want) {
		t.Errorf("with masking: got %v, want %v", got, want)
	}
	if n := len(FindSecrets(root, nil, 2)); n != 2 {
		t.Errorf("limit not honored: %d", n)
	}
}

func TestReportFailed(t *testing.T) {
	r := &Report{}
	r.Add("a", OK, "", "")
	r.Add("b", Warn, "", "")
	if r.Failed() {
		t.Error("warnings are not failures")
	}
	r.Add("c", Fail, "", "")
	if !r.Failed() {
		t.Error("a failed check must fail the report")
	}
}

func TestPlatformNotes(t *testing.T) {
	if n := PlatformNotes("windows", `C:\p`, false); len(n) != 1 || !strings.Contains(n[0].Detail, "WSL") {
		t.Errorf("windows: %+v", n)
	}
	if n := PlatformNotes("darwin", "/opt/p", false); len(n) != 1 {
		t.Errorf("darwin outside /Users: %+v", n)
	}
	if n := PlatformNotes("darwin", "/Users/me/p", false); len(n) != 0 {
		t.Errorf("darwin inside /Users: %+v", n)
	}
	if n := PlatformNotes("linux", "/home/me/p", true); len(n) != 1 || !strings.Contains(n[0].Detail, "rootless") {
		t.Errorf("linux rootless: %+v", n)
	}
	if n := PlatformNotes("linux", "/home/me/p", false); len(n) != 0 {
		t.Errorf("linux plain: %+v", n)
	}
}
