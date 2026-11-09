package config

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"strconv"

	"github.com/ellanetworks/ims/internal/cluster/pki"
)

const (
	defaultClusterPort  = 7000
	EnvClusterAdvertise = "IMS_CLUSTER_ADVERTISE"
)

type Cluster struct {
	Address         netip.Addr `yaml:"address"`
	Port            int        `yaml:"port"`
	Advertise       string     `yaml:"advertise"`
	Join            []string   `yaml:"join"`
	BootstrapExpect int        `yaml:"bootstrap_expect"`
	SecretFile      string     `yaml:"secret_file"`
}

func (c Cluster) JoinAddresses() []string {
	out := make([]string, 0, len(c.Join))

	for _, j := range c.Join {
		if _, _, err := net.SplitHostPort(j); err != nil {
			j = net.JoinHostPort(j, strconv.Itoa(c.Port))
		}

		out = append(out, j)
	}

	return out
}

func (c Cluster) AdvertiseAddress() string {
	if c.Advertise != "" {
		return c.Advertise
	}

	return netip.AddrPortFrom(c.Address, uint16(c.Port)).String()
}

func (c *Config) applyClusterEnv() {
	v := os.Getenv(EnvClusterAdvertise)
	if v == "" {
		return
	}

	if c.Cluster == nil {
		c.warnings = append(c.warnings, EnvClusterAdvertise+" is set, but the configuration has no cluster section: ignoring it")
		return
	}

	c.Cluster.Advertise = v
}

func (c Config) Warnings() []string {
	out := append([]string(nil), c.warnings...)

	if cl := c.Cluster; cl != nil {
		switch {
		case cl.BootstrapExpect == 2:
			out = append(out, "cluster.bootstrap_expect is 2: a cluster of 2 voters tolerates no failure")
		case cl.BootstrapExpect > 2 && cl.BootstrapExpect%2 == 0:
			out = append(out, fmt.Sprintf("cluster.bootstrap_expect is %d, an even number: it tolerates no more failures than %d",
				cl.BootstrapExpect, cl.BootstrapExpect-1))
		}

		if cl.BootstrapExpect > maxClusterNodes {
			out = append(out, fmt.Sprintf("cluster.bootstrap_expect is %d: a cluster has at most %d nodes",
				cl.BootstrapExpect, maxClusterNodes))
		}
	}

	return out
}

const maxClusterNodes = 5

func advertiseSource() string {
	if os.Getenv(EnvClusterAdvertise) != "" {
		return EnvClusterAdvertise
	}

	return "cluster.advertise"
}

func (c Cluster) validate() error {
	switch {
	case !c.Address.IsValid():
		return errors.New("cluster.address is required")
	case c.Port < 1 || c.Port > 65535:
		return fmt.Errorf("cluster.port %d is out of range", c.Port)
	case c.Advertise == "" && c.Address.IsUnspecified():
		return errors.New("cluster.advertise is required when cluster.address is unspecified")
	}

	if err := c.validateJoin(); err != nil {
		return err
	}

	if len(c.Join) > 0 && c.SecretFile == "" {
		return errors.New("cluster.secret_file is required with cluster.join")
	}

	if c.SecretFile != "" {
		if _, err := pki.ReadSecrets(c.SecretFile); err != nil {
			return fmt.Errorf("cluster.secret_file: %w", err)
		}
	}

	if c.Advertise == "" {
		return nil
	}

	host, port, err := net.SplitHostPort(c.Advertise)
	if err != nil || host == "" || port == "" {
		return fmt.Errorf("%s %q is not a host:port", advertiseSource(), c.Advertise)
	}

	if ip, err := netip.ParseAddr(host); err == nil && ip.IsUnspecified() {
		return fmt.Errorf("%s %q must be a specific address", advertiseSource(), c.Advertise)
	}

	return nil
}

func (c Cluster) validateJoin() error {
	switch {
	case c.BootstrapExpect < 0 || c.BootstrapExpect == 1:
		return fmt.Errorf("cluster.bootstrap_expect %d must be at least 2, or unset", c.BootstrapExpect)
	case c.BootstrapExpect > 0 && len(c.Join) == 0:
		return errors.New("cluster.bootstrap_expect requires cluster.join")
	}

	for _, j := range c.JoinAddresses() {
		host, port, err := net.SplitHostPort(j)
		if err != nil || host == "" {
			return fmt.Errorf("cluster.join %q is not a host or host:port", j)
		}

		if p, err := strconv.Atoi(port); err != nil || p < 1 || p > 65535 {
			return fmt.Errorf("cluster.join %q has an invalid port", j)
		}
	}

	return nil
}
