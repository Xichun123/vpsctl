package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func managementBinary(t *testing.T, home, name, body string) {
	t.Helper()
	path := filepath.Join(home, "bin", name)
	managementWrite(t, path, "#!/bin/sh\nset -eu\n"+body)
	if e := os.Chmod(path, 0700); e != nil {
		t.Fatal(e)
	}
	t.Setenv("PATH", filepath.Join(home, "bin")+":"+os.Getenv("PATH"))
}

func TestKeyAddDedupRollbackThroughFakeSSH(t *testing.T) {
	home := managementHome(t)
	managementBinary(t, home, "ssh-keygen", `printf '%s\n' "$@" > "$HOME/keygen-args"
exit 0
`)
	managementBinary(t, home, "ssh", `for arg do last=$arg; done
exec /bin/sh -c "$last"
`)
	pub := filepath.Join(home, "key.pub")
	managementWrite(t, pub, "ssh-ed25519 YWJj comment not deployed\n")
	auth := filepath.Join(home, ".ssh", "authorized_keys")
	initial := "ssh-ed25519 ZGVm comment YWJj\n"
	managementWrite(t, auth, initial)
	a := App{Timeout: 5 * time.Second}
	ctx := context.Background()
	r := a.Key(ctx, []string{"add", "host", "--public-key", pub})
	if !r.OK() {
		t.Fatalf("%+v", r)
	}
	id := r.Data.(map[string]string)["backup"]
	if !strings.Contains(managementRead(t, filepath.Join(home, "keygen-args")), "-l") {
		t.Fatal("not validated")
	}
	text := managementRead(t, auth)
	if !strings.Contains(text, "ssh-ed25519 YWJj\n") || strings.Contains(text, "comment not deployed") {
		t.Fatal(text)
	}
	r = a.Key(ctx, []string{"add", "--public-key", pub, "host"})
	if !r.OK() {
		t.Fatalf("%+v", r)
	}
	if managementRead(t, auth) != text {
		t.Fatal("duplicate key changed contents")
	}
	r = a.Key(ctx, []string{"rollback", "host", "--backup", id})
	if !r.OK() {
		t.Fatalf("%+v", r)
	}
	if managementRead(t, auth) != initial {
		t.Fatal("rollback did not restore exact content")
	}
	// Existing restricted keys must deduplicate by material, including quoted
	// options containing spaces; their restrictions must never be removed.
	restricted := "command=\"echo hello world\",no-pty ssh-ed25519 YWJj old comment\n"
	managementWrite(t, auth, restricted)
	r = a.Key(ctx, []string{"add", "host", "--public-key", pub})
	if !r.OK() || managementRead(t, auth) != restricted {
		t.Fatalf("restricted key changed: %+v %s", r, managementRead(t, auth))
	}
}

func TestKeyPartialBackupCannotRollback(t *testing.T) {
	home := managementHome(t)
	managementBinary(t, home, "ssh-keygen", "exit 0\n")
	managementBinary(t, home, "ssh", "for arg do last=$arg; done\nexec /bin/sh -c \"$last\"\n")
	managementBinary(t, home, "cp", `case "$1" in
 .vpsctl-key-lock/authorized_keys)
  printf 'PARTIAL' > "$2"
  echo 'injected ENOSPC during backup' >&2
  exit 1;;
esac
exec /bin/cp "$@"
`)
	pub := filepath.Join(home, "key.pub")
	managementWrite(t, pub, "ssh-ed25519 YWJj\n")
	auth := filepath.Join(home, ".ssh", "authorized_keys")
	initial := "ssh-ed25519 ZGVm existing-working-key\n"
	managementWrite(t, auth, initial)
	a := App{Timeout: 5 * time.Second}
	ctx := context.Background()
	add := a.Key(ctx, []string{"add", "host", "--public-key", pub})
	if add.OK() || !strings.Contains(add.Stderr, "injected ENOSPC") || managementRead(t, auth) != initial {
		t.Fatalf("partial backup failure mishandled: %+v", add)
	}
	id := add.Data.(map[string]string)["backup"]
	if !backupIDPattern.MatchString(id) {
		t.Fatalf("missing planned backup ID: %+v", add)
	}
	// Remove fault injection so only rollback's backup validation can reject it.
	if e := os.Remove(filepath.Join(home, "bin", "cp")); e != nil {
		t.Fatal(e)
	}
	r := a.Key(ctx, []string{"rollback", "host", "--backup", id})
	if r.OK() || !strings.Contains(r.Stderr, "explicit backup does not exist or is unsafe") || managementRead(t, auth) != initial {
		t.Fatalf("rollback accepted incomplete backup or changed original: %+v", r)
	}
	for _, path := range []string{filepath.Join(home, ".ssh", ".vpsctl-key-backups", id), filepath.Join(home, ".ssh", ".vpsctl-key-lock")} {
		if _, e := os.Lstat(path); !os.IsNotExist(e) {
			t.Fatalf("incomplete backup or lock left at %s: %v", path, e)
		}
	}
}

func TestKeyRemoteSymlinksLockAndValidation(t *testing.T) {
	home := managementHome(t)
	managementBinary(t, home, "ssh-keygen", "exit 0\n")
	managementBinary(t, home, "ssh", "for arg do last=$arg; done\nexec /bin/sh -c \"$last\"\n")
	pub := filepath.Join(home, "key.pub")
	managementWrite(t, pub, "ssh-ed25519 YWJj\n")
	target := filepath.Join(home, "valuable")
	managementWrite(t, target, "keep")
	auth := filepath.Join(home, ".ssh", "authorized_keys")
	if e := os.MkdirAll(filepath.Dir(auth), 0700); e != nil {
		t.Fatal(e)
	}
	if e := os.Symlink(target, auth); e != nil {
		t.Fatal(e)
	}
	a := App{Timeout: 5 * time.Second}
	r := a.Key(context.Background(), []string{"add", "host", "--public-key", pub})
	if r.OK() || managementRead(t, target) != "keep" {
		t.Fatalf("symlink accepted: %+v", r)
	}
	if e := os.Remove(auth); e != nil {
		t.Fatal(e)
	}
	lock := filepath.Join(home, ".ssh", ".vpsctl-key-lock")
	if e := os.Mkdir(lock, 0700); e != nil {
		t.Fatal(e)
	}
	r = a.Key(context.Background(), []string{"add", "host", "--public-key", pub})
	if r.OK() {
		t.Fatal("lock ignored")
	}
	if e := os.Remove(lock); e != nil {
		t.Fatal(e)
	}
	r = a.Key(context.Background(), []string{"rollback", "host", "--backup", "../../valuable"})
	if r.OK() {
		t.Fatal("unsafe backup accepted")
	}
	managementBinary(t, home, "ssh-keygen", "exit 1\n")
	r = a.Key(context.Background(), []string{"add", "host", "--public-key", pub})
	if r.OK() {
		t.Fatal("invalid key accepted")
	}
	if _, e := os.Stat(auth); !os.IsNotExist(e) {
		t.Fatal("key installed despite keygen failure")
	}
}

func TestKeyVerifyPinsIdentityAndFreshConnection(t *testing.T) {
	home := managementHome(t)
	config := filepath.Join(home, ".ssh", "config")
	managementWrite(t, config, "Host host\n HostName 192.0.2.1\n IdentityFile wrong\n")
	identity := filepath.Join(home, "selected")
	managementWrite(t, identity, "fake private key")
	managementBinary(t, home, "ssh", `isg=no; cfg=''; previous=''
for arg do
 if [ "$arg" = '-G' ]; then isg=yes; fi
 if [ "$previous" = '-F' ]; then cfg=$arg; fi
 previous=$arg
done
if [ "$isg" = yes ]; then
 printf 'host host\nhostname 192.0.2.1\nuser alice\nidentityfile wrong\nidentityfile another\ncertificatefile wrong-cert\ncontrolpath /wrong/socket\n'
else
 printf '%s\n' "$@" > "$HOME/verify-args"
 /bin/cat "$cfg" > "$HOME/verify-config"
fi
`)
	r := (App{Config: config}).Key(context.Background(), []string{"verify", "host", "--identity", identity})
	if !r.OK() {
		t.Fatalf("%+v", r)
	}
	args := managementRead(t, filepath.Join(home, "verify-args"))
	for _, want := range []string{"IdentitiesOnly=yes", "ControlPath=none", "ControlMaster=no", "ControlPersist=no", "BatchMode=yes", "PasswordAuthentication=no", "StrictHostKeyChecking=yes"} {
		if !strings.Contains(args, want) {
			t.Fatalf("missing %s: %s", want, args)
		}
	}
	text := managementRead(t, filepath.Join(home, "verify-config"))
	if strings.Contains(text, "identityfile wrong") || strings.Contains(text, "wrong-cert") || !strings.Contains(text, "IdentityFile \""+identity+"\"") || !strings.Contains(text, "CertificateFile none") {
		t.Fatal(text)
	}
}

func TestTunnelAbsentDefaultConfigWithSystemSSH(t *testing.T) {
	ssh, e := exec.LookPath("ssh")
	if e != nil {
		t.Skip("OpenSSH unavailable")
	}
	home := managementHome(t)
	// Check production arguments before injecting -F /dev/null solely for the
	// offline parser. OpenSSH uses getpwuid, not HOME, for its default config;
	// never load the developer's real config or its Match exec directives.
	// Control requests are stubbed; no master/server/socket is contacted.
	managementBinary(t, home, "ssh", `for arg do
 if [ "$arg" = '-O' ]; then exit 0; fi
done
printf '%s\n' "$@" > "$HOME/tunnel-master-args"
exec `+quote(ssh)+` -G -F /dev/null "$@"
`)
	r := (App{Timeout: 5 * time.Second}).Tunnel(context.Background(), []string{"start", "user@192.0.2.1", "--local-port", "8080", "--remote-host", "127.0.0.1", "--remote-port", "80"})
	if !r.OK() {
		t.Fatalf("default OpenSSH configuration rejected: %+v", r)
	}
	if strings.Contains(managementRead(t, filepath.Join(home, "tunnel-master-args")), "-F\n") {
		t.Fatal("default configuration overridden with -F")
	}
	record := r.Data.(tunnelRecord)
	stored, e := readTunnel(filepath.Join(home, ".ssh", "vpsctl-tunnels", record.ID+".json"))
	if e != nil || record.Config != "" || stored.Config != "" {
		t.Fatalf("default configuration was pinned: %+v, %+v, %v", record, stored, e)
	}
	if _, e := os.Stat(filepath.Join(home, ".ssh", "config")); !os.IsNotExist(e) {
		t.Fatalf("expected absent default config: %v", e)
	}
}

func TestTunnelKnownSocketsAndLoopbackOnly(t *testing.T) {
	home := managementHome(t)
	config := filepath.Join(home, ".ssh", "config")
	managementWrite(t, config, "Host host\n LocalForward 0.0.0.0:9999 elsewhere:9999\n")
	managementBinary(t, home, "ssh", `printf '%s\n' BEGIN "$@" END >> "$HOME/tunnel-args"
exit 0
`)
	cwd, e := os.Getwd()
	if e != nil {
		t.Fatal(e)
	}
	relative, e := filepath.Rel(cwd, config)
	if e != nil {
		t.Fatal(e)
	}
	a := App{Config: relative}
	ctx := context.Background()
	r := a.Tunnel(ctx, []string{"start", "host", "--local-port", "8080", "--remote-host", "db.internal", "--remote-port", "5432"})
	if !r.OK() {
		t.Fatalf("%+v", r)
	}
	id := r.Data.(tunnelRecord).ID
	stored, e := readTunnel(filepath.Join(home, ".ssh", "vpsctl-tunnels", id+".json"))
	if e != nil || stored.Config != config || r.Data.(tunnelRecord).Config != config {
		t.Fatalf("explicit config not stored as absolute path: %+v, %+v, %v", r, stored, e)
	}
	log := managementRead(t, filepath.Join(home, "tunnel-args"))
	if !strings.Contains(log, "-F\n"+config+"\n") {
		t.Fatalf("absolute config not passed to SSH: %s", log)
	}
	for _, want := range []string{"ClearAllForwardings=yes", "ExitOnForwardFailure=yes", "127.0.0.1:8080:db.internal:5432", "PasswordAuthentication=no", "StrictHostKeyChecking=yes", "BatchMode=yes", "forward", "/dev/null"} {
		if !strings.Contains(log, want) {
			t.Fatalf("missing %q: %s", want, log)
		}
	}
	if strings.Contains(log, "ClearAllForwardings=no") {
		t.Fatal("config forwarding could leak")
	}
	r = a.Tunnel(ctx, []string{"status", "other", "--id", id})
	if r.OK() {
		t.Fatal("wrong host accepted")
	}
	r = a.Tunnel(ctx, []string{"status", "host", "--id", "../../other"})
	if r.OK() {
		t.Fatal("unknown socket accepted")
	}
	r = a.Tunnel(ctx, []string{"status", "host", "--id", id})
	if !r.OK() {
		t.Fatalf("%+v", r)
	}
	r = a.Tunnel(ctx, []string{"list"})
	if !r.OK() || r.Data.(map[string]any)["live_checked"] != false {
		t.Fatalf("%+v", r)
	}
	r = a.Tunnel(ctx, []string{"stop", "host", "--id", id})
	if !r.OK() {
		t.Fatalf("%+v", r)
	}
	if _, e := os.Stat(filepath.Join(home, ".ssh", "vpsctl-tunnels", id+".json")); !os.IsNotExist(e) {
		t.Fatal("state not removed")
	}
}

func TestManagementLocalOutputAndTimeout(t *testing.T) {
	home := managementHome(t)
	managementBinary(t, home, "ssh", "printf 'abcdefghijklmnop'\n")
	r := (App{MaxOutput: 4}).localCommand(context.Background(), "ssh", nil, nil)
	if !r.OK() || !r.Truncated || r.Stdout != "abcd" {
		t.Fatalf("%+v", r)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r = (App{}).localCommand(ctx, "ssh", nil, nil)
	if r.OK() || r.Error.Code != "OUTCOME_UNKNOWN" {
		t.Fatalf("%+v", r)
	}
}

func TestResolvedConfigRoundTripsWithSystemSSH(t *testing.T) {
	ssh, e := exec.LookPath("ssh")
	if e != nil {
		t.Skip("OpenSSH unavailable")
	}
	home := managementHome(t)
	config := filepath.Join(home, ".ssh", "config")
	managementWrite(t, config, "Host host\n HostName 192.0.2.1\n User nobody\n")
	// -G never connects; exercise the exact resolved-config reconstruction used
	// by verify against real OpenSSH syntax without a server or user config.
	r := (App{Config: config}).resolvedHost(context.Background(), "host")
	if !r.OK() {
		t.Fatalf("%+v", r)
	}
	filtered := "Host *\n IdentityFile /no/such/key\n CertificateFile none\n"
	for _, line := range strings.Split(r.Stdout, "\n") {
		w := strings.Fields(line)
		if len(w) > 0 && w[0] != "identityfile" && w[0] != "certificatefile" {
			filtered += line + "\n"
		}
	}
	resolved := filepath.Join(home, "resolved")
	managementWrite(t, resolved, filtered)
	cmd := exec.Command(ssh, "-G", "-F", resolved, "host")
	if out, e := cmd.CombinedOutput(); e != nil {
		t.Fatalf("resolved config rejected: %v\n%s", e, out)
	}
}
