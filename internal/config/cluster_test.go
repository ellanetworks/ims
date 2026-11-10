package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

const (
	validConfig = validDB + validAPI + validSIP + validDiameter
	testSecret  = "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=\n"
)

func cluster(t *testing.T, lines string) string {
	t.Helper()

	return validConfig + "cluster:\n" + lines + "  secret_file: " + writeSecret(t, testSecret) + "\n"
}

func writeSecret(t *testing.T, content string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	return path
}

func TestLoadCluster(t *testing.T) {
	cfg, err := Load(writeConfig(t, cluster(t, "  address: 10.0.0.1\n")))
	if err != nil {
		t.Fatal(err)
	}

	c := cfg.Cluster
	if c == nil || c.Port != defaultClusterPort || c.AdvertiseAddress() != "10.0.0.1:7000" {
		t.Fatalf("cluster = %+v", c)
	}

	cfg, err = Load(writeConfig(t, cluster(t, "  address: 0.0.0.0\n  advertise: ims-0.ims:7000\n")))
	if err != nil || cfg.Cluster.AdvertiseAddress() != "ims-0.ims:7000" {
		t.Fatalf("cluster = %+v, %v", cfg.Cluster, err)
	}

	for name, block := range map[string]string{
		"no address":             "  port: 7000\n",
		"unspecified, no advert": "  address: 0.0.0.0\n",
		"advertise without port": "  address: 10.0.0.1\n  advertise: ims-0\n",
		"unspecified advertise":  "  address: 10.0.0.1\n  advertise: 0.0.0.0:7000\n",
		"expect of 1":            "  address: 10.0.0.1\n  join: [ims:7000]\n  bootstrap_expect: 1\n",
		"negative expect":        "  address: 10.0.0.1\n  join: [ims:7000]\n  bootstrap_expect: -3\n",
		"expect without join":    "  address: 10.0.0.1\n  bootstrap_expect: 3\n",
		"join with a bad port":   "  address: 10.0.0.1\n  join: [ims:70000]\n",
	} {
		if _, err := Load(writeConfig(t, cluster(t, block))); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestLoadClusterJoin(t *testing.T) {
	cfg, err := Load(writeConfig(t, cluster(t,
		"  address: 0.0.0.0\n  advertise: ims-0.ims:7000\n  port: 7001\n  join: [ims.ella.svc:7000, 10.0.0.2]\n  bootstrap_expect: 3\n")))
	if err != nil {
		t.Fatal(err)
	}

	if got, want := cfg.Cluster.JoinAddresses(), []string{"ims.ella.svc:7000", "10.0.0.2:7001"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("join = %v, want %v", got, want)
	}

	if cfg.Cluster.BootstrapExpect != 3 || len(cfg.Warnings()) != 0 {
		t.Fatalf("bootstrap_expect = %d, warnings %v", cfg.Cluster.BootstrapExpect, cfg.Warnings())
	}

	for expect, warns := range map[int]int{2: 1, 4: 1, 5: 0, 6: 2, 7: 1} {
		cfg, err := Load(writeConfig(t, cluster(t, "  address: 10.0.0.1\n  join: [ims:7000]\n  bootstrap_expect: "+
			strconv.Itoa(expect)+"\n")))
		if err != nil {
			t.Fatal(err)
		}

		if got := len(cfg.Warnings()); got != warns {
			t.Errorf("bootstrap_expect %d: warnings %v, want %d", expect, cfg.Warnings(), warns)
		}
	}
}

func TestLoadClusterSecret(t *testing.T) {
	if _, err := Load(writeConfig(t, validConfig+"cluster:\n  address: 10.0.0.1\n")); err != nil {
		t.Fatalf("Load without join and secret_file = %v", err)
	}

	if _, err := Load(writeConfig(t, validConfig+"cluster:\n  address: 10.0.0.1\n  join: [ims:7000]\n")); err == nil ||
		!strings.Contains(err.Error(), "secret_file") {
		t.Fatalf("Load with join and without secret_file = %v", err)
	}

	for name, content := range map[string]string{
		"empty":      "\n\n",
		"short":      "c2hvcnQ=\n",
		"not base64": "not base64 at all!\n",
	} {
		block := validConfig + "cluster:\n  address: 10.0.0.1\n  secret_file: " + writeSecret(t, content) + "\n"
		if _, err := Load(writeConfig(t, block)); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}

	block := validConfig + "cluster:\n  address: 10.0.0.1\n  secret_file: " + filepath.Join(t.TempDir(), "missing") + "\n"
	if _, err := Load(writeConfig(t, block)); err == nil {
		t.Error("missing secret file: expected an error")
	}

	rotated := testSecret + "\n" + "ZmVkY2JhOTg3NjU0MzIxMGZlZGNiYTk4NzY1NDMyMTA=\n"
	block = validConfig + "cluster:\n  address: 10.0.0.1\n  secret_file: " + writeSecret(t, rotated) + "\n"

	if _, err := Load(writeConfig(t, block)); err != nil {
		t.Fatalf("two secrets: %v", err)
	}
}

func TestLoadAdvertiseFromEnvironment(t *testing.T) {
	t.Setenv(EnvClusterAdvertise, "ella-ims-2.ella-ims-internal:7000")

	cfg, err := Load(writeConfig(t, cluster(t, "  address: 0.0.0.0\n  advertise: ims-0.ims:7000\n")))
	if err != nil {
		t.Fatal(err)
	}

	if got := cfg.Cluster.AdvertiseAddress(); got != "ella-ims-2.ella-ims-internal:7000" {
		t.Fatalf("advertise = %s, want the environment's", got)
	}

	cfg, err = Load(writeConfig(t, cluster(t, "  address: 0.0.0.0\n")))
	if err != nil || cfg.Cluster.AdvertiseAddress() != "ella-ims-2.ella-ims-internal:7000" {
		t.Fatalf("cluster = %+v, %v", cfg.Cluster, err)
	}

	cfg, err = Load(writeConfig(t, validConfig))
	if err != nil || cfg.Cluster != nil {
		t.Fatalf("Load without a cluster section = %+v, %v, want it to ignore %s", cfg.Cluster, err, EnvClusterAdvertise)
	}

	if w := cfg.Warnings(); len(w) != 1 || !strings.Contains(w[0], EnvClusterAdvertise) {
		t.Fatalf("warnings = %v, want one naming %s", w, EnvClusterAdvertise)
	}

	t.Setenv(EnvClusterAdvertise, "ella-ims-2")

	_, err = Load(writeConfig(t, cluster(t, "  address: 0.0.0.0\n")))
	if err == nil || !strings.Contains(err.Error(), EnvClusterAdvertise) {
		t.Fatalf("Load = %v, want an error naming %s", err, EnvClusterAdvertise)
	}
}
