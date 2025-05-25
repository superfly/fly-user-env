package tests

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"supervisor/lib"
)

// SetupConfiguredControlServer creates a test directory, instantiates a Control with the specified stacks, starts an httptest.Server, and calls the config endpoint with the provided config.
func SetupConfiguredControlServer(t *testing.T, stacks []string, dir string) (*httptest.Server, *lib.Control) {
	config := &lib.ObjectStorageConfig{
		Bucket:    os.Getenv("FLY_TIGRIS_BUCKET"),
		Endpoint:  os.Getenv("FLY_TIGRIS_ENDPOINT_URL"),
		AccessKey: os.Getenv("FLY_TIGRIS_ACCESS_KEY"),
		SecretKey: os.Getenv("FLY_TIGRIS_SECRET_ACCESS_KEY"),
		Region:    "auto",
		KeyPrefix: "test-control-checkpoints/",
	}

	// Create components based on requested stacks
	var components []lib.StackComponent
	for _, stack := range stacks {
		switch stack {
		case "db":
			components = append(components, lib.NewDBManagerComponent(dir))
		case "leaser":
			components = append(components, lib.NewLeaserComponent())
		case "juicefs":
			components = append(components, lib.NewJuiceFSComponent())
		}
	}

	control := lib.NewControl(
		"localhost:8080",
		"localhost:8080",
		"test-token",
		dir,
		nil, // supervisor not needed for this test
		components...,
	)

	cfg := &lib.SystemConfig{
		Storage: *config,
		Stacks:  stacks,
	}

	server := httptest.NewServer(control)

	cfgData, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("Failed to marshal config: %v", err)
	}

	req, err := http.NewRequest("POST", server.URL, bytes.NewBuffer(cfgData))
	if err != nil {
		t.Fatalf("Failed to create request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer test-token")
	req.Host = "fly-app-controller"

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Failed to send request: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("Failed to configure control: %v", resp.Status)
	}

	return server, control
}
