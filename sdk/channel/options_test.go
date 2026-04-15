package channel

import "testing"

func TestDefaultConfig_UsesDefaultBusSocketPath(t *testing.T) {
	t.Setenv("IPC_SOCKET_PATH", "")

	cfg := defaultConfig()

	if cfg.socketPath != "/var/run/claw/bus.sock" {
		t.Fatalf("default socketPath = %q, want %q", cfg.socketPath, "/var/run/claw/bus.sock")
	}
}

func TestDefaultConfig_UsesSocketPathFromEnv(t *testing.T) {
	t.Setenv("IPC_SOCKET_PATH", "/tmp/custom.sock")

	cfg := defaultConfig()

	if cfg.socketPath != "/tmp/custom.sock" {
		t.Fatalf("socketPath from env = %q, want %q", cfg.socketPath, "/tmp/custom.sock")
	}
}
