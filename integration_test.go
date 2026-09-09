package main

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Opt in on an isolated Linux machine/container: sudo -E env PATH="$PATH"
// VPSCTL_INTEGRATION=1 go test -run TestSSHIntegration -v .
// All keys, known_hosts, sshd configuration and remote HOME are temporary.
func TestSSHIntegration(t *testing.T) {
	if os.Getenv("VPSCTL_INTEGRATION") != "1" {
		t.Skip("set VPSCTL_INTEGRATION=1 on isolated Linux with root and sshd")
	}
	if runtime.GOOS != "linux" || os.Geteuid() != 0 {
		t.Fatal("integration test requires isolated Linux with root to launch sshd")
	}
	sshd, err := exec.LookPath("sshd")
	if err != nil {
		sshd = "/usr/sbin/sshd"
		if _, err = os.Stat(sshd); err != nil {
			t.Fatal(err)
		}
	}
	root := t.TempDir()
	home := filepath.Join(root, "remote")
	if err = os.Mkdir(home, 0700); err != nil {
		t.Fatal(err)
	}
	current, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	key := filepath.Join(root, "client")
	hostkey := filepath.Join(root, "host")
	for _, path := range []string{key, hostkey} {
		cmd := exec.Command("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-f", path)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("keygen: %s %v", out, err)
		}
	}
	authdir := filepath.Join(home, ".ssh")
	if err = os.Mkdir(authdir, 0700); err != nil {
		t.Fatal(err)
	}
	pub, _ := os.ReadFile(key + ".pub")
	if err = os.WriteFile(filepath.Join(authdir, "authorized_keys"), pub, 0600); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close()
	wrapper := filepath.Join(root, "remote-shell")
	if err = os.WriteFile(wrapper, []byte("#!/bin/sh\nexport HOME="+quote(home)+"\ncd \"$HOME\" || exit 1\nexec /bin/sh -c \"$SSH_ORIGINAL_COMMAND\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	config := fmt.Sprintf("Port %d\nListenAddress 127.0.0.1\nHostKey %s\nPidFile %s\nAuthorizedKeysFile %s\nStrictModes yes\nPasswordAuthentication no\nKbdInteractiveAuthentication no\nPubkeyAuthentication yes\nPermitRootLogin prohibit-password\nUsePAM no\nAllowUsers %s\nAllowTcpForwarding yes\nForceCommand %s\n", port, hostkey, filepath.Join(root, "sshd.pid"), filepath.Join(authdir, "authorized_keys"), current.Username, wrapper)
	serverConfig := filepath.Join(root, "sshd_config")
	if err = os.WriteFile(serverConfig, []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	// OpenSSH's privilege separation directory is a system prerequisite, not a
	// vpsctl state location; CI provisions it before opting into this test.
	var serverLog bytes.Buffer
	server := exec.Command(sshd, "-D", "-e", "-f", serverConfig)
	server.Stdout = &serverLog
	server.Stderr = &serverLog
	if err = server.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = server.Process.Kill()
		_ = server.Wait()
		if t.Failed() {
			t.Log(serverLog.String())
		}
	})
	address := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	deadline := time.Now().Add(5 * time.Second)
	for {
		conn, e := net.DialTimeout("tcp", address, 100*time.Millisecond)
		if e == nil {
			conn.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("isolated sshd did not listen")
		}
		time.Sleep(20 * time.Millisecond)
	}
	hostpub, _ := os.ReadFile(hostkey + ".pub")
	known := filepath.Join(root, "known_hosts")
	if err = os.WriteFile(known, []byte("[127.0.0.1]:"+strconv.Itoa(port)+" "+string(hostpub)), 0600); err != nil {
		t.Fatal(err)
	}
	clientConfig := filepath.Join(root, "ssh_config")
	client := fmt.Sprintf("Host fixture\n HostName 127.0.0.1\n Port %d\n User %s\n IdentityFile %s\n IdentitiesOnly yes\n UserKnownHostsFile %s\n", port, current.Username, key, known)
	if err = os.WriteFile(clientConfig, []byte(client), 0600); err != nil {
		t.Fatal(err)
	}
	a := App{Config: clientConfig, Timeout: 10 * time.Second, MaxOutput: 1024 * 1024}
	ctx := context.Background()
	require := func(r Result) {
		t.Helper()
		if !r.OK() {
			t.Fatalf("result: %+v; error: %+v", r, r.Error)
		}
	}
	r := a.Script(ctx, "fixture", "printf 'hello'; printf 'warn' >&2; exit 255")
	if r.Status != "completed" || r.ExitCode == nil || *r.ExitCode != 255 || r.Stdout != "hello" || r.Stderr != "warn" {
		t.Fatalf("remote exit framing: %+v", r)
	}
	r = a.Script(ctx, "fixture", "cat >/dev/null\nread value || :\nprintf 'input-isolated\\n'")
	if !r.OK() || r.Stdout != "input-isolated\n" {
		t.Fatalf("script stdin isolation: %+v", r)
	}
	source := filepath.Join(root, "source")
	dest := filepath.Join(home, "payload")
	download := filepath.Join(root, "download")
	payload := bytes.Repeat([]byte("payload\x00\xff\n"), 32768)
	if err = os.WriteFile(source, payload, 0600); err != nil {
		t.Fatal(err)
	}
	if r = a.upload(ctx, "fixture", source, home, false); r.OK() {
		t.Fatal("upload accepted directory target")
	}
	require(a.upload(ctx, "fixture", source, dest, false))
	small := a
	small.MaxOutput = 1
	require(small.download(ctx, "fixture", dest, download, false))
	got, _ := os.ReadFile(download)
	if !bytes.Equal(got, payload) {
		t.Fatal("transfer bytes differ")
	}
	require(small.Transfer(ctx, []string{"fixture", dest, "fixture", filepath.Join(home, "cross")}))
	job := a.StartJob(ctx, "fixture", "printf 'start\\n'; sleep 1; printf 'finish\\n'; exit 23")
	require(job)
	id := job.Data.(map[string]any)["job_id"].(string)
	deadline = time.Now().Add(10 * time.Second)
	for {
		r = a.Job(ctx, []string{"status", "fixture", id})
		if r.Status == "completed" {
			break
		}
		if !r.OK() || time.Now().After(deadline) {
			t.Fatalf("job status: %+v", r)
		}
		time.Sleep(50 * time.Millisecond)
	}
	if r.ExitCode == nil || *r.ExitCode != 23 {
		t.Fatalf("wrong job exit: %+v", r)
	}
	log := a.Job(ctx, []string{"output", "fixture", id, "--limit", "6"})
	require(log)
	if log.Stdout != "start\n" || !log.Truncated {
		t.Fatalf("first chunk: %+v", log)
	}
	log = a.Job(ctx, []string{"output", "fixture", id, "--cursor", "6"})
	require(log)
	if log.Stdout != "finish\n" {
		t.Fatalf("second chunk: %+v", log)
	}
	// Verify the known-host trust boundary without accepting a scanned key.
	if err = os.WriteFile(known, []byte("[127.0.0.1]:"+strconv.Itoa(port)+" "+strings.TrimSpace(string(pub))+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	r = a.Script(ctx, "fixture", "true")
	if r.Error == nil || r.Error.Code != "HOST_KEY_REJECTED" {
		t.Fatalf("changed host key accepted: %+v", r)
	}
}
