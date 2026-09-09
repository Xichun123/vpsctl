package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// An executable boundary, not a mocked Result: scripts really run in a local
// shell, with a private HOME and no network or access to user SSH configuration.
func fakeSSH(t *testing.T) App {
	t.Helper()
	root := t.TempDir()
	bin := filepath.Join(root, "bin")
	if err := os.Mkdir(bin, 0700); err != nil {
		t.Fatal(err)
	}
	script := `#!/bin/sh
for arg do
 if [ "$last" = -E ]; then diagnostic=$arg; fi
 previous=$last
 last=$arg
done
case "$previous" in
 auth-failed) echo 'Permission denied (publickey).' >> "$diagnostic"; exit 255;;
 changed-key) echo 'Host key verification failed.' >> "$diagnostic"; exit 255;;
 connect-failed) echo 'Connection refused' >> "$diagnostic"; exit 255;;
 disconnected) echo 'connection lost' >&2; exit 255;;
 missing-marker) exit 0;;
esac
exec /bin/sh -c "$last"
`
	if err := os.WriteFile(filepath.Join(bin, "ssh"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("HOME", root)
	return App{MaxOutput: 1024, Stdin: strings.NewReader("")}
}

func TestScriptCompletionAndErrors(t *testing.T) {
	a := fakeSSH(t)
	ctx := context.Background()
	r := a.Script(ctx, "fixture", "printf 'hello'; printf 'warning' >&2; exit 255")
	if r.Status != "completed" || r.ExitCode == nil || *r.ExitCode != 255 || r.Stdout != "hello" || r.Stderr != "warning" || r.Error != nil || r.Truncated {
		t.Fatalf("unexpected completion: %+v", r)
	}
	for host, code := range map[string]string{"auth-failed": "AUTH_FAILED", "changed-key": "HOST_KEY_REJECTED", "connect-failed": "CONNECT_FAILED", "disconnected": "OUTCOME_UNKNOWN", "missing-marker": "OUTCOME_UNKNOWN"} {
		r := a.Script(ctx, host, "exit 0")
		if r.Error == nil || r.Error.Code != code || r.ExitCode != nil || r.OK() {
			t.Fatalf("%s: %+v", host, r)
		}
	}
	r = a.Script(ctx, "-oProxyCommand=bad", "touch should-not-run")
	if r.Error == nil || r.Error.Code != "INVALID_ARGUMENT" {
		t.Fatal(r)
	}
	r = a.Script(ctx, "fixture", "echo\x00bad")
	if r.Error == nil {
		t.Fatal(r)
	}
}

func TestScriptInputIsolation(t *testing.T) {
	a := fakeSSH(t)
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	// Include a heredoc and a large script to cover quoting and argv limits.
	script := "cat >/dev/null\nread value || :\ncat <<'DATA'\n$literal 'quoted'\nDATA\n" + strings.Repeat("# padding\n", 32768) + "printf 'finished\\n'\n"
	for _, args := range [][]string{{"exec", "fixture", "--stdin"}, {"cluster", "--hosts", "fixture,other", "--stdin"}} {
		a.Stdin = strings.NewReader(script)
		r := a.Run(context.Background(), args)
		if !r.OK() {
			t.Fatalf("%v: %+v", args, r)
		}
		if args[0] == "exec" && r.Stdout != "$literal 'quoted'\nfinished\n" {
			t.Fatalf("child consumed script input: %+v", r)
		}
		if args[0] == "cluster" {
			b, _ := json.Marshal(r.Data)
			if strings.Count(string(b), "finished") != 2 {
				t.Fatalf("batch lost script commands: %s", b)
			}
		}
	}
	entries, err := os.ReadDir(tmp)
	if err != nil || len(entries) != 0 {
		t.Fatalf("temporary scripts or diagnostics were not cleaned: %v %v", entries, err)
	}
}

func TestDisconnectDoesNotTrustRemoteStderr(t *testing.T) {
	a := fakeSSH(t)
	bin := strings.Split(os.Getenv("PATH"), string(os.PathListSeparator))[0]
	// Drop the completion marker after executing the actual remote wrapper.
	ssh := "#!/bin/sh\nfor arg do last=$arg; done\n/bin/sh -c \"$last\" 2>&1 | sed '/^VPSCTL_[0-9a-f]*:[0-9]*$/d' >&2\nexit 255\n"
	if err := os.WriteFile(filepath.Join(bin, "ssh"), []byte(ssh), 0700); err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"curl: Connection refused", "Permission denied (publickey).", "Host key verification failed."} {
		applied := filepath.Join(t.TempDir(), "applied")
		script := "touch " + quote(applied) + "; printf '%s\\n' " + quote(text) + " >&2"
		for _, direct := range []bool{false, true} {
			var r Result
			if direct {
				r = a.SSH(context.Background(), "fixture", "sh -c "+quote(script), nil)
			} else {
				r = a.Script(context.Background(), "fixture", script)
			}
			if _, err := os.Stat(applied); err != nil {
				t.Fatal(err)
			}
			if r.Status != "unknown" || r.Error == nil || r.Error.Code != "OUTCOME_UNKNOWN" || !strings.Contains(r.Stderr, text) {
				t.Fatalf("misclassified remote output: %+v", r)
			}
		}
	}
}

func TestScriptCaptureAndCancellation(t *testing.T) {
	a := fakeSSH(t)
	a.MaxOutput = 3
	r := a.Script(context.Background(), "fixture", "printf 'abcdef'; printf '123456' >&2")
	if !r.OK() || r.Stdout != "abc" || r.Stderr != "123" || !r.Truncated {
		t.Fatal(r)
	}
	r = a.Script(context.Background(), "fixture", "printf '123' >&2")
	if !r.OK() || r.Stderr != "123" || r.Truncated {
		t.Fatal(r)
	}
	a.Timeout = 50 * time.Millisecond
	start := time.Now()
	r = a.Script(context.Background(), "fixture", "sleep 30")
	if r.Status != "unknown" || r.ExitCode != nil || time.Since(start) > 3*time.Second {
		t.Fatalf("timeout lost semantics: %+v", r)
	}
}

func TestSSHAuthenticationAndArgumentBoundary(t *testing.T) {
	a := App{Config: "/tmp/config with spaces"}
	args := a.sshArgs("fixture", "printf '%s' '$HOME'")
	joined := strings.Join(args, "\n")
	for _, required := range []string{"BatchMode=yes", "PasswordAuthentication=no", "KbdInteractiveAuthentication=no", "StrictHostKeyChecking=yes", "ForwardAgent=no", "PermitLocalCommand=no"} {
		if !strings.Contains(joined, required) {
			t.Errorf("missing %s", required)
		}
	}
	if args[len(args)-2] != "fixture" || args[len(args)-1] != "printf '%s' '$HOME'" {
		t.Fatal(args)
	}
}

func TestCLIJSONAndScriptInputs(t *testing.T) {
	a := fakeSSH(t)
	for _, args := range [][]string{{"--help"}, {"--version"}, {"exec", "--help"}, {"unknown"}, {"--timeout", "invalid"}, {"exec", "fixture"}, {"exec", "fixture", "echo ok", "--stdin"}} {
		r := a.Run(context.Background(), args)
		b, err := json.Marshal(r)
		if err != nil || !json.Valid(b) {
			t.Fatalf("%v: %s %v", args, b, err)
		}
		for _, key := range []string{`"status"`, `"exit_code"`, `"stdout"`, `"stderr"`, `"error"`, `"truncated"`} {
			if !strings.Contains(string(b), key) {
				t.Errorf("missing %s: %s", key, b)
			}
		}
	}
	a.Stdin = strings.NewReader("printf '%s' '$HOME `not executed` \"quoted\"'\n")
	r := a.Run(context.Background(), []string{"exec", "fixture", "--stdin"})
	if !r.OK() || r.Stdout != "$HOME `not executed` \"quoted\"" {
		t.Fatal(r)
	}
	dir := t.TempDir()
	file := filepath.Join(dir, "script.sh")
	if err := os.WriteFile(file, []byte("pwd"), 0600); err != nil {
		t.Fatal(err)
	}
	r = a.Run(context.Background(), []string{"exec", "fixture", "--script-file", file, "--cwd", dir})
	if !r.OK() || strings.TrimSpace(r.Stdout) != dir {
		t.Fatal(r)
	}
}

func TestClusterExplicitTargetsAndResults(t *testing.T) {
	a := fakeSSH(t)
	for _, args := range [][]string{{"--hosts", ""}, {"--hosts", "fixture,fixture", "true"}, {"--hosts", "fixture", "--parallel", "0", "true"}} {
		if r := a.Cluster(context.Background(), args); r.Error == nil {
			t.Fatal(r)
		}
	}
	r := a.Cluster(context.Background(), []string{"--hosts", "fixture,auth-failed", "--parallel", "2", "printf ok"})
	if r.Error == nil || r.Error.Code != "BATCH_FAILED" {
		t.Fatal(r)
	}
	b, _ := json.Marshal(r.Data)
	if !strings.Contains(string(b), `"stdout":"ok"`) || !strings.Contains(string(b), "AUTH_FAILED") {
		t.Fatal(string(b))
	}
}
