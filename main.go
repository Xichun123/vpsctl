package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
)

var version = "0.4.0"

func newFlags(name string) *flag.FlagSet {
	f := flag.NewFlagSet(name, flag.ContinueOnError)
	f.SetOutput(io.Discard)
	return f
}

// parseFlags permits flags after positionals without splitting command strings.
// Use -- before positionals that start with a dash. Never tokenize remote code.
func parseFlags(f *flag.FlagSet, args []string) error {
	var opts, pos []string
	for i := 0; i < len(args); i++ {
		s := args[i]
		if s == "--" {
			pos = append(pos, args[i+1:]...)
			break
		}
		if len(s) < 2 || s[0] != '-' {
			pos = append(pos, s)
			continue
		}
		name := strings.TrimLeft(s, "-")
		key, _, hasValue := strings.Cut(name, "=")
		item := f.Lookup(key)
		if item == nil {
			return fmt.Errorf("unknown option: %s", s)
		}
		opts = append(opts, s)
		isBool := false
		if b, ok := item.Value.(interface{ IsBoolFlag() bool }); ok {
			isBool = b.IsBoolFlag()
		}
		if !hasValue && !isBool {
			i++
			if i >= len(args) {
				return fmt.Errorf("missing value for %s", s)
			}
			opts = append(opts, args[i])
		}
	}
	return f.Parse(append(append(opts, "--"), pos...))
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	a := App{Timeout: 0, MaxOutput: 1024 * 1024, Stdin: os.Stdin}
	r := a.Run(ctx, os.Args[1:])
	enc := json.NewEncoder(os.Stdout)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(r); err != nil {
		os.Exit(1)
	}
	if !r.OK() {
		os.Exit(1)
	}
}

var help = map[string]any{
	"version": version,
	"usage":   "vpsctl [--ssh-config FILE] [--timeout DURATION] [--max-output BYTES] COMMAND ...",
	"commands": []string{
		"host list|find|show|add|update|delete ...",
		"exec HOST [--cwd PATH] [--stdin | --script-file FILE | COMMAND] [--detach]",
		"job status HOST ID | job output HOST ID [--cursor BYTES] [--limit BYTES]",
		"upload HOST LOCAL REMOTE [--overwrite] [--recursive] [--resume]",
		"download HOST REMOTE LOCAL [--overwrite] [--recursive] [--resume]",
		"transfer SOURCE SOURCE_PATH DESTINATION DEST_PATH [--overwrite]",
		"cluster --hosts HOST1,HOST2 [--parallel N] [--stdin | --script-file FILE | COMMAND]",
		"tunnel start|list|status|stop ...",
		"key add|verify|rollback ...",
	},
	"policy": "JSON only; key/ssh-agent authentication; pre-provision verified known_hosts; never retry uncertain mutations; local timeout does not cancel remote work. Remote scripts use POSIX sh; use an explicit interpreter for other languages.",
}

func (a App) Run(ctx context.Context, args []string) Result {
	if len(args) == 1 && args[0] == "--version" {
		return success(map[string]string{"version": version})
	}
	// Global options must precede the command, leaving remote arguments untouched.
	f := newFlags("vpsctl")
	f.StringVar(&a.Config, "ssh-config", a.Config, "SSH config file")
	f.DurationVar(&a.Timeout, "timeout", a.Timeout, "local wait timeout (0 means unlimited)")
	f.IntVar(&a.MaxOutput, "max-output", a.MaxOutput, "max bytes per output stream")
	if err := f.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return success(help)
		}
		return failure("INVALID_ARGUMENT", err.Error())
	}
	if a.Timeout < 0 || a.MaxOutput < 1 || a.MaxOutput > 64*1024*1024 {
		return failure("INVALID_ARGUMENT", "timeout must be nonnegative; max-output must be 1..67108864")
	}
	args = f.Args()
	if len(args) == 0 {
		return success(help)
	}
	if args[0] == "version" || args[0] == "--version" {
		return success(map[string]string{"version": version})
	}
	if len(args) == 2 && (args[1] == "--help" || args[1] == "-h") {
		return success(help)
	}
	switch args[0] {
	case "help":
		return success(help)
	case "exec":
		return a.Exec(ctx, args[1:])
	case "job":
		return a.Job(ctx, args[1:])
	case "host":
		return a.Host(ctx, args[1:])
	case "list":
		return a.Host(ctx, append([]string{"list"}, args[1:]...))
	case "find":
		return a.Host(ctx, append([]string{"find"}, args[1:]...))
	case "key":
		return a.Key(ctx, args[1:])
	case "tunnel":
		return a.Tunnel(ctx, args[1:])
	case "upload", "download":
		return a.Files(ctx, args[0], args[1:])
	case "transfer":
		return a.Transfer(ctx, args[1:])
	case "cluster":
		return a.Cluster(ctx, args[1:])
	default:
		return failure("INVALID_ARGUMENT", "unknown command: "+args[0])
	}
}

func readScript(stdin io.Reader, file string, fromStdin bool, command []string) (string, error) {
	modes := 0
	if file != "" {
		modes++
	}
	if fromStdin {
		modes++
	}
	if len(command) > 0 {
		modes++
	}
	if modes != 1 {
		return "", fmt.Errorf("provide exactly one command string, --stdin, or --script-file")
	}
	if len(command) > 1 {
		return "", fmt.Errorf("remote command must be one argument; use --stdin for scripts")
	}
	if len(command) == 1 {
		return command[0], nil
	}
	var r io.Reader = stdin
	if file != "" {
		f, err := os.Open(file)
		if err != nil {
			return "", err
		}
		defer f.Close()
		r = f
	}
	if r == nil {
		return "", fmt.Errorf("stdin is unavailable")
	}
	b, err := io.ReadAll(io.LimitReader(r, 8*1024*1024+1))
	if len(b) > 8*1024*1024 {
		return "", fmt.Errorf("script exceeds 8 MiB")
	}
	return string(b), err
}

func (a App) Exec(ctx context.Context, args []string) Result {
	f := newFlags("exec")
	file := f.String("script-file", "", "local script")
	stdin := f.Bool("stdin", false, "read script from stdin")
	cwd := f.String("cwd", "", "remote absolute working directory")
	detach := f.Bool("detach", false, "persist remote job")
	if err := parseFlags(f, args); err != nil {
		return failure("INVALID_ARGUMENT", err.Error())
	}
	args = f.Args()
	if len(args) < 1 {
		return failure("INVALID_ARGUMENT", "exec requires a host")
	}
	script, err := readScript(a.Stdin, *file, *stdin, args[1:])
	if err != nil {
		return failure("INVALID_ARGUMENT", err.Error())
	}
	if *cwd != "" {
		if *cwd != "/" && !validRemotePath(*cwd) {
			return failure("INVALID_ARGUMENT", "cwd must be an absolute path without control characters")
		}
		script = "cd " + quote(*cwd) + " || exit $?\n" + script
	}
	if *detach {
		return a.StartJob(ctx, args[0], script)
	}
	return a.Script(ctx, args[0], script)
}

func (a App) Cluster(ctx context.Context, args []string) Result {
	f := newFlags("cluster")
	hosts := f.String("hosts", "", "explicit comma-separated hosts")
	parallel := f.Int("parallel", 4, "concurrent hosts")
	file := f.String("script-file", "", "script file")
	stdin := f.Bool("stdin", false, "read stdin")
	if err := parseFlags(f, args); err != nil {
		return failure("INVALID_ARGUMENT", err.Error())
	}
	if *hosts == "" || *parallel < 1 || *parallel > 64 {
		return failure("INVALID_ARGUMENT", "explicit --hosts and --parallel 1..64 required")
	}
	script, err := readScript(a.Stdin, *file, *stdin, f.Args())
	if err != nil {
		return failure("INVALID_ARGUMENT", err.Error())
	}
	names := strings.Split(*hosts, ",")
	seen := map[string]bool{}
	if len(names) > 256 {
		return failure("INVALID_ARGUMENT", "at most 256 hosts per batch")
	}
	for _, h := range names {
		if !validHost(h) || seen[h] {
			return failure("INVALID_ARGUMENT", "hosts must be valid and unique")
		}
		seen[h] = true
	}
	// Bound the whole batch transcript, not just each subprocess, so a large
	// host list cannot multiply --max-output into unbounded Agent output.
	if perStream := (8 << 20) / (2 * len(names)); a.MaxOutput > perStream {
		a.MaxOutput = perStream
	}
	type item struct {
		Host   string `json:"host"`
		Result Result `json:"result"`
	}
	results := make([]item, len(names))
	work := make(chan int)
	var wg sync.WaitGroup
	for n := 0; n < *parallel; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range work {
				results[i] = item{names[i], a.Script(ctx, names[i], script)}
			}
		}()
	}
	for i := range names {
		work <- i
	}
	close(work)
	wg.Wait()
	r := success(results)
	for _, v := range results {
		r.Truncated = r.Truncated || v.Result.Truncated
		if !v.Result.OK() {
			r.Status = "failed"
			r.ExitCode = nil
			r.Error = &Problem{"BATCH_FAILED", "one or more hosts failed or have unknown outcomes; inspect each result"}
		}
	}
	return r
}

// Common decimal validation for byte offsets exposed to shell scripts.
func nonnegative(s string) (int64, error) {
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("expected a nonnegative integer")
	}
	return n, nil
}
