package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Inventory is intentionally syntactic: an alias is not a claim of connectivity.
// SSH configuration is trusted local code (e.g. ProxyCommand). Match is never
// evaluated by inventory, and show/verify refuse it to prevent Match exec.
type hostEntry struct {
	Alias  string   `json:"alias"`
	Source string   `json:"source"`
	Labels []string `json:"labels,omitempty"`
}

func (a App) configPath() (string, error) {
	if a.Config != "" {
		return filepath.Abs(a.Config)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".ssh", "config"), nil
}

// configWords handles OpenSSH quoting, comments, and key=value without shell
// expansion. Unsupported or malformed syntax fails closed rather than guessing.
func configWords(s string) ([]string, error) {
	var words []string
	var b strings.Builder
	var q rune
	escaped, started := false, false
	flush := func() {
		if started {
			words = append(words, b.String())
			b.Reset()
			started = false
		}
	}
	for i, c := range s {
		if escaped {
			b.WriteRune(c)
			escaped = false
			started = true
			continue
		}
		// OpenSSH only unescapes quotes, backslashes, and unquoted spaces.
		// Other backslashes are literal (not shell escapes).
		if c == '\\' && i+1 < len(s) {
			next := s[i+1]
			if next == '\\' || next == '"' || next == '\'' || (q == 0 && next == ' ') {
				escaped = true
				started = true
				continue
			}
		}
		if q != 0 {
			if c == q {
				q = 0
			} else {
				b.WriteRune(c)
			}
			continue
		}
		if c == '"' || c == '\'' {
			q = c
			started = true
			continue
		}
		if c == '#' && !started {
			break
		}
		if c == ' ' || c == '\t' || c == '\r' || c == '\n' || (c == '=' && (len(words) == 0 || (len(words) == 1 && !started))) {
			flush()
			continue
		}
		b.WriteRune(c)
		started = true
	}
	if escaped || q != 0 {
		return nil, fmt.Errorf("unsupported unterminated SSH config quoting")
	}
	flush()
	return words, nil
}

func scanHosts(path string) ([]hostEntry, bool, error) {
	entries := []hostEntry{}
	match := false
	seen := map[string]bool{}
	count := 0
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, false, err
	}
	var walk func(string, int) error
	walk = func(p string, depth int) error {
		if depth > 16 {
			return fmt.Errorf("SSH Include nesting exceeds 16")
		}
		p, err = filepath.Abs(p)
		if err != nil {
			return err
		}
		if seen[p] {
			return nil
		}
		seen[p] = true
		count++
		if count > 1024 {
			return fmt.Errorf("too many SSH Include files")
		}
		data, e := os.ReadFile(p)
		if os.IsNotExist(e) && depth == 0 {
			return nil
		}
		if e != nil {
			return e
		}
		if len(data) > 4<<20 {
			return fmt.Errorf("SSH config too large")
		}
		var labels []string
		for _, line := range strings.Split(string(data), "\n") {
			t := strings.TrimSpace(line)
			if strings.HasPrefix(t, "# vpsctl-labels:") {
				labels = strings.Fields(strings.TrimSpace(strings.TrimPrefix(t, "# vpsctl-labels:")))
				continue
			}
			w, e := configWords(line)
			if e != nil {
				return fmt.Errorf("%s: %w", p, e)
			}
			if len(w) == 0 {
				continue
			}
			switch strings.ToLower(w[0]) {
			case "match":
				match = true
			case "host":
				for _, alias := range w[1:] {
					if validHost(alias) && !strings.ContainsAny(alias, "*?!%") {
						entries = append(entries, hostEntry{alias, p, append([]string(nil), labels...)})
					}
				}
				labels = nil
			case "include":
				for _, pattern := range w[1:] {
					if strings.ContainsAny(pattern, "%${}") {
						return fmt.Errorf("unsupported dynamic Include in %s", p)
					}
					if strings.HasPrefix(pattern, "~/") {
						pattern = filepath.Join(home, pattern[2:])
					} else if strings.HasPrefix(pattern, "~") {
						return fmt.Errorf("unsupported user Include in %s", p)
					}
					if !filepath.IsAbs(pattern) {
						pattern = filepath.Join(home, ".ssh", pattern)
					}
					files, e := filepath.Glob(pattern)
					if e != nil {
						return e
					}
					for _, file := range files {
						if e = walk(file, depth+1); e != nil {
							return e
						}
					}
				}
			}
		}
		return nil
	}
	err = walk(path, 0)
	return entries, match, err
}

func (a App) Host(ctx context.Context, args []string) Result {
	if len(args) == 0 {
		return failure("INVALID_ARGUMENT", "host requires list, find, show, add, update, or delete")
	}
	path, err := a.configPath()
	if err != nil {
		return failure("CONFIG_ERROR", err.Error())
	}
	switch args[0] {
	case "list", "find":
		if (args[0] == "list" && len(args) != 1) || (args[0] == "find" && len(args) != 2) {
			return failure("INVALID_ARGUMENT", "usage: host list | host find QUERY")
		}
		hosts, _, err := scanHosts(path)
		if err != nil {
			return failure("CONFIG_ERROR", err.Error())
		}
		if args[0] == "find" {
			filtered := []hostEntry{}
			q := strings.ToLower(args[1])
			for _, h := range hosts {
				if strings.Contains(strings.ToLower(h.Alias+" "+strings.Join(h.Labels, " ")), q) {
					filtered = append(filtered, h)
				}
			}
			hosts = filtered
		}
		return success(hosts)
	case "show":
		if len(args) != 2 || !validHost(args[1]) {
			return failure("INVALID_ARGUMENT", "usage: host show HOST")
		}
		return a.resolvedHost(ctx, args[1])
	case "add", "update", "delete":
		return a.editHost(path, args[0], args[1:])
	default:
		return failure("INVALID_ARGUMENT", "unknown host operation")
	}
}

func (a App) resolvedHost(ctx context.Context, host string) Result {
	path, err := a.configPath()
	if err != nil {
		return failure("CONFIG_ERROR", err.Error())
	}
	_, match, err := scanHosts(path)
	if err != nil {
		return failure("CONFIG_ERROR", err.Error())
	}
	if match {
		return failure("UNSAFE_CONFIG", "show and key verify refuse Match stanzas; ssh -G may execute Match exec")
	}
	if _, e := os.Stat(path); os.IsNotExist(e) && a.Config == "" {
		path = os.DevNull
	}
	// Explicit -F also excludes the uninspected system configuration.
	args := a.sshArgs(host, "")
	args = args[:len(args)-2]
	args = append([]string{"-G", "-F", path}, args...)
	args = append(args, host)
	return a.localCommand(ctx, "ssh", args, nil)
}

func simpleValue(s string) bool   { return s != "" && !strings.ContainsAny(s, "\r\n\x00\"\\#%$") }
func configValue(s string) string { return "\"" + s + "\"" }

// safeDir refuses symlink components. Mutation paths must remain owned/trusted
// by the user; the lock serializes vpsctl writers, not unrelated editors.
func safeDir(dir string) error {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	parts := strings.Split(strings.TrimPrefix(abs, string(filepath.Separator)), string(filepath.Separator))
	p := string(filepath.Separator)
	for _, part := range parts {
		if part == "" {
			continue
		}
		p = filepath.Join(p, part)
		info, e := os.Lstat(p)
		if os.IsNotExist(e) {
			if e = os.Mkdir(p, 0700); e != nil {
				return e
			}
			continue
		}
		if e != nil {
			return e
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("unsafe directory %s", p)
		}
	}
	return nil
}
func regularOrMissing(path string) error {
	info, e := os.Lstat(path)
	if os.IsNotExist(e) {
		return nil
	}
	if e != nil {
		return e
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("refusing non-regular file %s", path)
	}
	return nil
}
func localLock(path string) (func(), error) {
	f, e := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if e != nil {
		return nil, fmt.Errorf("cannot acquire lock %s (remove only after confirming no writer): %w", path, e)
	}
	if e = f.Close(); e != nil {
		os.Remove(path)
		return nil, e
	}
	return func() { os.Remove(path) }, nil
}
func atomicFile(path string, data []byte) error {
	if e := regularOrMissing(path); e != nil {
		return e
	}
	f, e := os.CreateTemp(filepath.Dir(path), ".vpsctl-write-")
	if e != nil {
		return e
	}
	defer os.Remove(f.Name())
	if e = f.Chmod(0600); e == nil {
		_, e = f.Write(data)
	}
	if e == nil {
		e = f.Sync()
	}
	ce := f.Close()
	if e == nil {
		e = ce
	}
	if e != nil {
		return e
	}
	return os.Rename(f.Name(), path)
}

func (a App) editHost(path, op string, args []string) Result {
	fs := newFlags("host " + op)
	alias := fs.String("alias", "", "")
	hostname := fs.String("hostname", "", "")
	user := fs.String("user", "", "")
	identity := fs.String("identity", "", "")
	port := fs.Int("port", 0, "")
	jump := fs.String("jump", "", "")
	if e := parseFlags(fs, args); e != nil {
		return failure("INVALID_ARGUMENT", e.Error())
	}
	if fs.NArg() != 0 || !validHost(*alias) || strings.ContainsAny(*alias, "@:%[]") {
		return failure("INVALID_ARGUMENT", "supply a literal --alias; no positional arguments")
	}
	fields := map[string]string{}
	bad := ""
	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "hostname":
			fields["hostname"] = *hostname
		case "user":
			fields["user"] = *user
		case "identity":
			fields["identityfile"] = *identity
		case "port":
			fields["port"] = strconv.Itoa(*port)
		case "jump":
			fields["proxyjump"] = *jump
		}
	})
	for k, v := range fields {
		if !simpleValue(v) {
			bad = k
		}
		switch k {
		case "hostname", "user":
			if !validHost(v) || strings.ContainsAny(v, "@%") {
				bad = k
			}
		case "proxyjump":
			for _, hop := range strings.Split(v, ",") {
				if !validHost(hop) || strings.Contains(hop, "%") {
					bad = k
				}
			}
		}
	}
	if bad != "" || (*port != 0 && (*port < 1 || *port > 65535)) {
		return failure("INVALID_ARGUMENT", "invalid SSH field value")
	}
	if _, ok := fields["port"]; ok && *port == 0 {
		return failure("INVALID_ARGUMENT", "port must be 1..65535")
	}
	if op == "add" && *hostname == "" {
		return failure("INVALID_ARGUMENT", "host add requires --hostname")
	}
	if op == "delete" && len(fields) > 0 {
		return failure("INVALID_ARGUMENT", "host delete accepts only --alias")
	}
	if e := safeDir(filepath.Dir(path)); e != nil {
		return failure("CONFIG_ERROR", e.Error())
	}
	unlock, e := localLock(path + ".vpsctl-lock")
	if e != nil {
		return failure("LOCKED", e.Error())
	}
	defer unlock()
	if e = regularOrMissing(path); e != nil {
		return failure("CONFIG_ERROR", e.Error())
	}
	data, e := os.ReadFile(path)
	if e != nil && !os.IsNotExist(e) {
		return failure("CONFIG_ERROR", e.Error())
	}
	lines := strings.SplitAfter(string(data), "\n")
	start, end := -1, len(lines)
	active := false
	occurrences := 0
	for i, line := range lines {
		w, e := configWords(line)
		if e != nil {
			return failure("UNSAFE_CONFIG", e.Error())
		}
		if len(w) == 0 {
			continue
		}
		key := strings.ToLower(w[0])
		if key == "include" || key == "match" {
			return failure("UNSAFE_CONFIG", "editing configs containing Include or Match is not supported; edit them manually")
		}
		if key == "host" {
			if active {
				end = i
				active = false
			}
			for _, v := range w[1:] {
				if strings.EqualFold(v, *alias) {
					occurrences++
					if len(w) != 2 {
						return failure("UNSAFE_CONFIG", "target alias shares a Host block")
					}
					start = i
					active = true
				}
			}
			continue
		}
		if active {
			switch key {
			case "hostname", "user", "identityfile", "port", "proxyjump":
			default:
				return failure("UNSAFE_CONFIG", "target block contains unsupported directive "+w[0])
			}
		}
	}
	if occurrences > 1 {
		return failure("UNSAFE_CONFIG", "duplicate Host blocks")
	}
	if op == "add" && occurrences > 0 {
		return failure("ALREADY_EXISTS", "alias already exists")
	}
	if op != "add" && occurrences == 0 {
		return failure("NOT_FOUND", "alias not found in writable config")
	}
	if op == "update" || op == "delete" {
		seen := map[string]bool{}
		for i := start + 1; i < end; i++ {
			w, _ := configWords(lines[i])
			if len(w) == 0 {
				continue
			}
			k := strings.ToLower(w[0])
			if seen[k] || len(w) != 2 || !simpleValue(w[1]) || strings.Contains(lines[i], "#") {
				return failure("UNSAFE_CONFIG", "ambiguous target directive")
			}
			seen[k] = true
			if _, ok := fields[k]; !ok {
				fields[k] = w[1]
			}
		}
	}
	var result string
	if op == "delete" {
		// Keep comments and blank lines, including comments belonging to the next host.
		kept := []string{}
		for _, line := range lines[start+1 : end] {
			w, _ := configWords(line)
			if len(w) == 0 {
				kept = append(kept, line)
			}
		}
		result = strings.Join(lines[:start], "") + strings.Join(kept, "") + strings.Join(lines[end:], "")
	} else {
		block := "Host " + *alias + "\n"
		for _, k := range []string{"hostname", "user", "identityfile", "port", "proxyjump"} {
			if v, ok := fields[k]; ok {
				block += "    " + k + " " + configValue(v) + "\n"
			}
		}
		if op == "add" {
			insert := len(lines)
			for i, line := range lines {
				w, _ := configWords(line)
				if len(w) > 0 && strings.EqualFold(w[0], "host") {
					insert = i
					break
				}
			}
			// Keep labels/comments immediately above the old first Host with it.
			for insert > 0 {
				w, _ := configWords(lines[insert-1])
				if len(w) != 0 {
					break
				}
				insert--
			}
			prefix := strings.Join(lines[:insert], "")
			if prefix != "" && !strings.HasSuffix(prefix, "\n") {
				prefix += "\n"
			}
			result = prefix + block + "\n" + strings.Join(lines[insert:], "")
		} else {
			var comments string
			for _, line := range lines[start+1 : end] {
				w, _ := configWords(line)
				if len(w) == 0 {
					comments += line
				}
			}
			result = strings.Join(lines[:start], "") + block + comments + strings.Join(lines[end:], "")
		}
	}
	backup := path + ".vpsctl-backup-" + token()
	if e = atomicFile(backup, data); e != nil {
		return failure("CONFIG_ERROR", e.Error())
	}
	if e = atomicFile(path, []byte(result)); e != nil {
		return failure("CONFIG_ERROR", e.Error())
	}
	return success(map[string]string{"alias": *alias, "config": path, "backup": backup, "action": op})
}
