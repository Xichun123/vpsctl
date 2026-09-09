package main

import (
	"context"
	"encoding/base64"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func jobsFakeSSH(t *testing.T, body string) {
	t.Helper()
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "ssh"), []byte("#!/bin/sh\n"+body), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func jobsLocal(t *testing.T) (App, string) {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("job shell integration requires Linux /proc and GNU coreutils")
	}
	if _, err := exec.LookPath("setsid"); err != nil {
		t.Skip("job launch integration requires util-linux setsid")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	jobsFakeSSH(t, "for last do :; done\nexec /bin/sh -c \"$last\"\n")
	return App{Timeout: 5 * time.Second, MaxOutput: 1024}, home
}

func jobsID(t *testing.T, r Result) string {
	t.Helper()
	data, ok := r.Data.(map[string]any)
	if !ok {
		t.Fatalf("missing job data: %#v", r)
	}
	id, ok := data["job_id"].(string)
	if !ok || !validJobID(id) || data["host"] != "local" {
		t.Fatalf("invalid reconciliation data: %#v", data)
	}
	return id
}

func TestJobsDetachedLifecycle(t *testing.T) {
	a, home := jobsLocal(t)
	// stdin belongs to CLI script ingestion, never to the detached command.
	a.Stdin = strings.NewReader("must not be delivered to job")
	gate := filepath.Join(home, "release")
	script := "printf 'abc'; printf 'def' >&2; printf '\\377\\000'; if read value; then exit 99; fi; while [ ! -f " + quote(gate) + " ]; do sleep 0.05; done; exit 23\n"
	started := a.StartJob(context.Background(), "local", script)
	if !started.OK() || started.Status != "running" {
		t.Fatalf("launch: %#v", started)
	}
	id := jobsID(t, started)
	t.Cleanup(func() { _ = os.WriteFile(gate, nil, 0600) })
	status := a.Job(context.Background(), []string{"status", "local", id})
	if status.Status != "running" || status.ExitCode != nil {
		t.Fatalf("detached worker not running: %#v", status)
	}
	first := a.Job(context.Background(), []string{"output", "local", id, "--limit", "3"})
	if !first.OK() || first.Stdout != "abc" || !first.Truncated || first.Data.(map[string]any)["next_cursor"] != int64(3) {
		t.Fatalf("first chunk: %#v", first)
	}
	second := a.Job(context.Background(), []string{"output", "--cursor=3", "--limit=8", "local", id})
	if !second.OK() || second.Truncated {
		t.Fatalf("second chunk: %#v", second)
	}
	decoded, err := base64.StdEncoding.DecodeString(second.Data.(map[string]any)["output_base64"].(string))
	if err != nil || string(decoded) != "def\xff\x00" {
		t.Fatalf("binary chunk lost: %q %v", decoded, err)
	}
	empty := a.Job(context.Background(), []string{"output", "local", id, "--cursor", "8"})
	if !empty.OK() || empty.Stdout != "" || empty.Truncated {
		t.Fatalf("EOF chunk: %#v", empty)
	}
	bad := a.Job(context.Background(), []string{"output", "local", id, "--cursor", "9"})
	if bad.Error == nil || bad.Error.Code != "INVALID_CURSOR" {
		t.Fatalf("past EOF: %#v", bad)
	}
	if err := os.WriteFile(gate, nil, 0600); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		status = a.Job(context.Background(), []string{"status", "local", id})
		if status.Status == "completed" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("completion not recorded: %#v", status)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if status.ExitCode == nil || *status.ExitCode != 23 {
		t.Fatalf("wrong completion code: %#v", status)
	}
	dir := filepath.Join(home, ".local", "state", "vpsctl", "jobs", id)
	info, err := os.Stat(dir)
	if err != nil || info.Mode().Perm() != 0700 {
		t.Fatalf("job permissions: %v %v", info, err)
	}
}

func TestJobsUnknownAndStorage(t *testing.T) {
	a, home := jobsLocal(t)
	id := strings.Repeat("a", 32)
	missing := a.Job(context.Background(), []string{"status", "local", id})
	if missing.Error == nil || missing.Error.Code != "JOB_NOT_FOUND" {
		t.Fatalf("missing: %#v", missing)
	}
	dir := filepath.Join(home, ".local", "state", "vpsctl", "jobs", id)
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	for _, record := range []string{"", "1 stale-boot-id 0\n"} {
		if record != "" {
			if err := os.WriteFile(filepath.Join(dir, "ready"), []byte(record), 0600); err != nil {
				t.Fatal(err)
			}
		}
		r := a.Job(context.Background(), []string{"status", "local", id})
		if r.Status != "unknown" || r.ExitCode != nil || r.Error == nil {
			t.Fatalf("interrupted/rebooted worker: %#v", r)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "exit"), []byte("0\n"), 0600); err != nil {
		t.Fatal(err)
	}
	r := a.Job(context.Background(), []string{"status", "local", id})
	if !r.OK() || r.Status != "completed" {
		t.Fatalf("completed record: %#v", r)
	}
	if err := os.Symlink(filepath.Join(home, "outside"), filepath.Join(dir, "log")); err != nil {
		t.Fatal(err)
	}
	r = a.Job(context.Background(), []string{"output", "local", id})
	if r.Error == nil || r.Error.Code != "UNSAFE_STORAGE" {
		t.Fatalf("symlink log: %#v", r)
	}
	other := strings.Repeat("b", 32)
	if err := os.Symlink(dir, filepath.Join(filepath.Dir(dir), other)); err != nil {
		t.Fatal(err)
	}
	r = a.Job(context.Background(), []string{"status", "local", other})
	if r.Error == nil || r.Error.Code != "UNSAFE_STORAGE" {
		t.Fatalf("symlink job directory: %#v", r)
	}
}

func TestJobsRejectSymlinkRoot(t *testing.T) {
	a, home := jobsLocal(t)
	if err := os.Symlink(t.TempDir(), filepath.Join(home, ".local")); err != nil {
		t.Fatal(err)
	}
	r := a.StartJob(context.Background(), "local", "exit 0")
	jobsID(t, r)
	if r.Error == nil || r.Error.Code != "UNSAFE_STORAGE" {
		t.Fatalf("symlink storage accepted: %#v", r)
	}
}

func TestJobsShellSyntax(t *testing.T) {
	jobsFakeSSH(t, "exec sh -n\n")
	a := App{}
	results := []Result{
		a.StartJob(context.Background(), "local", "printf '%s' \"quotes ' $()\"\n"),
		a.Job(context.Background(), []string{"status", "local", strings.Repeat("a", 32)}),
		a.Job(context.Background(), []string{"output", "local", strings.Repeat("a", 32)}),
	}
	for _, r := range results {
		if r.Stderr != "" || r.Error == nil || r.Error.Code != "OUTCOME_UNKNOWN" {
			t.Fatalf("generated shell did not parse: %#v", r)
		}
	}
	cmd := exec.Command("sh", "-n")
	cmd.Stdin = strings.NewReader(jobWorker)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("worker shell syntax: %s: %v", out, err)
	}
}

func TestJobsResponseParsing(t *testing.T) {
	// Run Script's real framing wrapper with a substituted remote body.
	jobsFakeSSH(t, "for last do :; done\nexec /bin/sh -c \"$last\" <<'EOF'\nprintf '%s' \"$JOBS_TEST_RESPONSE\"\nEOF\n")
	a := App{MaxOutput: 3}
	id := strings.Repeat("a", 32)
	for _, tc := range []struct {
		response, action, state, stdout string
		code                            int
		truncated                       bool
	}{
		{"completed 23\n", "status", "completed", "", 23, false},
		{"running\n", "status", "running", "", -1, false},
		{"unknown\n", "status", "unknown", "", -1, false},
		{"8 3\nYWJj\n", "output", "completed", "abc", 0, true},
		{"3 3\nYWJj\n", "output", "completed", "abc", 0, false},
		{"0 0\n", "output", "completed", "", 0, false},
	} {
		t.Setenv("JOBS_TEST_RESPONSE", tc.response)
		r := a.Job(context.Background(), []string{tc.action, "local", id})
		if r.Status != tc.state || r.Stdout != tc.stdout || r.Truncated != tc.truncated {
			t.Fatalf("response %q: %#v", tc.response, r)
		}
		if tc.code < 0 {
			if r.ExitCode != nil {
				t.Fatalf("unexpected code: %#v", r)
			}
		} else if r.ExitCode == nil || *r.ExitCode != tc.code {
			t.Fatalf("wrong code: %#v", r)
		}
	}
	for _, response := range []string{"garbage", "8 3\nYQ==\n", "2 3\nYWJj", "8 4\nYWJjZA==", "-1 0\n"} {
		t.Setenv("JOBS_TEST_RESPONSE", response)
		r := a.Job(context.Background(), []string{"output", "local", id})
		if r.Error == nil || r.Error.Code != "JOB_PROTOCOL_ERROR" {
			t.Fatalf("accepted bad response %q: %#v", response, r)
		}
	}
}

func TestJobsValidationAndUncertainLaunch(t *testing.T) {
	jobsFakeSSH(t, "exit 255\n")
	a := App{}
	for _, args := range [][]string{
		{}, {"cancel"}, {"status", "local", "../escape"},
		{"output", "local", strings.Repeat("a", 32), "--cursor", "1;id"},
		{"output", "local", strings.Repeat("a", 32), "--cursor", "-1"},
		{"output", "local", strings.Repeat("a", 32), "--limit", "1048577"},
		{"output", "local", strings.Repeat("a", 32), "--limit", "0"},
		{"output", "local", strings.Repeat("a", 32), "--limit", "1", "--limit", "2"},
		{"status", "local", strings.Repeat("a", 32), "--cursor", "0"},
	} {
		r := a.Job(context.Background(), args)
		if r.Error == nil || r.Error.Code != "INVALID_ARGUMENT" {
			t.Fatalf("accepted %q: %#v", args, r)
		}
	}
	r := a.StartJob(context.Background(), "local", "exit 0")
	jobsID(t, r)
	if r.Status != "unknown" || r.ExitCode != nil {
		t.Fatalf("lost acknowledgement: %#v", r)
	}
	r = a.StartJob(context.Background(), "local", "bad\x00script")
	jobsID(t, r)
	if r.Error == nil || r.Error.Code != "INVALID_ARGUMENT" {
		t.Fatalf("NUL script accepted: %#v", r)
	}
}
