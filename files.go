package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

func validRemotePath(p string) bool {
	return strings.HasPrefix(p, "/") && !strings.ContainsAny(p, "\x00\r\n") && p != "/"
}

func (a App) Files(ctx context.Context, direction string, args []string) Result {
	f := newFlags(direction)
	overwrite := f.Bool("overwrite", false, "allow replacement")
	recursive := f.Bool("recursive", false, "directory copy")
	resume := f.Bool("resume", false, "use rsync partial-file recovery")
	if err := parseFlags(f, args); err != nil {
		return failure("INVALID_ARGUMENT", err.Error())
	}
	args = f.Args()
	if len(args) != 3 || !validHost(args[0]) {
		return failure("INVALID_ARGUMENT", direction+" requires HOST SOURCE DESTINATION")
	}
	remote, local := args[2], args[1]
	if direction == "download" {
		remote, local = args[1], args[2]
	}
	if !validRemotePath(remote) {
		return failure("INVALID_ARGUMENT", "remote path must be absolute, non-root, and without control characters")
	}
	absolute, err := filepath.Abs(local)
	if err != nil {
		return failure("INVALID_ARGUMENT", err.Error())
	}
	if *recursive || *resume {
		return a.rsync(ctx, direction, args[0], absolute, remote, *overwrite, *recursive)
	}
	if direction == "upload" {
		return a.upload(ctx, args[0], absolute, remote, *overwrite)
	}
	return a.download(ctx, args[0], remote, absolute, *overwrite)
}

func fileDigest(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return "", 0, err
	}
	if !st.Mode().IsRegular() {
		return "", 0, fmt.Errorf("expected a regular file")
	}
	h := sha256.New()
	n, err := io.Copy(h, f)
	return hex.EncodeToString(h.Sum(nil)), n, err
}

func uploadScript(remote, digest string, size int64, overwrite bool) string {
	commit := "ln -T -- \"$tmp\" \"$target\" && rm -- \"$tmp\""
	if overwrite {
		commit = "mv -fT -- \"$tmp\" \"$target\""
	}
	return "set -eu\numask 077\ntarget=" + quote(remote) + "\n" +
		"[ ! -L \"$target\" ] && { [ ! -e \"$target\" ] || [ -f \"$target\" ]; } || { echo 'nonregular destination rejected' >&2; exit 1; }\n" +
		"tmp=$(mktemp -- \"${target}.vpsctl.XXXXXXXX\")\ntrap 'rm -f -- \"$tmp\"' EXIT HUP INT TERM\ncat > \"$tmp\"\n" +
		fmt.Sprintf("[ \"$(wc -c < \"$tmp\" | tr -d ' ')\" = %s ] || { echo 'size mismatch' >&2; exit 1; }\n", quote(fmt.Sprint(size))) +
		"actual=$(sha256sum < \"$tmp\"); actual=${actual%% *}\n[ \"$actual\" = " + quote(digest) + " ] || { echo 'checksum mismatch' >&2; exit 1; }\n" + commit + "\n"
}

func (a App) upload(ctx context.Context, host, local, remote string, overwrite bool) Result {
	digest, size, err := fileDigest(local)
	if err != nil {
		return failure("LOCAL_IO", err.Error())
	}
	f, err := os.Open(local)
	if err != nil {
		return failure("LOCAL_IO", err.Error())
	}
	defer f.Close()
	r := a.SSH(ctx, host, "sh -c "+quote(uploadScript(remote, digest, size, overwrite)), f)
	r.Data = map[string]any{"host": host, "path": remote, "bytes": size, "sha256": digest, "atomic": true, "overwrite": overwrite}
	return r
}

func (a App) download(ctx context.Context, host, remote, local string, overwrite bool) Result {
	if st, err := os.Lstat(local); err == nil {
		if !overwrite {
			return failure("DESTINATION_EXISTS", "use --overwrite to replace destination")
		}
		if !st.Mode().IsRegular() {
			return failure("INVALID_ARGUMENT", "destination must be a regular file")
		}
	} else if !os.IsNotExist(err) {
		return failure("LOCAL_IO", err.Error())
	}
	// Probe digest before reading. A concurrent source mutation becomes a checksum failure.
	protocol := a
	protocol.MaxOutput = 4096 // Internal checksum/size framing is not display output.
	probe := protocol.Script(ctx, host, "set -eu\n[ -f "+quote(remote)+" ] && [ ! -L "+quote(remote)+" ]\nsha256sum < "+quote(remote)+"\n")
	if !probe.OK() {
		return probe
	}
	parts := strings.Fields(probe.Stdout)
	if len(parts) < 1 || len(parts[0]) != 64 {
		return failure("PROTOCOL_ERROR", "invalid remote checksum response")
	}
	digest := parts[0]
	f, err := os.CreateTemp(filepath.Dir(local), ".vpsctl-download-*")
	if err != nil {
		return failure("LOCAL_IO", err.Error())
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if a.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, a.Timeout)
		defer cancel()
	}
	cmd := a.command(ctx, "ssh", a.sshArgs(host, "cat -- "+quote(remote))...)
	errout := &capture{limit: a.MaxOutput}
	cmd.Stderr = errout
	h := sha256.New()
	cmd.Stdout = io.MultiWriter(f, h)
	if err = cmd.Run(); err != nil {
		r := failure("TRANSFER_FAILED", "download did not complete; destination was not replaced")
		r.Stderr = errout.prefix.String()
		return r
	}
	if hex.EncodeToString(h.Sum(nil)) != digest {
		return failure("CHECKSUM_MISMATCH", "remote file changed or transfer was corrupted; destination was not replaced")
	}
	st, err := f.Stat()
	if err != nil {
		return failure("LOCAL_IO", err.Error())
	}
	if err = f.Sync(); err != nil {
		return failure("LOCAL_IO", err.Error())
	}
	if err = f.Close(); err != nil {
		return failure("LOCAL_IO", err.Error())
	}
	if overwrite {
		err = os.Rename(f.Name(), local)
	} else {
		err = os.Link(f.Name(), local)
	}
	if err != nil {
		return failure("LOCAL_IO", err.Error())
	}
	return success(map[string]any{"host": host, "path": local, "bytes": st.Size(), "sha256": digest, "atomic": true, "overwrite": overwrite})
}

func (a App) rsync(ctx context.Context, direction, host, local, remote string, overwrite, recursive bool) Result {
	// ponytail: rsync commits individual files, not a directory transaction. Use
	// release directories plus an explicit symlink switch for atomic deployments.
	if strings.ContainsAny(local, "\x00\r\n") {
		return failure("INVALID_ARGUMENT", "invalid local path")
	}
	if direction == "upload" {
		st, err := os.Stat(local)
		if err != nil {
			return failure("LOCAL_IO", err.Error())
		}
		if st.IsDir() != recursive {
			return failure("INVALID_ARGUMENT", "directory uploads require --recursive; file uploads must omit it")
		}
	}
	if a.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, a.Timeout)
		defer cancel()
	}
	ssh := a.sshArgs(host, "")
	ssh = ssh[:len(ssh)-2]
	q := []string{"ssh"}
	for _, s := range ssh {
		q = append(q, quote(s))
	}
	args := []string{"--times", "--perms", "--partial", "--delay-updates", "--protect-args", "--safe-links", "--no-specials", "--no-devices", "--chmod=Fu=rw,Fgo=,Du=rwx,Dgo=", "-e", strings.Join(q, " ")}
	if recursive {
		args = append(args, "--recursive")
		local = strings.TrimRight(local, "/") + "/"
		remote = strings.TrimRight(remote, "/") + "/"
	}
	if !overwrite {
		args = append(args, "--ignore-existing")
	}
	args = append(args, "--")
	if direction == "upload" {
		args = append(args, local, host+":"+remote)
	} else {
		args = append(args, host+":"+remote, local)
	}
	cmd := a.command(ctx, "rsync", args...)
	out, errout := &capture{limit: a.MaxOutput}, &capture{limit: a.MaxOutput}
	cmd.Stdout = out
	cmd.Stderr = errout
	err := cmd.Run()
	r := success(map[string]any{"host": host, "local_path": local, "remote_path": remote, "atomic": false, "resume": true, "existing_files": "skipped"})
	if overwrite {
		r.Data.(map[string]any)["existing_files"] = "updated"
	}
	r.Stdout = out.prefix.String()
	r.Stderr = errout.prefix.String()
	r.Truncated = out.total > a.MaxOutput || errout.total > a.MaxOutput
	if err != nil {
		r.Status = "failed"
		r.ExitCode = nil
		r.Error = &Problem{"TRANSFER_FAILED", "rsync failed; some files may already be updated. Requires rsync 3+ locally and remotely; inspect stderr"}
	}
	return r
}

func (a App) Transfer(ctx context.Context, args []string) Result {
	f := newFlags("transfer")
	overwrite := f.Bool("overwrite", false, "replace destination")
	if err := parseFlags(f, args); err != nil {
		return failure("INVALID_ARGUMENT", err.Error())
	}
	args = f.Args()
	if len(args) != 4 || !validHost(args[0]) || !validHost(args[2]) || !validRemotePath(args[1]) || !validRemotePath(args[3]) {
		return failure("INVALID_ARGUMENT", "transfer requires SOURCE_HOST ABSOLUTE_FILE DEST_HOST ABSOLUTE_FILE")
	}
	if args[0] == args[2] && args[1] == args[3] {
		return failure("INVALID_ARGUMENT", "source and destination must differ")
	}
	protocol := a
	protocol.MaxOutput = 4096 // Internal checksum/size framing is not display output.
	probe := protocol.Script(ctx, args[0], "set -eu\n[ -f "+quote(args[1])+" ] && [ ! -L "+quote(args[1])+" ]\nsha256sum < "+quote(args[1])+"\nwc -c < "+quote(args[1])+"\n")
	if !probe.OK() {
		return probe
	}
	fields := strings.Fields(probe.Stdout)
	if len(fields) != 3 || len(fields[0]) != 64 {
		return failure("PROTOCOL_ERROR", "invalid source checksum/size")
	}
	size, err := nonnegative(fields[2])
	if err != nil {
		return failure("PROTOCOL_ERROR", err.Error())
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	if a.Timeout > 0 {
		var stop context.CancelFunc
		ctx, stop = context.WithTimeout(ctx, a.Timeout)
		defer stop()
	}
	source := a.command(ctx, "ssh", a.sshArgs(args[0], "cat -- "+quote(args[1]))...)
	stderr := &capture{limit: a.MaxOutput}
	source.Stderr = stderr
	reader, writer, err := os.Pipe()
	if err != nil {
		return failure("LOCAL_IO", err.Error())
	}
	defer reader.Close()
	source.Stdout = writer
	if err = source.Start(); err != nil {
		writer.Close()
		return failure("DEPENDENCY_MISSING", err.Error())
	}
	writer.Close()
	dest := a.SSH(ctx, args[2], "sh -c "+quote(uploadScript(args[3], fields[0], size, *overwrite)), reader)
	reader.Close()
	if !dest.OK() {
		cancel()
	}
	sourceErr := source.Wait()
	dest.Data = map[string]any{"source_host": args[0], "destination_host": args[2], "path": args[3], "bytes": size, "sha256": fields[0], "mode": "stream", "atomic": true}
	if sourceErr != nil {
		dest.Status = "unknown"
		dest.ExitCode = nil
		dest.Error = &Problem{"TRANSFER_SOURCE_FAILED", "source stream failed; inspect destination before retrying"}
		dest.Stderr += "\n" + stderr.prefix.String()
	}
	return dest
}
