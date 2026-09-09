package main

import (
	"context"
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"
)

const jobOutputLimit = 1 << 20

// Storage is private to the remote account. Like SSH itself, this is not a
// security boundary against another process running as that same account.
const jobStorage = `umask 077
die() { printf '%s\n' "$2" >&2; exit "$1"; }
case "$HOME" in /*) ;; *) die 46 'HOME must be absolute';; esac
[ -d "$HOME" ] && [ ! -L "$HOME" ] || die 46 'unsafe HOME'
p=$HOME
for component in .local state vpsctl jobs; do
 p=$p/$component
 [ ! -L "$p" ] || die 46 'symlink in job storage'
 if [ ! -d "$p" ]; then mkdir "$p" 2>/dev/null || [ -d "$p" ] || die 46 'cannot create job storage'; fi
 [ ! -L "$p" ] && [ -d "$p" ] || die 46 'unsafe job storage'
 [ "$(stat -c %u "$p")" = "$(id -u)" ] || die 46 'job storage must belong to this user'
 case "$(stat -c %a "$p")" in *[2367][0-7]|*[0-7][2367]) die 46 'job storage is writable by other users';; esac
done
`

// /proc start time plus boot ID avoids mistaking a reused PID (or reboot) for
// the original worker. A missing completion record is never success.
const jobIdentity = `identity() {
 [ -r /proc/sys/kernel/random/boot_id ] && [ -r "/proc/$1/stat" ] || return 1
 boot=$(cat /proc/sys/kernel/random/boot_id) || return 1
 proc=$(cat "/proc/$1/stat") || return 1
 proc=${proc##*) }
 set -- $proc
 [ "$1" != Z ] && [ "$1" != X ] || return 1
 [ "$#" -ge 20 ] || return 1
 shift 19
 printf '%s %s\n' "$boot" "$1"
}
`

const jobWorker = `umask 077
d=$1
cd "$d" || exit 1
` + jobIdentity + `stamp=$(identity $$) || exit 1
printf '%s %s\n' "$$" "$stamp" > ready.tmp && mv ready.tmp ready || exit 1
(cd "$HOME" && sh "$d/script" < /dev/null) >> log 2>&1
rc=$?
printf '%s\n' "$rc" > exit.tmp && mv exit.tmp exit
`

func validJobID(id string) bool {
	if len(id) != 32 {
		return false
	}
	for _, c := range id {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

func jobData(host, id string) map[string]any { return map[string]any{"host": host, "job_id": id} }

func jobRemoteError(r Result) Result {
	if r.Error != nil || r.ExitCode == nil || *r.ExitCode == 0 {
		return r
	}
	code := "JOB_ERROR"
	switch *r.ExitCode {
	case 44:
		code = "JOB_NOT_FOUND"
	case 45:
		code = "INVALID_CURSOR"
	case 46:
		code = "UNSAFE_STORAGE"
	case 47:
		code = "DEPENDENCY_MISSING"
	}
	r.Error = &Problem{Code: code, Message: strings.TrimSpace(r.Stderr)}
	r.Status = "failed"
	return r
}

// StartJob never retries: even an uncertain launch returns its chosen ID, so
// callers can reconcile with job status rather than execute the script twice.
func (a App) StartJob(ctx context.Context, host, script string) Result {
	id := token()
	data := jobData(host, id)
	fail := func(code, message string) Result { r := failure(code, message); r.Data = data; return r }
	if !validHost(host) {
		return fail("INVALID_ARGUMENT", "invalid SSH host alias")
	}
	if strings.IndexByte(script, 0) >= 0 {
		return fail("INVALID_ARGUMENT", "script contains a NUL byte")
	}
	remote := jobStorage + `for tool in nohup setsid base64; do command -v "$tool" >/dev/null 2>&1 || die 47 "missing $tool"; done
[ -r /proc/sys/kernel/random/boot_id ] || die 47 'Linux procfs is required'
d=$p/` + id + `
mkdir "$d" || die 46 'job ID already exists; refusing duplicate launch'
cd "$d" || die 46 'cannot enter job directory'
`
	for _, item := range []struct{ name, text string }{{"script", script}, {"worker", jobWorker}} {
		remote += "printf '%s' " + quote(base64.StdEncoding.EncodeToString([]byte(item.text))) + " | base64 -d > " + item.name + " || die 46 'cannot store job input'\n"
	}
	remote += `: > log || die 46 'cannot create job log'
nohup setsid sh worker "$d" </dev/null >/dev/null 2>&1 &
i=0
while [ "$i" -lt 5 ]; do
 if [ -f ready ]; then printf 'started\n'; exit 0; fi
 sleep 1
 i=$((i+1))
done
die 48 'launch not acknowledged; reconcile this job ID before retrying'
`
	protocol := a
	protocol.MaxOutput = 4096
	r := jobRemoteError(protocol.Script(ctx, host, remote))
	r.Data = data
	if r.OK() && r.Stdout == "started\n" {
		r.Status, r.ExitCode, r.Stdout = "running", nil, ""
		data["state"] = "running"
	} else if r.Error == nil || r.ExitCode != nil && *r.ExitCode == 48 {
		r.Status, r.ExitCode = "unknown", nil
		r.Error = &Problem{"OUTCOME_UNKNOWN", "launch not acknowledged; reconcile this job ID before retrying"}
	}
	return r
}

// Job accepts output flags on either side of HOST ID. Output cursors and limits
// are byte counts, not Unicode character counts. Logs are not rotated or capped.
func (a App) Job(ctx context.Context, args []string) Result {
	if len(args) == 0 || args[0] != "status" && args[0] != "output" {
		return failure("INVALID_ARGUMENT", "usage: job status HOST ID | job output HOST ID [--cursor N] [--limit N]")
	}
	action := args[0]
	cursor, limit := int64(0), int64(64<<10)
	var positional []string
	seen := map[string]bool{}
	for i := 1; i < len(args); i++ {
		arg := args[i]
		if strings.HasPrefix(arg, "--") {
			name, value, hasValue := strings.Cut(arg, "=")
			if action != "output" || name != "--cursor" && name != "--limit" || seen[name] {
				return failure("INVALID_ARGUMENT", "unknown or duplicate job flag: "+name)
			}
			seen[name] = true
			if !hasValue {
				i++
				if i >= len(args) {
					return failure("INVALID_ARGUMENT", "missing value for "+name)
				}
				value = args[i]
			}
			n, err := strconv.ParseInt(value, 10, 64)
			if err != nil || n < 0 || name == "--limit" && (n == 0 || n > jobOutputLimit) {
				return failure("INVALID_ARGUMENT", "cursor must be nonnegative; limit must be 1..1048576")
			}
			if name == "--cursor" {
				cursor = n
			} else {
				limit = n
			}
		} else {
			positional = append(positional, arg)
		}
	}
	if len(positional) != 2 || !validHost(positional[0]) || !validJobID(positional[1]) {
		return failure("INVALID_ARGUMENT", "expected a valid HOST and 32 lowercase hexadecimal job ID")
	}
	host, id := positional[0], positional[1]
	data := jobData(host, id)
	remote := jobStorage + "d=$p/" + id + `
[ ! -L "$d" ] || die 46 'symlink job directory'
[ -d "$d" ] || die 44 'job does not exist'
cd "$d" || die 46 'cannot enter job directory'
for file in ready exit log; do
 [ ! -L "$file" ] || die 46 'symlink job file'
 if [ -e "$file" ]; then [ -f "$file" ] || die 46 'nonregular job file'; fi
done
`
	if action == "status" {
		remote += jobIdentity + `if [ -f exit ]; then
 rc=$(cat exit)
 case "$rc" in ''|*[!0-9]*) die 46 'invalid completion record';; esac
 [ "$rc" -le 255 ] || die 46 'invalid completion record'
 printf 'completed %s\n' "$rc"
elif [ -f ready ]; then
 read -r pid boot start < ready
 case "$pid" in ''|*[!0-9]*) die 46 'invalid worker record';; esac
 now=$(identity "$pid")
 if [ "$now" = "$boot $start" ]; then printf 'running\n'; else printf 'unknown\n'; fi
else
 printf 'unknown\n'
fi
`
	} else {
		if a.MaxOutput > 0 && int64(a.MaxOutput) < limit {
			limit = int64(a.MaxOutput)
		}
		remote += fmt.Sprintf(`cursor=%d
limit=%d
[ -f log ] || die 44 'job log does not exist'
size=$(wc -c < log) || die 46 'cannot measure log'
[ "$cursor" -le "$size" ] || die 45 'cursor exceeds log size'
n=$((size-cursor))
[ "$n" -le "$limit" ] || n=$limit
printf '%%s %%s\n' "$size" "$n"
dd if=log bs=65536 skip="$cursor" count="$n" iflag=skip_bytes,count_bytes 2>/dev/null | base64
`, cursor, limit)
	}
	protocol := a
	protocol.MaxOutput = int(limit)*2 + 4096 // Includes base64 wrapping and protocol header.
	r := jobRemoteError(protocol.Script(ctx, host, remote))
	r.Data = data
	if !r.OK() {
		return r
	}
	bad := func() Result {
		r.Status, r.ExitCode = "unknown", nil
		r.Error = &Problem{"JOB_PROTOCOL_ERROR", "invalid or incomplete job response"}
		return r
	}
	if r.Truncated {
		return bad()
	}
	if action == "status" {
		fields := strings.Fields(r.Stdout)
		if len(fields) == 2 && fields[0] == "completed" {
			n, err := strconv.Atoi(fields[1])
			if err != nil || n < 0 || n > 255 {
				return bad()
			}
			r.Status, r.ExitCode = "completed", &n
		} else if len(fields) == 1 && (fields[0] == "running" || fields[0] == "unknown") {
			r.Status, r.ExitCode = fields[0], nil
			if r.Status == "unknown" {
				r.Error = &Problem{"OUTCOME_UNKNOWN", "worker is absent or unacknowledged without a completion record; do not blindly retry"}
			}
		} else {
			return bad()
		}
		r.Stdout = ""
		data["state"] = r.Status
		return r
	}
	header, encoded, ok := strings.Cut(r.Stdout, "\n")
	fields := strings.Fields(header)
	if !ok || len(fields) != 2 {
		return bad()
	}
	size, err := strconv.ParseInt(fields[0], 10, 64)
	n, nerr := strconv.ParseInt(fields[1], 10, 64)
	if err != nil || nerr != nil || size < cursor || n < 0 || n > limit || n > size-cursor {
		return bad()
	}
	chunk, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || int64(len(chunk)) != n {
		return bad()
	}
	r.Stdout = strings.ToValidUTF8(string(chunk), "\ufffd")
	r.Truncated = cursor+n < size
	data["cursor"], data["next_cursor"], data["size"] = cursor, cursor+n, size
	data["output_base64"] = base64.StdEncoding.EncodeToString(chunk) // Lossless even for binary/non-UTF-8 logs.
	return r
}
