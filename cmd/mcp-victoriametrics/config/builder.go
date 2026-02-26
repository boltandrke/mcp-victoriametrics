package config

import (
	"fmt"
	"net/url"
)

// ClusterConfig holds the parameters needed to construct a Config for a single cluster.
type ClusterConfig struct {
	Entrypoint    string `json:"entrypoint" yaml:"entrypoint"`
	InstanceType  string `json:"instance_type" yaml:"instance_type"`
	BearerToken   string `json:"bearer_token,omitempty" yaml:"bearer_token,omitempty"`
	DefaultTenant string `json:"default_tenant,omitempty" yaml:"default_tenant,omitempty"`
	GrafanaURL    string `json:"grafana_url,omitempty" yaml:"grafana_url,omitempty"`
	VmalertURL    string `json:"vmalert_url,omitempty" yaml:"vmalert_url,omitempty"`
}

// NewConfig creates a Config from explicit parameters, without reading env vars.
// This is used by the multi-cluster proxy to construct one Config per cluster.
func NewConfig(cc ClusterConfig) (*Config, error) {
	if cc.Entrypoint == "" {
		return nil, fmt.Errorf("entrypoint is required")
	}
	if cc.InstanceType == "" {
		cc.InstanceType = "single"
	}
	if cc.InstanceType != "single" && cc.InstanceType != "cluster" {
		return nil, fmt.Errorf("instance_type must be 'single' or 'cluster', got %q", cc.InstanceType)
	}

	u, err := url.Parse(cc.Entrypoint)
	if err != nil {
		return nil, fmt.Errorf("failed to parse entrypoint URL: %w", err)
	}

	cfg := &Config{
		serverMode:    "stdio",
		entrypoint:    cc.Entrypoint,
		instanceType:  cc.InstanceType,
		bearerToken:   cc.BearerToken,
		disabledTools: make(map[string]bool),
		customHeaders: make(map[string]string),
		entryPointURL: u,
	}

	if cc.VmalertURL != "" {
		vu, err := url.Parse(cc.VmalertURL)
		if err != nil {
			return nil, fmt.Errorf("failed to parse vmalert URL: %w", err)
		}
		cfg.vmalertURL = vu
	}

	return cfg, nil
}
