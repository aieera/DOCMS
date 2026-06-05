// Package archtest contains architecture-level assertions that run
// under plain `go test` and gate the build. No runtime deps allowed.
package archtest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// TestGatewayRoutesMirrorKongConfig pins the B2.1 contract: every
// public route declared in deploy/gateway/routes.yaml must be served
// by a route in deploy/gateway/kong.yaml with the same path prefix.
// Adding a new route to one file without the other fails the build,
// which prevents "I forgot to wire it through the gateway" bugs.
func TestGatewayRoutesMirrorKongConfig(t *testing.T) {
	repoRoot := findRepoRoot(t)

	routes := loadRoutesYAML(t, filepath.Join(repoRoot, "deploy", "gateway", "routes.yaml"))
	kongPaths := loadKongRoutePaths(t, filepath.Join(repoRoot, "deploy", "gateway", "kong.yaml"))

	var missing []string
	for _, r := range routes.Routes {
		found := false
		for _, p := range kongPaths {
			// Either an exact match or the Kong path is a parent prefix
			// (e.g. /api/v1/auth/mfa covers /api/v1/auth/mfa/setup).
			if p == r.Prefix || strings.HasPrefix(r.Prefix, p+"/") {
				found = true
				break
			}
		}
		if !found {
			missing = append(missing, r.Prefix+" (service="+r.Service+", auth="+r.Auth+")")
		}
	}

	if len(missing) > 0 {
		t.Fatalf("routes.yaml declares %d route(s) not present in kong.yaml:\n  %s",
			len(missing), strings.Join(missing, "\n  "))
	}
}

// TestKongRoutesDeclaredInRoutesYAML is the reverse contract: every route
// path in deploy/gateway/kong.yaml must also be declared (with its auth
// level) in deploy/gateway/routes.yaml. Together with the forward check
// above this keeps the gateway config and the source-of-truth inventory in
// exact lockstep — a route can't be added to one file without the other.
//
// This is the half that was missing when ~21 routes drifted: kong.yaml had
// been edited without routes.yaml (and vice-versa) and nothing caught it.
func TestKongRoutesDeclaredInRoutesYAML(t *testing.T) {
	repoRoot := findRepoRoot(t)

	routes := loadRoutesYAML(t, filepath.Join(repoRoot, "deploy", "gateway", "routes.yaml"))
	kongPaths := loadKongRoutePaths(t, filepath.Join(repoRoot, "deploy", "gateway", "kong.yaml"))

	var undeclared []string
	for _, kp := range kongPaths {
		found := false
		for _, r := range routes.Routes {
			// Declared if routes.yaml has: an exact match; a parent prefix
			// covering this Kong path; or a child prefix (Kong groups several
			// sub-routes under one broad route, e.g. /auth/mfa covers
			// /auth/mfa/verify+/setup which routes.yaml enumerates).
			if r.Prefix == kp ||
				strings.HasPrefix(kp, r.Prefix+"/") ||
				strings.HasPrefix(r.Prefix, kp+"/") {
				found = true
				break
			}
		}
		if !found {
			undeclared = append(undeclared, kp)
		}
	}

	if len(undeclared) > 0 {
		t.Fatalf("kong.yaml declares %d route path(s) not present in routes.yaml:\n  %s",
			len(undeclared), strings.Join(undeclared, "\n  "))
	}
}

// ---- helpers --------------------------------------------------------

type routesDoc struct {
	Routes []struct {
		Service string   `yaml:"service"`
		Prefix  string   `yaml:"prefix"`
		Methods []string `yaml:"methods"`
		Auth    string   `yaml:"auth"`
		Rate    string   `yaml:"rate"`
	} `yaml:"routes"`
}

func loadRoutesYAML(t *testing.T, path string) routesDoc {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read routes.yaml: %v", err)
	}
	var d routesDoc
	if err := yaml.Unmarshal(data, &d); err != nil {
		t.Fatalf("parse routes.yaml: %v", err)
	}
	if len(d.Routes) == 0 {
		t.Fatal("routes.yaml: zero routes — fixture or parse error")
	}
	return d
}

type kongDoc struct {
	Services []struct {
		Name   string `yaml:"name"`
		URL    string `yaml:"url"`
		Routes []struct {
			Name    string   `yaml:"name"`
			Paths   []string `yaml:"paths"`
			Methods []string `yaml:"methods"`
		} `yaml:"routes"`
	} `yaml:"services"`
}

func loadKongRoutePaths(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read kong.yaml: %v", err)
	}
	var d kongDoc
	if err := yaml.Unmarshal(data, &d); err != nil {
		t.Fatalf("parse kong.yaml: %v", err)
	}
	var paths []string
	for _, s := range d.Services {
		for _, r := range s.Routes {
			paths = append(paths, r.Paths...)
		}
	}
	if len(paths) == 0 {
		t.Fatal("kong.yaml: zero route paths — fixture or parse error")
	}
	return paths
}

// findRepoRoot walks up from the test binary's directory until it
// finds go.work, which anchors the SeDoc monorepo.
func findRepoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 10; i++ {
		if _, err := os.Stat(filepath.Join(dir, "go.work")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	t.Fatal("could not find go.work walking up from cwd")
	return ""
}
