package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestKeyVerifyRejectsFlattenedJump(t *testing.T) {
	home := managementHome(t)
	config := filepath.Join(home, "config")
	managementWrite(t, config, "Host target\n HostName target.internal\n ProxyJump jump\n")
	identity := filepath.Join(home, "key")
	managementWrite(t, identity, "fixture")
	managementBinary(t, home, "ssh", `for arg do
 if [ "$arg" = -G ]; then printf 'host target\nhostname target.internal\nproxyjump jump\n'; exit 0; fi
done
exit 99
`)
	r := (App{Config: config}).Key(context.Background(), []string{"verify", "target", "--identity", identity})
	if r.Error == nil || r.Error.Code != "UNSUPPORTED_CONFIG" {
		t.Fatalf("unsafe flattened config: %+v", r)
	}
}

func TestKeyUncertainWriteKeepsRecoveryID(t *testing.T) {
	home := managementHome(t)
	managementBinary(t, home, "ssh-keygen", "exit 0\n")
	managementBinary(t, home, "ssh", "exit 255\n")
	pub := filepath.Join(home, "key.pub")
	managementWrite(t, pub, "ssh-ed25519 YWJj\n")
	r := (App{}).Key(context.Background(), []string{"add", "target", "--public-key", pub})
	data, ok := r.Data.(map[string]string)
	if r.Status != "unknown" || !ok || !backupIDPattern.MatchString(data["backup"]) {
		t.Fatalf("lost recovery ID: %+v", r)
	}
}

func TestNoPythonRuntimeDependency(t *testing.T) {
	for _, path := range []string{"pyproject.toml", "MANIFEST.in", "src/vpsctl/cli.py"} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("obsolete runtime remains: %s", path)
		}
	}
}
