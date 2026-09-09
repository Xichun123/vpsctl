package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// localCommand uses the same bounded capture and deadline as SSH. It is used
// only for OpenSSH/keygen, never a shell containing local user arguments.
func (a App) localCommand(ctx context.Context, name string, args []string, input io.Reader) Result {
	if a.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, a.Timeout)
		defer cancel()
	}
	limit := a.MaxOutput
	if limit <= 0 {
		limit = 1024 * 1024
	}
	out, errout := &capture{limit: limit}, &capture{limit: limit}
	cmd := a.command(ctx, name, args...)
	cmd.Stdin = input
	cmd.Stdout = out
	cmd.Stderr = errout
	err := cmd.Run()
	r := completed(0, out.prefix.String(), errout.prefix.String())
	r.Truncated = out.total > limit || errout.total > limit
	if err != nil {
		if ctx.Err() != nil {
			r = failure("OUTCOME_UNKNOWN", "local command timed out or was canceled; inspect state before retrying")
			r.Status = "unknown"
		} else if e, ok := err.(*exec.ExitError); ok {
			r = completed(e.ExitCode(), out.prefix.String(), errout.prefix.String())
			r.Error = &Problem{"COMMAND_FAILED", name + " failed"}
			r.Status = "failed"
		} else {
			r = failure("DEPENDENCY_ERROR", err.Error())
		}
		r.Stdout = out.prefix.String()
		r.Stderr = errout.prefix.String()
		r.Truncated = out.total > limit || errout.total > limit
	}
	return r
}

var backupIDPattern = regexp.MustCompile(`^[a-f0-9]{32}$`)

func (a App) Key(ctx context.Context, args []string) Result {
	if len(args) == 0 {
		return failure("INVALID_ARGUMENT", "key requires add, verify, or rollback")
	}
	op := args[0]
	fs := newFlags("key " + op)
	public := fs.String("public-key", "", "")
	identity := fs.String("identity", "", "")
	backup := fs.String("backup", "", "")
	if e := parseFlags(fs, args[1:]); e != nil {
		return failure("INVALID_ARGUMENT", e.Error())
	}
	if fs.NArg() != 1 || !validHost(fs.Arg(0)) {
		return failure("INVALID_ARGUMENT", "key requires one HOST")
	}
	host := fs.Arg(0)
	switch op {
	case "add":
		if *public == "" || *identity != "" || *backup != "" {
			return failure("INVALID_ARGUMENT", "usage: key add HOST --public-key FILE")
		}
		data, e := os.ReadFile(*public)
		if e != nil {
			return failure("KEY_ERROR", e.Error())
		}
		line := strings.TrimSpace(string(data))
		if len(data) > 65536 || strings.ContainsAny(line, "\r\n\x00") {
			return failure("INVALID_ARGUMENT", "public key must contain exactly one key line")
		}
		fields := strings.Fields(line)
		if len(fields) < 2 || !(strings.HasPrefix(fields[0], "ssh-") || strings.HasPrefix(fields[0], "ecdsa-") || strings.HasPrefix(fields[0], "sk-")) {
			return failure("INVALID_ARGUMENT", "expected an OpenSSH public key without authorized_keys options")
		}
		if _, e = base64.StdEncoding.DecodeString(fields[1]); e != nil {
			return failure("INVALID_ARGUMENT", "invalid public key encoding")
		}
		// Validate a snapshot, not a user path that could change after validation.
		f, e := os.CreateTemp("", "vpsctl-public-key-")
		if e != nil {
			return failure("KEY_ERROR", e.Error())
		}
		defer os.Remove(f.Name())
		if _, e = f.WriteString(fields[0] + " " + fields[1] + "\n"); e != nil {
			f.Close()
			return failure("KEY_ERROR", e.Error())
		}
		if e = f.Close(); e != nil {
			return failure("KEY_ERROR", e.Error())
		}
		r := a.localCommand(ctx, "ssh-keygen", []string{"-l", "-f", f.Name()}, nil)
		if !r.OK() {
			return r
		}
		id := token()
		r = a.Script(ctx, host, keyScript(fields[0]+" "+fields[1], fields[1], id, false))
		// Return the planned backup ID even when the connection drops mid-write.
		r.Data = map[string]string{"host": host, "backup": id, "action": "add"}
		return r
	case "rollback":
		if !backupIDPattern.MatchString(*backup) || *identity != "" || *public != "" {
			return failure("INVALID_ARGUMENT", "usage: key rollback HOST --backup ID (32 lowercase hex characters)")
		}
		r := a.Script(ctx, host, keyScript("", "", *backup, true))
		if r.OK() {
			r.Data = map[string]string{"host": host, "backup": *backup, "action": "rollback"}
		}
		return r
	case "verify":
		if *identity == "" || *public != "" || *backup != "" {
			return failure("INVALID_ARGUMENT", "usage: key verify HOST --identity PRIVATE_KEY")
		}
		path, e := filepath.Abs(*identity)
		if e != nil || !simpleValue(path) {
			return failure("INVALID_ARGUMENT", "invalid identity path")
		}
		info, e := os.Stat(path)
		if e != nil {
			return failure("KEY_ERROR", e.Error())
		}
		if !info.Mode().IsRegular() {
			return failure("KEY_ERROR", "identity must be a regular file")
		}
		resolved := a.resolvedHost(ctx, host)
		if !resolved.OK() {
			return resolved
		}
		if resolved.Truncated {
			return failure("OUTPUT_LIMIT", "resolved SSH configuration exceeded output limit")
		}
		// IdentityFile is additive in OpenSSH: simply passing -i does NOT isolate a
		// key. Rebuild the resolved config without identities and certificates.
		var b strings.Builder
		b.WriteString("Host *\n    IdentityFile " + configValue(path) + "\n    CertificateFile none\n")
		for _, line := range strings.Split(resolved.Stdout, "\n") {
			w := strings.Fields(line)
			if len(w) == 0 {
				continue
			}
			switch strings.ToLower(w[0]) {
			case "proxyjump":
				if len(w) > 1 && w[1] != "none" {
					return failure("UNSUPPORTED_CONFIG", "isolated key verification cannot safely flatten ProxyJump configuration; normal exec and transfers still support ProxyJump")
				}
			case "identityfile", "certificatefile":
				continue
			}
			b.WriteString(line + "\n")
		}
		f, e := os.CreateTemp("", "vpsctl-verify-config-")
		if e != nil {
			return failure("KEY_ERROR", e.Error())
		}
		defer os.Remove(f.Name())
		if _, e = f.WriteString(b.String()); e != nil {
			f.Close()
			return failure("KEY_ERROR", e.Error())
		}
		if e = f.Close(); e != nil {
			return failure("KEY_ERROR", e.Error())
		}
		isolated := a
		isolated.Config = f.Name()
		sshArgs := isolated.sshArgs(host, "true")
		sshArgs = append([]string{"-o", "IdentitiesOnly=yes", "-o", "ControlMaster=no", "-o", "ControlPath=none", "-o", "ControlPersist=no", "-o", "AddKeysToAgent=no"}, sshArgs...)
		r := a.localCommand(ctx, "ssh", sshArgs, nil)
		if r.OK() {
			r.Data = map[string]string{"host": host, "identity": path, "verified": "fresh public-key authentication"}
		}
		return r
	default:
		return failure("INVALID_ARGUMENT", "unknown key operation")
	}
}

// Lock and temporary files are confined to the user's private .ssh directory.
// Requires POSIX sh, mkdir, chmod, cp, mv, rm, and awk on the remote host.
func keyScript(key, material, id string, rollback bool) string {
	script := `set -eu
umask 077
[ -n "${HOME:-}" ] || { echo 'HOME is unset' >&2; exit 1; }
cd "$HOME"
[ ! -L .ssh ] || { echo 'refusing symlink .ssh' >&2; exit 1; }
if [ ! -e .ssh ]; then mkdir .ssh; fi
[ -d .ssh ] || exit 1
chmod 700 .ssh
cd .ssh
mkdir .vpsctl-key-lock 2>/dev/null || { echo 'key management locked; inspect .ssh/.vpsctl-key-lock before removing' >&2; exit 1; }
tmp=''
trap 'if [ -n "$tmp" ]; then rm -f "$tmp"; fi; rm -f .vpsctl-key-lock/backup; rmdir .vpsctl-key-lock' 0
trap 'exit 130' 1 2 15
[ ! -L authorized_keys ] || { echo 'refusing symlink authorized_keys' >&2; exit 1; }
[ ! -e authorized_keys ] || [ -f authorized_keys ] || exit 1
[ ! -L .vpsctl-key-backups ] || exit 1
if [ ! -e .vpsctl-key-backups ]; then mkdir .vpsctl-key-backups; fi
[ -d .vpsctl-key-backups ] || exit 1
chmod 700 .vpsctl-key-backups
`
	script += "backup=.vpsctl-key-backups/" + quote(id) + "\n"
	script += "tmp=.vpsctl-key-lock/authorized_keys\n"
	if rollback {
		script += `[ ! -L "$backup" ] && [ -f "$backup" ] || { echo 'explicit backup does not exist or is unsafe' >&2; exit 1; }
cp "$backup" "$tmp"
chmod 600 "$tmp"
mv -f "$tmp" authorized_keys
tmp=''
printf 'restored\n'
`
		return script
	}
	script += `[ ! -e "$backup" ] && [ ! -L "$backup" ] || exit 1
if [ -f authorized_keys ]; then cp authorized_keys "$tmp"; else : > "$tmp"; fi
# Publish only a complete backup; a failed copy must never become rollback input.
cp "$tmp" .vpsctl-key-lock/backup
chmod 600 .vpsctl-key-lock/backup
mv .vpsctl-key-lock/backup "$backup"
`
	script += "if awk -v key=" + quote(material) + " -v kind=" + quote(strings.Fields(key)[0]) + ` '
 /^[[:space:]]*#/ {next}
 {
  n=0; word=""; quoted=0; escaped=0
  for (i=1;i<=length($0)+1;i++) {
   c=substr($0,i,1)
   if (escaped) {word=word c; escaped=0; continue}
   if (c=="\\") {escaped=1; word=word c; continue}
   if (c=="\"") {quoted=!quoted; word=word c; continue}
   if ((!quoted && c ~ /[[:space:]]/) || c=="") {
    if (word!="") {part[++n]=word; word=""; if(n==3) break}
   } else word=word c
  }
  if ((part[1]==kind && part[2]==key) || (part[2]==kind && part[3]==key)) found=1
  for (j in part) delete part[j]
 }
 END {exit !found}' "$tmp"; then
 printf 'already present\n'
else
 printf '\n%s\n' ` + quote(key) + " >> \"$tmp\"\n printf 'added\\n'\nfi\nchmod 600 \"$tmp\"\nmv -f \"$tmp\" authorized_keys\ntmp=''\n"
	return script
}

type tunnelRecord struct {
	ID         string `json:"id"`
	Host       string `json:"host"`
	LocalPort  int    `json:"local_port"`
	RemoteHost string `json:"remote_host"`
	RemotePort int    `json:"remote_port"`
	Config     string `json:"config"`
}

func tunnelDir() (string, error) {
	home, e := os.UserHomeDir()
	if e != nil {
		return "", e
	}
	dir := filepath.Join(home, ".ssh", "vpsctl-tunnels")
	if e = safeDir(dir); e != nil {
		return "", e
	}
	info, e := os.Stat(dir)
	if e != nil {
		return "", e
	}
	if info.Mode().Perm()&0077 != 0 {
		return "", fmt.Errorf("tunnel directory must have mode 0700: %s", dir)
	}
	return dir, nil
}
func readTunnel(path string) (tunnelRecord, error) {
	var r tunnelRecord
	if e := regularOrMissing(path); e != nil {
		return r, e
	}
	data, e := os.ReadFile(path)
	if e != nil {
		return r, e
	}
	e = json.Unmarshal(data, &r)
	if e == nil && (!backupIDPattern.MatchString(r.ID) || filepath.Base(path) != r.ID+".json" || !validHost(r.Host)) {
		e = fmt.Errorf("invalid tunnel state")
	}
	return r, e
}

func (a App) Tunnel(ctx context.Context, args []string) Result {
	if len(args) == 0 {
		return failure("INVALID_ARGUMENT", "tunnel requires start, status, stop, or list")
	}
	fs := newFlags("tunnel " + args[0])
	lp := fs.Int("local-port", 0, "")
	rp := fs.Int("remote-port", 0, "")
	rh := fs.String("remote-host", "", "")
	id := fs.String("id", "", "")
	if e := parseFlags(fs, args[1:]); e != nil {
		return failure("INVALID_ARGUMENT", e.Error())
	}
	op := args[0]
	if op != "start" && op != "status" && op != "stop" && op != "list" {
		return failure("INVALID_ARGUMENT", "unknown tunnel operation")
	}
	if op == "list" {
		if fs.NArg() != 0 || fs.NFlag() != 0 {
			return failure("INVALID_ARGUMENT", "usage: tunnel list")
		}
	} else if fs.NArg() != 1 || !validHost(fs.Arg(0)) {
		return failure("INVALID_ARGUMENT", "tunnel requires one HOST")
	}
	if op == "start" {
		if *lp < 1 || *lp > 65535 || *rp < 1 || *rp > 65535 || !validHost(*rh) || strings.ContainsAny(*rh, "@:%[]") || *id != "" {
			return failure("INVALID_ARGUMENT", "start requires --local-port 1..65535 --remote-host DNS_OR_IPV4 --remote-port 1..65535")
		}
	} else if op != "list" {
		if !backupIDPattern.MatchString(*id) || *lp != 0 || *rp != 0 || *rh != "" {
			return failure("INVALID_ARGUMENT", "status/stop require only HOST --id ID")
		}
	}
	dir, e := tunnelDir()
	if e != nil {
		return failure("STATE_ERROR", e.Error())
	}
	if op == "list" {
		entries, e := os.ReadDir(dir)
		if e != nil {
			return failure("STATE_ERROR", e.Error())
		}
		records := []tunnelRecord{}
		for _, entry := range entries {
			if strings.HasSuffix(entry.Name(), ".json") {
				r, e := readTunnel(filepath.Join(dir, entry.Name()))
				if e != nil {
					return failure("STATE_ERROR", e.Error())
				}
				records = append(records, r)
			}
		}
		return success(map[string]any{"tunnels": records, "live_checked": false})
	}
	if op == "start" {
		*id = token()
	}
	unlock, e := localLock(filepath.Join(dir, *id+".lock"))
	if e != nil {
		return failure("LOCKED", e.Error())
	}
	defer unlock()
	state := filepath.Join(dir, *id+".json")
	socket := filepath.Join(dir, *id+".sock")
	if len(socket) > 100 {
		return failure("STATE_ERROR", "SSH control socket path exceeds portable limit of 100 bytes; use a shorter home directory")
	}
	record := tunnelRecord{ID: *id, Host: fs.Arg(0), LocalPort: *lp, RemoteHost: *rh, RemotePort: *rp}
	if op == "start" {
		// An empty Config must retain OpenSSH's default user and system config.
		if a.Config != "" {
			record.Config, e = a.configPath()
			if e != nil {
				return failure("CONFIG_ERROR", e.Error())
			}
		}
		data, _ := json.Marshal(record)
		if e = atomicFile(state, data); e != nil {
			return failure("STATE_ERROR", e.Error())
		}
	} else {
		record, e = readTunnel(state)
		if e != nil {
			return failure("NOT_FOUND", e.Error())
		}
		if record.Host != fs.Arg(0) {
			return failure("INVALID_ARGUMENT", "tunnel ID belongs to a different host")
		}
	}
	configured := a
	configured.Config = record.Config
	sshArgs := configured.sshArgs(record.Host, "")
	sshArgs = sshArgs[:len(sshArgs)-2]
	if op == "start" {
		// Start with ALL configured forwards cleared; add only the requested
		// loopback forward through the known socket in a separate control request.
		prefix := []string{"-M", "-S", socket, "-f", "-N", "-o", "ControlMaster=yes", "-o", "ControlPersist=no", "-o", "ExitOnForwardFailure=yes", "-o", "GatewayPorts=no"}
		sshArgs = append(prefix, sshArgs...)
	} else {
		operation := "check"
		if op == "stop" {
			operation = "exit"
		}
		sshArgs = []string{"-F", os.DevNull, "-S", socket, "-O", operation, "-o", "BatchMode=yes"}
	}
	sshArgs = append(sshArgs, record.Host)
	r := a.localCommand(ctx, "ssh", sshArgs, nil)
	if op == "start" && r.OK() {
		r = a.localCommand(ctx, "ssh", []string{"-F", os.DevNull, "-S", socket, "-O", "forward", "-o", "BatchMode=yes", "-o", "ExitOnForwardFailure=yes", "-o", "GatewayPorts=no", "-L", "127.0.0.1:" + strconv.Itoa(*lp) + ":" + *rh + ":" + strconv.Itoa(*rp), record.Host}, nil)
	}
	// Keep state even on ambiguous startup failure: callers can check or stop
	// the known socket instead of losing a potentially running SSH master.
	r.Data = record
	if op == "stop" && r.OK() {
		if e = os.Remove(state); e != nil {
			return failure("STATE_ERROR", e.Error())
		}
	}
	return r
}
