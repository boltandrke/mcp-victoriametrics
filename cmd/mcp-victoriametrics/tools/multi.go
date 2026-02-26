package tools

import (
	"context"
	"fmt"
	"sort"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/VictoriaMetrics-Community/mcp-victoriametrics/cmd/mcp-victoriametrics/config"
)

// clusterToolHandler is the standard handler signature used by most tools.
type clusterToolHandler func(ctx context.Context, cfg *config.Config, tcr mcp.CallToolRequest) (*mcp.CallToolResult, error)

// clusterToolEntry describes a tool that needs per-cluster config routing.
type clusterToolEntry struct {
	name    string
	build   func(c *config.Config) mcp.Tool
	handler clusterToolHandler
}

// clusterTools returns all tools that require per-cluster routing.
// Cloud-only tools (tiers, regions, deployments, access_tokens, etc.) are excluded.
func clusterTools() []clusterToolEntry {
	return []clusterToolEntry{
		{toolNameQuery, toolQuery, toolQueryHandler},
		{toolNameQueryRange, toolQueryRange, toolQueryRangeHandler},
		{toolNameLabels, toolLabels, toolLabelsHandler},
		{toolNameLabelValues, toolLabelsValues, toolLabelValuesHandler},
		{toolNameSeries, toolSeries, toolSeriesHandler},
		{toolNameMetrics, toolMetrics, toolMetricsHandler},
		{toolNameMetricsMetadata, toolMetricsMetadata, toolMetricsMetadataHandler},
		{toolNameMetricStats, toolMetricStats, toolMetricStatsHandler},
		{toolNameTSDBStatus, toolTSDBStatus, toolTSDBStatusHandler},
		{toolNameAlerts, toolAlerts, toolAlertsHandler},
		{toolNameRules, toolRules, toolRulesHandler},
		{toolNameFlags, toolFlags, toolFlagsHandler},
		{toolNameExport, toolExport, toolExportHandler},
		{toolNameTopQueries, toolTopQueries, toolTopQueriesHandler},
		{toolNameActiveQueries, toolActiveQueries, toolActiveQueriesHandler},
		// toolExplainQuery is a package-level var, wrap it as a builder function
		{toolNameExplainQuery, func(_ *config.Config) mcp.Tool { return toolExplainQuery }, toolExplainQueryHandler},
		{toolNamePrettifyQuery, toolPrettifyQuery, toolPrettifyQueryHandler},
		{toolNameMetricRelabelDebug, toolMetricRelabelDebug, toolMetricRelabelDebugHandler},
		{toolNameRetentionFiltersDebug, toolRetentionFiltersDebug, toolRetentionFiltersDebugHandler},
		{toolNameDownsamplingFiltersDebug, toolDownsamplingFiltersDebug, toolDownsamplingFiltersDebugHandler},
		{toolNameTenants, toolTenants, toolTenantsHandler},
	}
}

// addClusterParam returns a copy of the tool with a required `cluster` enum parameter prepended.
func addClusterParam(t mcp.Tool, clusterNames []string) mcp.Tool {
	// Build the enum property as a JSON Schema object
	enumProp := map[string]any{
		"type":        "string",
		"title":       "Cluster",
		"description": "Name of the TiDB cluster to query",
		"enum":        clusterNames,
	}

	// Copy properties and add cluster
	newProps := make(map[string]any, len(t.InputSchema.Properties)+1)
	newProps["cluster"] = enumProp
	for k, v := range t.InputSchema.Properties {
		newProps[k] = v
	}

	// Copy required and prepend cluster
	newRequired := make([]string, 0, len(t.InputSchema.Required)+1)
	newRequired = append(newRequired, "cluster")
	newRequired = append(newRequired, t.InputSchema.Required...)

	t.InputSchema.Properties = newProps
	t.InputSchema.Required = newRequired
	return t
}

// RegisterMultiClusterTools registers all cluster-aware tools with an added `cluster` enum
// parameter. The handler resolves the cluster name to the correct *config.Config before
// delegating to the original tool handler.
//
// Config-independent tools (documentation, test_rules) are registered once without a cluster param.
func RegisterMultiClusterTools(s *server.MCPServer, clusters map[string]*config.Config) {
	clusterNames := make([]string, 0, len(clusters))
	for name := range clusters {
		clusterNames = append(clusterNames, name)
	}
	sort.Strings(clusterNames)

	// Use first cluster config for building tool definitions (schema is identical across clusters).
	firstCfg := clusters[clusterNames[0]]

	// Initialize embedded data required by explain_query tool.
	if !firstCfg.IsToolDisabled(toolNameExplainQuery) {
		if err := initFunctionsInfo(); err != nil {
			panic(fmt.Sprintf("error initializing functions info: %s", err))
		}
		if err := initMetricsInfo(); err != nil {
			panic(fmt.Sprintf("error initializing metrics info: %s", err))
		}
	}

	for _, entry := range clusterTools() {
		if firstCfg.IsToolDisabled(entry.name) {
			continue
		}

		baseTool := entry.build(firstCfg)
		multiTool := addClusterParam(baseTool, clusterNames)

		// Capture handler for the closure
		h := entry.handler
		s.AddTool(multiTool, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			clusterName, err := GetToolReqParam[string](request, "cluster", true)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			cfg, ok := clusters[clusterName]
			if !ok {
				return mcp.NewToolResultError(fmt.Sprintf("unknown cluster %q, available: %v", clusterName, clusterNames)), nil
			}
			return h(ctx, cfg, request)
		})
	}

	// Register config-independent tools once (no per-cluster routing needed).
	RegisterToolDocumentation(s, firstCfg)
	RegisterToolTestRules(s, firstCfg)
}
