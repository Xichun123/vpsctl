package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestTransferValidation(t *testing.T) {
	a := fakeSSH(t)
	for _, args := range [][]string{{"fixture", "relative", "/tmp/out"}, {"-bad", "/tmp/input", "/tmp/output"}, {"fixture", "/", "/tmp/output"}} {
		if r := a.Files(context.Background(), "download", args); r.Error == nil || r.Error.Code != "INVALID_ARGUMENT" {
			t.Fatal(r)
		}
	}
	if r := a.Transfer(context.Background(), []string{"fixture", "/tmp/a", "fixture", "/tmp/a"}); r.Error == nil {
		t.Fatal(r)
	}
}

func TestDownloadAtomicityAndChecksum(t *testing.T) {
	a := fakeSSH(t)
	root := t.TempDir()
	source := filepath.Join(root, "source ' $file")
	dest := filepath.Join(root, "download")
	content := []byte("hello\x00world\n")
	if err := os.WriteFile(source, content, 0600); err != nil {
		t.Fatal(err)
	}
	r := a.download(context.Background(), "fixture", source, dest, false)
	if !r.OK() {
		t.Fatalf("download: %+v", r)
	}
	got, err := os.ReadFile(dest)
	if err != nil || string(got) != string(content) {
		t.Fatalf("%q %v", got, err)
	}
	if r = a.download(context.Background(), "fixture", source, dest, false); r.Error == nil || r.Error.Code != "DESTINATION_EXISTS" {
		t.Fatal(r)
	}
	if r = a.download(context.Background(), "fixture", source+"-missing", dest, true); r.OK() {
		t.Fatal(r)
	}
	got, _ = os.ReadFile(dest)
	if string(got) != string(content) {
		t.Fatal("failed transfer overwrote destination")
	}
	st, _ := os.Stat(dest)
	if st.Mode().Perm() != 0600 {
		t.Fatal(st.Mode())
	}
}

func TestFileProbeIgnoresDisplayBudget(t *testing.T) {
	a := fakeSSH(t)
	root := t.TempDir()
	source := filepath.Join(root, "source")
	if err := os.WriteFile(source, []byte("payload"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, budget := range []string{"1", "32", "64"} {
		dest := filepath.Join(root, "download-"+budget)
		r := a.Run(context.Background(), []string{"--max-output", budget, "download", "fixture", source, dest})
		if !r.OK() {
			t.Fatalf("budget %s: %+v", budget, r)
		}
		got, err := os.ReadFile(dest)
		if err != nil || string(got) != "payload" {
			t.Fatalf("%q %v", got, err)
		}
		if runtime.GOOS == "linux" {
			r = a.Run(context.Background(), []string{"--max-output", budget, "transfer", "fixture", source, "other", dest + "-cross"})
			if !r.OK() {
				t.Fatalf("transfer budget %s: %+v", budget, r)
			}
		} else {
			// Reach the commit guard on non-GNU systems without invoking GNU ln.
			r = a.Run(context.Background(), []string{"--max-output", budget, "transfer", "fixture", source, "other", root})
			if r.OK() || r.Error != nil && r.Error.Code == "PROTOCOL_ERROR" {
				t.Fatalf("probe was truncated: %+v", r)
			}
		}
	}
}

func TestUploadRejectsDirectoryTarget(t *testing.T) {
	a := fakeSSH(t)
	root := t.TempDir()
	source, dest := filepath.Join(root, "source"), filepath.Join(root, "directory")
	if err := os.WriteFile(source, []byte("payload"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(dest, 0700); err != nil {
		t.Fatal(err)
	}
	for _, overwrite := range []bool{false, true} {
		r := a.upload(context.Background(), "fixture", source, dest, overwrite)
		if r.OK() || !strings.Contains(r.Stderr, "nonregular destination") {
			t.Fatalf("directory accepted: %+v", r)
		}
		args := []string{"fixture", source, "other", dest}
		if overwrite {
			args = append(args, "--overwrite")
		}
		if r = a.Transfer(context.Background(), args); r.OK() {
			t.Fatalf("transfer directory accepted: %+v", r)
		}
	}
	entries, err := os.ReadDir(dest)
	if err != nil || len(entries) != 0 {
		t.Fatalf("unexpected directory mutation: %v %v", entries, err)
	}
	if !strings.Contains(uploadScript(dest, "", 0, false), "ln -T --") {
		t.Fatal("commit must reject a directory created after validation")
	}
}

func TestUploadAndCrossHostStream(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("atomic upload helpers target Linux GNU coreutils")
	}
	a := fakeSSH(t)
	root := t.TempDir()
	source := filepath.Join(root, "source")
	dest := filepath.Join(root, "destination ' $name")
	cross := filepath.Join(root, "cross")
	if err := os.WriteFile(source, []byte("first"), 0600); err != nil {
		t.Fatal(err)
	}
	if r := a.upload(context.Background(), "fixture", source, dest, false); !r.OK() {
		t.Fatal(r)
	}
	if r := a.upload(context.Background(), "fixture", source, dest, false); r.OK() {
		t.Fatal("overwrite must be explicit")
	}
	if err := os.WriteFile(source, []byte("second"), 0600); err != nil {
		t.Fatal(err)
	}
	if r := a.upload(context.Background(), "fixture", source, dest, true); !r.OK() {
		t.Fatal(r)
	}
	if r := a.Transfer(context.Background(), []string{"fixture", source, "other", cross}); !r.OK() {
		t.Fatal(r)
	}
	got, _ := os.ReadFile(cross)
	if string(got) != "second" {
		t.Fatal(string(got))
	}
	// The remote commit must not happen for incomplete/mismatching input.
	expected := sha256.Sum256([]byte("complete"))
	r := a.SSH(context.Background(), "fixture", "sh -c "+quote(uploadScript(dest, hex.EncodeToString(expected[:]), 8, true)), strings.NewReader("short"))
	if r.OK() {
		t.Fatal("accepted truncated upload")
	}
	got, _ = os.ReadFile(dest)
	if string(got) != "second" {
		t.Fatal("corrupted destination")
	}
	leftovers, err := filepath.Glob(dest + ".vpsctl.*")
	if err != nil || len(leftovers) != 0 {
		t.Fatal(leftovers, err)
	}
}
