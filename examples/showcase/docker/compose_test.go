package docker_test

import (
	"context"
	"encoding/json"
	"os/exec"
	"testing"
	"time"
)

// Compose resolves YAML anchors and validates limits without contacting a daemon.
// Docker is optional for native Go tests and absent from the image build stage.
func TestComposeResourceBudgets(t *testing.T) {
	docker, err := exec.LookPath("docker")
	if err != nil {
		t.Skip("Docker CLI is not installed")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := exec.CommandContext(ctx, docker, "compose", "version").Run(); err != nil {
		t.Skip("Docker Compose is not available")
	}
	output, err := exec.CommandContext(ctx, docker, "compose", "-f", "../compose.yaml", "config", "--format", "json").Output()
	if err != nil {
		t.Fatalf("validate Compose configuration: %v", err)
	}
	var config struct {
		Services map[string]struct {
			CPUs       float64     `json:"cpus"`
			Memory     json.Number `json:"mem_limit"`
			MemorySwap json.Number `json:"memswap_limit"`
			PIDs       int         `json:"pids_limit"`
			Tmpfs      []string    `json:"tmpfs"`
		}
	}
	if err := json.Unmarshal(output, &config); err != nil {
		t.Fatalf("decode Compose configuration: %v", err)
	}
	want := map[string]struct {
		cpus   float64
		memory int64
		pids   int
		app    bool
	}{
		"postgres":   {0.75, 512 << 20, 128, false},
		"redis":      {0.25, 128 << 20, 64, false},
		"web":        {0.50, 256 << 20, 128, true},
		"worker":     {0.50, 256 << 20, 128, true},
		"setup":      {0.50, 256 << 20, 128, true},
		"initialize": {0.50, 256 << 20, 128, true},
	}
	if len(config.Services) != len(want) {
		t.Fatalf("all services must have reviewed budgets: got %d services, want %d", len(config.Services), len(want))
	}
	for name, budget := range want {
		t.Run(name, func(t *testing.T) {
			service, ok := config.Services[name]
			if !ok {
				t.Fatal("service is missing")
			}
			memory, memoryErr := service.Memory.Int64()
			memorySwap, swapErr := service.MemorySwap.Int64()
			if memoryErr != nil || swapErr != nil {
				t.Fatal("Compose must resolve both memory limits to bytes")
			}
			if service.CPUs != budget.cpus || memory != budget.memory || memorySwap != budget.memory || service.PIDs != budget.pids {
				t.Errorf("unexpected limits: CPUs=%g RAM=%d RAM+swap=%d PIDs=%d", service.CPUs, memory, memorySwap, service.PIDs)
			}
			if budget.app && (len(service.Tmpfs) != 1 || service.Tmpfs[0] != "/tmp:size=32m") {
				t.Error("application temporary storage must be bounded")
			}
		})
	}
}
