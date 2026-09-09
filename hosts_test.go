package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func managementHome(t *testing.T) string {
	t.Helper()
	// Keep the Unix control socket short, and resolve macOS /tmp's symlink.
	home, e := os.MkdirTemp("/tmp", "vpsctl-test-")
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { os.RemoveAll(home) })
	home, e = filepath.EvalSymlinks(home)
	if e != nil {
		t.Fatal(e)
	}
	t.Setenv("HOME", home)
	return home
}
func managementWrite(t *testing.T, path, content string) {
	t.Helper()
	if e := os.MkdirAll(filepath.Dir(path), 0700); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(path, []byte(content), 0600); e != nil {
		t.Fatal(e)
	}
}
func managementRead(t *testing.T, path string) string {
	t.Helper()
	b, e := os.ReadFile(path)
	if e != nil {
		t.Fatal(e)
	}
	return string(b)
}

func TestHostOfflineIncludesAndLabels(t *testing.T) {
	home := managementHome(t)
	path := filepath.Join(home, ".ssh", "config")
	managementWrite(t, path, "# password: NEVER_EMIT_THIS\n# vpsctl-labels: production eu\nHost prod *.wild !excluded\n HostName 192.0.2.1\nInclude parts/*.conf\nMatch exec \"touch /do-not-run\"\n Host conditional\n")
	included := filepath.Join(home, ".ssh", "parts", "one.conf")
	managementWrite(t, included, "Host include-alias\nInclude ../config\n")
	t.Setenv("PATH", home)
	a := App{Config: path}
	r := a.Host(context.Background(), []string{"list"})
	if !r.OK() {
		t.Fatalf("%+v error=%+v", r, r.Error)
	}
	data, _ := json.Marshal(r.Data)
	text := string(data)
	for _, want := range []string{"prod", "production", "include-alias", included, "conditional"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q: %s", want, text)
		}
	}
	for _, bad := range []string{"NEVER_EMIT_THIS", "*.wild", "excluded"} {
		if strings.Contains(text, bad) {
			t.Fatal(text)
		}
	}
	r = a.Host(context.Background(), []string{"find", "production"})
	if !r.OK() || len(r.Data.([]hostEntry)) != 1 {
		t.Fatalf("%+v error=%+v", r, r.Error)
	}
	r = a.Host(context.Background(), []string{"show", "prod"})
	if r.Error == nil || r.Error.Code != "UNSAFE_CONFIG" {
		t.Fatalf("Match exec not refused: %+v", r)
	}
}

func TestHostEditsBackupPreserveAndLock(t *testing.T) {
	home := managementHome(t)
	path := filepath.Join(home, ".ssh", "config")
	original := "# global\nServerAliveInterval 20\nHost other\n    HostName old.example\n# keep secret locally\n"
	managementWrite(t, path, original)
	a := App{Config: path}
	ctx := context.Background()
	r := a.Host(ctx, []string{"add", "--alias", "new", "--hostname", "new.example", "--user", "alice", "--port", "2222"})
	if !r.OK() {
		t.Fatalf("%+v error=%+v", r, r.Error)
	}
	if got := managementRead(t, r.Data.(map[string]string)["backup"]); got != original {
		t.Fatal("backup changed")
	}
	text := managementRead(t, path)
	if !strings.HasPrefix(text, "# global\nServerAliveInterval 20\nHost new\n") || !strings.Contains(text, "Host other\n    HostName old.example\n# keep secret locally\n") {
		t.Fatal(text)
	}
	r = a.Host(ctx, []string{"update", "--alias", "new", "--user", "bob"})
	if !r.OK() {
		t.Fatalf("%+v error=%+v", r, r.Error)
	}
	text = managementRead(t, path)
	if !strings.Contains(text, "user \"bob\"") || !strings.Contains(text, "hostname \"new.example\"") {
		t.Fatal(text)
	}
	r = a.Host(ctx, []string{"delete", "--alias", "new"})
	if !r.OK() {
		t.Fatalf("%+v error=%+v", r, r.Error)
	}
	if strings.Contains(managementRead(t, path), "Host new") {
		t.Fatal("not deleted")
	}
	managementWrite(t, path+".vpsctl-lock", "")
	r = a.Host(ctx, []string{"add", "--alias", "locked", "--hostname", "x"})
	if r.Error == nil || r.Error.Code != "LOCKED" {
		t.Fatalf("%+v error=%+v", r, r.Error)
	}
}

func TestHostRefusesComplexOrUnsafeEdits(t *testing.T) {
	home := managementHome(t)
	path := filepath.Join(home, ".ssh", "config")
	a := App{Config: path}
	for _, content := range []string{"Include *.conf\nHost target\n HostName a\n", "Host target other\n HostName a\n", "Host target\n ProxyCommand secret\n", "Host target\n HostName a\nHost target\n HostName b\n", "Match all\nHost target\n"} {
		managementWrite(t, path, content)
		r := a.Host(context.Background(), []string{"delete", "--alias", "target"})
		if r.OK() {
			t.Fatalf("accepted complex config: %s", content)
		}
		if got := managementRead(t, path); got != content {
			t.Fatal("modified rejected config")
		}
	}
	managementWrite(t, path, "Host target\n HostName a\n")
	r := a.Host(context.Background(), []string{"update", "--alias", "target", "--hostname", "x\nProxyCommand evil"})
	if r.OK() {
		t.Fatal("accepted config injection")
	}
	target := filepath.Join(home, "target")
	managementWrite(t, target, "untouched")
	if e := os.Remove(path); e != nil {
		t.Fatal(e)
	}
	if e := os.Symlink(target, path); e != nil {
		t.Fatal(e)
	}
	r = a.Host(context.Background(), []string{"add", "--alias", "new", "--hostname", "x"})
	if r.OK() || managementRead(t, target) != "untouched" {
		t.Fatal("followed config symlink")
	}
}

// All real ssh invocations use -G and an explicit config with numeric HostName:
// no server connection, DNS lookup, or user/system SSH configuration is needed.
func hostSSHField(t *testing.T, path, key string) string {
	t.Helper()
	ssh, e := exec.LookPath("ssh")
	if e != nil {
		t.Skip("OpenSSH unavailable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, e := exec.CommandContext(ctx, ssh, "-G", "-F", path, "target").CombinedOutput()
	if e != nil {
		t.Fatalf("ssh -G: %v\n%s", e, out)
	}
	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(line, key+" ") {
			return strings.TrimPrefix(line, key+" ")
		}
	}
	t.Fatalf("ssh -G missing %s: %s", key, out)
	return ""
}

func TestHostIdentityEscapesWithSystemSSH(t *testing.T) {
	home := managementHome(t)
	path := filepath.Join(home, "config")
	for _, tt := range []struct {
		input, want string
	}{
		{`/tmp/key\name`, `/tmp/key\name`},
		{`/tmp/key\\name`, `/tmp/key\name`},
		{`"/tmp/key\name"`, `/tmp/key\name`},
		{`'/tmp/key\name'`, `/tmp/key\name`},
		{`/tmp/key\ name`, `/tmp/key name`},
		{`"/tmp/key\ name"`, `/tmp/key\ name`},
		{`'/tmp/key\ name'`, `/tmp/key\ name`},
		{`/tmp/key\#name`, `/tmp/key\#name`},
		{`/tmp/key#name`, `/tmp/key#name`},
		{`"/tmp/key"#name`, `/tmp/key#name`},
		{`/tmp/key\"name`, `/tmp/key"name`},
		{`"/tmp/key\'name"`, `/tmp/key'name`},
		{`'/tmp/key\"name'`, `/tmp/key"name`},
		{`/tmp/key\=name`, `/tmp/key\=name`},
		{`/tmp/key\`, `/tmp/key\`},
	} {
		t.Run(tt.input, func(t *testing.T) {
			original := "Host target\n HostName 192.0.2.1\n User alice\n IdentityFile " + tt.input + "\n"
			managementWrite(t, path, original)
			before := hostSSHField(t, path, "identityfile")
			if before != tt.want {
				t.Fatalf("OpenSSH parsed %q as %q, want %q", tt.input, before, tt.want)
			}
			w, e := configWords("IdentityFile " + tt.input)
			if e != nil || !reflect.DeepEqual(w, []string{"IdentityFile", before}) {
				t.Fatalf("configWords disagrees with OpenSSH: %q %v; want %q", w, e, before)
			}
			r := (App{Config: path}).Host(context.Background(), []string{"update", "--alias", "target", "--user", "bob"})
			if simpleValue(before) {
				if !r.OK() {
					t.Fatalf("safe update failed: %+v", r.Error)
				}
				if got := hostSSHField(t, path, "user"); got != "bob" {
					t.Errorf("user not updated: %q", got)
				}
			} else {
				if r.Error == nil || r.Error.Code != "UNSAFE_CONFIG" {
					t.Errorf("unsafe rewrite not refused: %+v", r)
				}
				if got := managementRead(t, path); got != original {
					t.Error("rejected config modified")
				}
			}
			if after := hostSSHField(t, path, "identityfile"); after != before {
				t.Errorf("IdentityFile changed: before %q, after %q", before, after)
			}
		})
	}
}

func TestHostIncludeSpecialCharactersRefuseMatch(t *testing.T) {
	home := managementHome(t)
	path := filepath.Join(home, "config")
	marker := filepath.Join(home, "match-executed")
	for _, tt := range []struct {
		pattern, filename string
	}{
		{`include#fragment`, `include#fragment`},
		{`include\name`, `includename`},
		{`include\\name`, `includename`},
		{`include\\\\name`, `include\name`},
		{`include\ name`, `include name`},
	} {
		t.Run(tt.pattern, func(t *testing.T) {
			included := filepath.Join(home, tt.filename)
			managementWrite(t, included, "Host target\n User included-user\n")
			managementWrite(t, path, "Include "+filepath.Join(home, tt.pattern)+"\nHost target\n HostName 192.0.2.1\n")
			// First prove real OpenSSH follows this exact Include, without Match.
			if got := hostSSHField(t, path, "user"); got != "included-user" {
				t.Fatalf("OpenSSH did not follow Include: %q", got)
			}
			managementWrite(t, included, "Host target\nMatch exec \"touch "+marker+"\"\n User nobody\n")
			entries, match, e := scanHosts(path)
			if e != nil || !match || len(entries) != 2 || entries[0].Source != included {
				t.Errorf("Include missed: entries=%+v match=%v err=%v", entries, match, e)
			}
			r := (App{Config: path, Timeout: 5 * time.Second}).Host(context.Background(), []string{"show", "target"})
			if r.Error == nil || r.Error.Code != "UNSAFE_CONFIG" {
				t.Errorf("Match not refused: %+v error=%+v", r, r.Error)
			}
			if _, e := os.Stat(marker); !os.IsNotExist(e) {
				t.Fatalf("Match exec marker must not exist: %v", e)
			}
		})
	}
}

func TestConfigWordsCommentBoundaries(t *testing.T) {
	for _, tt := range []struct {
		input string
		want  []string
	}{
		{"  # comment", nil},
		{`Include /tmp/include#fragment # comment`, []string{"Include", "/tmp/include#fragment"}},
		{`Include "/tmp/include#fragment"`, []string{"Include", "/tmp/include#fragment"}},
		{`IdentityFile ""#fragment`, []string{"IdentityFile", "#fragment"}},
		{`Include /tmp/one /tmp/two#fragment # comment`, []string{"Include", "/tmp/one", "/tmp/two#fragment"}},
	} {
		w, e := configWords(tt.input)
		if e != nil || !reflect.DeepEqual(w, tt.want) {
			t.Errorf("%q: got %q %v, want %q", tt.input, w, e, tt.want)
		}
	}
}

func TestConfigWordsEqualsAndQuotes(t *testing.T) {
	for _, s := range []string{"HostName=x", "HostName = x", "HostName \"x\" # comment"} {
		w, e := configWords(s)
		if e != nil || len(w) != 2 || w[0] != "HostName" || w[1] != "x" {
			t.Fatalf("%q: %q %v", s, w, e)
		}
	}
}
