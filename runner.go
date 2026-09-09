package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type App struct {
	Config    string
	Timeout   time.Duration
	MaxOutput int
	Stdin     io.Reader
}

var hostPattern = regexp.MustCompile(`^[a-zA-Z0-9_][a-zA-Z0-9_.:@%+\-\[\]]*$`)

func validHost(host string) bool { return hostPattern.MatchString(host) }
func quote(s string) string      { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
func token() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

// Keep a bounded prefix for Agent output, and a small tail for the completion marker.
type capture struct {
	prefix bytes.Buffer
	tail   []byte
	total  int
	limit  int
}

func (b *capture) Write(p []byte) (int, error) {
	n := len(p)
	b.total += n
	if room := b.limit - b.prefix.Len(); room > 0 {
		if room > n {
			room = n
		}
		b.prefix.Write(p[:room])
	}
	const tailLimit = 256
	if n >= tailLimit {
		b.tail = append(b.tail[:0], p[n-tailLimit:]...)
	} else {
		b.tail = append(b.tail, p...)
		if len(b.tail) > tailLimit {
			b.tail = append([]byte(nil), b.tail[len(b.tail)-tailLimit:]...)
		}
	}
	return n, nil
}

func (a App) sshArgs(host, command string) []string {
	args := []string{"-T", "-o", "BatchMode=yes", "-o", "PasswordAuthentication=no", "-o", "KbdInteractiveAuthentication=no", "-o", "PreferredAuthentications=publickey", "-o", "StrictHostKeyChecking=yes", "-o", "ConnectTimeout=15", "-o", "ConnectionAttempts=1", "-o", "ServerAliveInterval=15", "-o", "ServerAliveCountMax=3", "-o", "ForwardAgent=no", "-o", "PermitLocalCommand=no", "-o", "ClearAllForwardings=yes", "-o", "RequestTTY=no"}
	if a.Config != "" {
		args = append(args, "-F", a.Config)
	}
	return append(args, host, command)
}

func (a App) command(ctx context.Context, name string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = append(os.Environ(), "LC_ALL=C", "SSH_ASKPASS_REQUIRE=never")
	// Detach from any controlling terminal: even a ProxyJump subprocess must
	// fail rather than prompt. Cancel the local process group, not remote work.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.Cancel = func() error {
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
	cmd.WaitDelay = 2 * time.Second
	return cmd
}

func (a App) SSH(ctx context.Context, host, remote string, input io.Reader) Result {
	if !validHost(host) {
		return failure("INVALID_ARGUMENT", "invalid SSH host alias")
	}
	return a.runSSH(ctx, host, remote, input, "")
}

// Script emits an unguessable marker from the parent shell so remote exit 255 is
// distinguishable from a lost SSH connection. It does not retry remote commands.
func (a App) Script(ctx context.Context, host, script string) Result {
	if strings.IndexByte(script, 0) >= 0 {
		return failure("INVALID_ARGUMENT", "script contains a NUL byte")
	}
	marker := "VPSCTL_" + token() + ":"
	// Receive the whole script before execution: child commands must not consume
	// unread script text from SSH stdin. Keep large scripts out of argv as well.
	loader := `mask=$(umask)
umask 077
tmp=$(mktemp "${TMPDIR:-/tmp}/vpsctl-script.XXXXXXXX") || exit $?
trap 'rm -f -- "$tmp"' 0
trap 'exit 130' 1 2 15
cat > "$tmp" || exit $?
umask "$mask"
sh "$tmp" < /dev/null`
	remote := "sh -c " + quote(loader) + "; rc=$?; printf '\\n" + marker + "%s\\n' \"$rc\" >&2; exit 0"
	if !validHost(host) {
		return failure("INVALID_ARGUMENT", "invalid SSH host alias")
	}
	return a.runSSH(ctx, host, remote, strings.NewReader(script), marker)
}

func (a App) runSSH(ctx context.Context, host, remote string, input io.Reader, marker string) (r Result) {
	if a.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, a.Timeout)
		defer cancel()
	}
	limit := a.MaxOutput
	if limit <= 0 {
		limit = 1024 * 1024
	}
	// OpenSSH diagnostics must not share the remote stderr trust boundary.
	// -E routes client logs here, while the command's stderr remains on its pipe.
	log, e := os.CreateTemp("", "vpsctl-ssh-log-*")
	if e != nil {
		return failure("LOCAL_IO", e.Error())
	}
	defer os.Remove(log.Name())
	defer log.Close()
	out, errout := &capture{limit: limit}, &capture{limit: limit}
	args := append([]string{"-E", log.Name(), "-o", "LogLevel=ERROR"}, a.sshArgs(host, remote)...)
	cmd := a.command(ctx, "ssh", args...)
	cmd.Stdin = input
	cmd.Stdout = out
	cmd.Stderr = errout
	err := cmd.Run()
	diagnostic := &capture{limit: limit}
	// Read bounded memory even if client diagnostics exceed the display budget.
	_, logErr := io.Copy(diagnostic, log)
	defer func() {
		room := limit - len(r.Stderr)
		text := diagnostic.prefix.String()
		if len(text) > room {
			text = text[:room]
		}
		r.Stderr += text
		r.Truncated = r.Truncated || diagnostic.total > room
	}()
	r = completed(0, out.prefix.String(), errout.prefix.String())
	r.Truncated = out.total > limit || errout.total > limit
	if marker != "" && err == nil {
		tail := string(errout.tail)
		idx := strings.LastIndex(tail, "\n"+marker)
		if idx >= 0 {
			value := strings.TrimSuffix(tail[idx+len(marker)+1:], "\n")
			code, e := strconv.Atoi(value)
			if e == nil && code >= 0 && code <= 255 {
				r.ExitCode = &code
				contentLen := errout.total - len(tail[idx:])
				if contentLen < len(r.Stderr) {
					r.Stderr = r.Stderr[:contentLen]
				}
				r.Truncated = out.total > limit || contentLen > limit
				return r
			}
		}
		r.Status = "unknown"
		r.ExitCode = nil
		r.Error = &Problem{"OUTCOME_UNKNOWN", "SSH returned without a remote completion marker; do not blindly retry"}
		return r
	}
	if err == nil {
		return r
	}
	r.ExitCode = nil
	if ctx.Err() != nil {
		r.Status = "unknown"
		r.Error = &Problem{"OUTCOME_UNKNOWN", "local wait canceled or timed out; remote work may still be running"}
		return r
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() != 255 && marker == "" {
		code := exit.ExitCode()
		r.ExitCode = &code
		return r
	}
	r.Status = "unknown"
	r.Error = &Problem{"OUTCOME_UNKNOWN", "SSH connection failed; remote execution may have started"}
	text := ""
	if logErr == nil {
		text = diagnostic.prefix.String() + string(diagnostic.tail)
	}
	switch {
	case errors.Is(err, exec.ErrNotFound):
		r.Status = "failed"
		r.Error = &Problem{"DEPENDENCY_MISSING", "OpenSSH ssh executable was not found"}
	case strings.Contains(text, "REMOTE HOST IDENTIFICATION HAS CHANGED"), strings.Contains(text, "Host key verification failed"):
		r.Status = "failed"
		r.Error = &Problem{"HOST_KEY_REJECTED", "verify the server fingerprint and provision known_hosts before retrying"}
	case strings.Contains(text, "Permission denied (publickey"):
		r.Status = "failed"
		r.Error = &Problem{"AUTH_FAILED", "configure an SSH key or unlock it in ssh-agent; password authentication is disabled"}
	case strings.Contains(text, "Could not resolve hostname"), strings.Contains(text, "Connection refused"), strings.Contains(text, "No route to host"):
		r.Status = "failed"
		r.Error = &Problem{"CONNECT_FAILED", "SSH could not establish the connection"}
	}
	if !errors.As(err, &exit) && r.Error.Code == "OUTCOME_UNKNOWN" {
		r.Error.Message = fmt.Sprintf("SSH process failed (%v); remote outcome is unknown", err)
	}
	return r
}
