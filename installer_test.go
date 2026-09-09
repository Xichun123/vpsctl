package main

import (
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestReleaseInstaller(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("installer targets Linux/macOS")
	}
	script, err := filepath.Abs("scripts/install.sh")
	if err != nil {
		t.Fatal(err)
	}
	for _, scenario := range []string{"latest", "pinned", "corrupt", "missing", "duplicate", "download-failed", "directory", "symlink", "invalid-version", "unsupported"} {
		t.Run(scenario, func(t *testing.T) {
			home := managementHome(t)
			dest := filepath.Join(home, "install dir")
			if err := os.Mkdir(dest, 0700); err != nil {
				t.Fatal(err)
			}
			target := filepath.Join(dest, "vpsctl")
			managementWrite(t, target, "old CLI")
			payload := "#!/bin/sh\nprintf 'release binary\\n'\n"
			fixture := filepath.Join(home, "payload")
			managementWrite(t, fixture, payload)
			asset := "vpsctl_" + runtime.GOOS + "_" + runtime.GOARCH
			sums := fmt.Sprintf("%x  %s\n", sha256.Sum256([]byte(payload)), asset)
			switch scenario {
			case "corrupt":
				sums = strings.Repeat("0", 64) + "  " + asset + "\n"
			case "missing":
				sums = ""
			case "duplicate":
				sums += sums
			case "directory":
				if err := os.Remove(target); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(target, 0700); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err := os.Remove(target); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(fixture, target); err != nil {
					t.Fatal(err)
				}
			case "unsupported":
				managementBinary(t, home, "uname", "printf 'unsupported\\n'\n")
			}
			managementWrite(t, filepath.Join(home, "checksums"), sums)
			managementBinary(t, home, "curl", `out=''
previous=''
for arg do
 if [ "$previous" = --output ]; then out=$arg; fi
 previous=$arg
 url=$arg
done
printf '%s\n' "$url" >> "$HOME/requests"
case "$url" in
 https://github.com/Xichun123/vpsctl/releases/latest)
  printf 'https://github.com/Xichun123/vpsctl/releases/tag/v0.4.0/';;
 https://github.com/Xichun123/vpsctl/releases/download/v0.4.0/checksums.txt)
  cp "$HOME/checksums" "$out";;
 https://github.com/Xichun123/vpsctl/releases/download/v0.4.0/vpsctl_*)
  [ "$SCENARIO" != download-failed ] || exit 22
  cp "$HOME/payload" "$out";;
 *) echo "unexpected URL: $url" >&2; exit 99;;
esac
`)
			args := []string{script}
			if scenario == "pinned" {
				args = append(args, "v0.4.0")
			}
			if scenario == "invalid-version" {
				args = append(args, "../../evil")
			}
			cmd := exec.Command("bash", args...)
			cmd.Env = append(os.Environ(), "HOME="+home, "VPSCTL_INSTALL_DIR="+dest, "SCENARIO="+scenario)
			out, err := cmd.CombinedOutput()
			good := scenario == "latest" || scenario == "pinned"
			if good {
				if err != nil || managementRead(t, target) != payload {
					t.Fatalf("install: %s %v", out, err)
				}
				st, e := os.Stat(target)
				if e != nil || st.Mode().Perm() != 0755 {
					t.Fatalf("mode: %v %v", st, e)
				}
				requests := managementRead(t, filepath.Join(home, "requests"))
				if strings.Contains(requests, "/latest/download") {
					t.Fatal("release was not pinned")
				}
				if scenario == "pinned" && strings.Contains(requests, "/latest") {
					t.Fatal("pinned install queried latest")
				}
			} else {
				if err == nil {
					t.Fatalf("accepted %s: %s", scenario, out)
				}
				if scenario != "directory" && scenario != "symlink" && managementRead(t, target) != "old CLI" {
					t.Fatal("failure replaced old CLI")
				}
				if scenario == "directory" {
					entries, e := os.ReadDir(target)
					if e != nil || len(entries) != 0 {
						t.Fatal("directory modified")
					}
				}
				if scenario == "symlink" {
					st, e := os.Lstat(target)
					if e != nil || st.Mode()&os.ModeSymlink == 0 || managementRead(t, fixture) != payload {
						t.Fatal("symlink modified")
					}
				}
			}
			leftovers, e := filepath.Glob(filepath.Join(dest, ".vpsctl-install.*"))
			if e != nil || len(leftovers) != 0 {
				t.Fatalf("staging leftovers: %v %v", leftovers, e)
			}
			for _, p := range []string{".agents", ".pi", ".zshrc", ".bashrc"} {
				if _, e := os.Stat(filepath.Join(home, p)); !os.IsNotExist(e) {
					t.Fatalf("installer touched %s", p)
				}
			}
		})
	}
}
