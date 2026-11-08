package main

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"
	"time"

	"github.com/ellanetworks/ims/internal/server"
	"gopkg.in/yaml.v3"
)

const stopMargin = 15 * time.Second

func readRepoFile(t *testing.T, name string) []byte {
	t.Helper()

	b, err := os.ReadFile(filepath.Join("..", "..", name))
	if err != nil {
		t.Fatal(err)
	}

	return b
}

func parseDuration(t *testing.T, s string) time.Duration {
	t.Helper()

	d, err := time.ParseDuration(s)
	if err != nil {
		t.Fatal(err)
	}

	return d
}

func TestPlatformsWaitForTheShutdown(t *testing.T) {
	want := server.ShutdownTimeout + stopMargin

	var snap struct {
		Apps map[string]struct {
			StopTimeout string `yaml:"stop-timeout"`
		} `yaml:"apps"`
	}

	if err := yaml.Unmarshal(readRepoFile(t, "snap/snapcraft.yaml"), &snap); err != nil {
		t.Fatal(err)
	}

	var rock struct {
		Services map[string]struct {
			KillDelay string `yaml:"kill-delay"`
		} `yaml:"services"`
	}

	if err := yaml.Unmarshal(readRepoFile(t, "rockcraft.yaml"), &rock); err != nil {
		t.Fatal(err)
	}

	var statefulSet struct {
		Spec struct {
			Template struct {
				Spec struct {
					TerminationGracePeriodSeconds int `yaml:"terminationGracePeriodSeconds"`
				} `yaml:"spec"`
			} `yaml:"template"`
		} `yaml:"spec"`
	}

	if err := yaml.Unmarshal(readRepoFile(t, "k8s/base/ims-statefulset.yaml"), &statefulSet); err != nil {
		t.Fatal(err)
	}

	compose := regexp.MustCompile(`stop_grace_period: (\S+)`).FindSubmatch(readRepoFile(t, "docs/how_to/install.md"))
	if compose == nil {
		t.Fatal("docs/how_to/install.md: no stop_grace_period in the Docker Compose file")
	}

	got := map[string]time.Duration{
		"snap/snapcraft.yaml stop-timeout": parseDuration(t, snap.Apps["imsd"].StopTimeout),
		"rockcraft.yaml kill-delay":        parseDuration(t, rock.Services["ella-ims"].KillDelay),
		"k8s/base/ims-statefulset.yaml terminationGracePeriodSeconds": time.Duration(
			statefulSet.Spec.Template.Spec.TerminationGracePeriodSeconds) * time.Second,
		"docs/how_to/install.md stop_grace_period": parseDuration(t, string(compose[1])),
	}

	for name, d := range got {
		if d != want {
			t.Errorf("%s is %s, want %s", name, d, want)
		}
	}
}
