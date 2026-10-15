package build

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInsightServiceConfiguration(t *testing.T) {
	for _, tc := range []struct {
		name, port, binding string
		invalid             bool
	}{
		{"disabled", "", "", false},
		{"enabled", `,"blockbook_insight":9876`, `,"insight_binding_template":"127.0.0.1:{{.Ports.BlockbookInsight}}"`, false},
		{"port only", `,"blockbook_insight":9876`, "", true},
		{"template only", "", `,"insight_binding_template":"127.0.0.1:9876"`, true},
		{"bad port", `,"blockbook_insight":65536`, `,"insight_binding_template":"127.0.0.1:{{.Ports.BlockbookInsight}}"`, true},
		{"host required", `,"blockbook_insight":9876`, `,"insight_binding_template":":{{.Ports.BlockbookInsight}}"`, true},
		{"wrong port", `,"blockbook_insight":9876`, `,"insight_binding_template":"127.0.0.1:1234"`, true},
		{"invalid template", `,"blockbook_insight":9876`, `,"insight_binding_template":"{{broken"`, true},
		{"unsafe whitespace", `,"blockbook_insight":9876`, `,"insight_binding_template":"127.0.0.1:9876 -public=0.0.0.0:80"`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.Mkdir(filepath.Join(dir, "coins"), 0755); err != nil {
				t.Fatal(err)
			}
			coin := `{"coin":{"name":"Example","alias":"example"},"ports":{"blockbook_public":9130` + tc.port + `},"blockbook":{"package_name":"blockbook-example","public_binding_template":"127.0.0.1:{{.Ports.BlockbookPublic}}"` + tc.binding + `}}`
			if err := os.WriteFile(filepath.Join(dir, "coins", "example.json"), []byte(coin), 0644); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "environ.json"), []byte(`{"blockbook_install_path":"/opt/coins"}`), 0644); err != nil {
				t.Fatal(err)
			}
			cfg, err := LoadConfig(dir, "example")
			if tc.invalid {
				if err == nil {
					t.Fatal("invalid Insight configuration accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			out := filepath.Join(dir, "out")
			if err = GeneratePackageDefinitions(cfg, "../templates", out); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(filepath.Join(out, "blockbook/debian/service"))
			if err != nil {
				t.Fatal(err)
			}
			if tc.name == "enabled" {
				if !strings.Contains(string(data), " -insight=127.0.0.1:9876 ") {
					t.Fatalf("Insight flag missing from actual service: %s", data)
				}
			} else if strings.Contains(string(data), "-insight") {
				t.Fatal("Insight enabled by default")
			}
		})
	}
}
