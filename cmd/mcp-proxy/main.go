package main

import (
	"encoding/json"
	"fmt"
	"log"
	"log/slog"
	"os"
	"sort"

	"github.com/mark3labs/mcp-go/server"

	"github.com/VictoriaMetrics-Community/mcp-victoriametrics/cmd/mcp-victoriametrics/config"
	"github.com/VictoriaMetrics-Community/mcp-victoriametrics/cmd/mcp-victoriametrics/resources"
	"github.com/VictoriaMetrics-Community/mcp-victoriametrics/cmd/mcp-victoriametrics/tools"
)

var (
	version = "dev"
	date    = "unknown"
)

// ClustersFile is the on-disk format for the clusters configuration.
type ClustersFile struct {
	Clusters        map[string]config.ClusterConfig `json:"clusters"`
	IgnoredClusters []string                        `json:"ignored_clusters,omitempty"`
	Grafana         GrafanaConfig                   `json:"grafana,omitempty"`
}

// GrafanaConfig holds Grafana-specific settings (dashboard UIDs, datasource, etc.)
// These are deployment-specific and should be configured in clusters.json, not hardcoded.
type GrafanaConfig struct {
	DatasourceUID  string            `json:"datasource_uid,omitempty"`
	DashboardFolder string           `json:"dashboard_folder,omitempty"`
	Dashboards     map[string]string `json:"dashboards,omitempty"` // name → UID
}

func main() {
	configPath := os.Getenv("MCP_PROXY_CONFIG")
	if configPath == "" {
		configPath = "clusters.json"
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		log.Fatalf("FATAL: failed to read config %s: %v", configPath, err)
	}

	var cf ClustersFile
	if err := json.Unmarshal(data, &cf); err != nil {
		log.Fatalf("FATAL: failed to parse config %s: %v", configPath, err)
	}
	if len(cf.Clusters) == 0 {
		log.Fatal("FATAL: no clusters defined in config")
	}

	// Build ignored set
	ignored := make(map[string]bool, len(cf.IgnoredClusters))
	for _, name := range cf.IgnoredClusters {
		ignored[name] = true
	}

	// Build per-cluster configs (skip ignored)
	clusters := make(map[string]*config.Config, len(cf.Clusters))
	for name, cc := range cf.Clusters {
		if ignored[name] {
			continue
		}
		cfg, err := config.NewConfig(cc)
		if err != nil {
			log.Fatalf("FATAL: cluster %q: %v", name, err)
		}
		clusters[name] = cfg
	}
	if len(cf.IgnoredClusters) > 0 {
		slog.Info("Ignored clusters", "ignored", cf.IgnoredClusters)
	}

	// Log cluster list
	names := make([]string, 0, len(clusters))
	for n := range clusters {
		names = append(names, n)
	}
	sort.Strings(names)
	slog.Info("MCP proxy starting",
		"version", version,
		"date", date,
		"clusters", len(clusters),
	)
	for _, n := range names {
		slog.Info("  cluster registered", "name", n, "entrypoint", cf.Clusters[n].Entrypoint, "grafana", cf.Clusters[n].GrafanaURL)
	}

	s := server.NewMCPServer(
		"VictoriaMetrics Multi-Cluster Proxy",
		fmt.Sprintf("v%s (date: %s)", version, date),
		server.WithRecovery(),
		server.WithLogging(),
		server.WithToolCapabilities(false),
		server.WithResourceCapabilities(false, false),
		server.WithPromptCapabilities(false),
		server.WithInstructions(buildInstructions(names, cf)),
	)

	// Register docs resources (shared, not per-cluster)
	resources.RegisterDocsResources(s, clusters[names[0]])

	// Register all tools with multi-cluster routing
	tools.RegisterMultiClusterTools(s, clusters)

	slog.Info("Serving via stdio")
	if err := server.ServeStdio(s); err != nil {
		slog.Error("failed to serve", "error", err)
		os.Exit(1)
	}
}

// buildInstructions generates the system instructions for the MCP server.
func buildInstructions(names []string, cf ClustersFile) string {
	// Build cluster → Grafana URL mapping for non-empty entries
	var grafanaLines string
	var firstGrafanaCluster, firstGrafanaURL string
	for _, n := range names {
		if u := cf.Clusters[n].GrafanaURL; u != "" {
			grafanaLines += fmt.Sprintf("  %s: %s\n", n, u)
			if firstGrafanaCluster == "" {
				firstGrafanaCluster = n
				firstGrafanaURL = u
			}
		}
	}

	instructions := fmt.Sprintf(
		"You are a Virtual Assistant for querying multiple TiDB cluster monitoring instances via VictoriaMetrics.\n\n"+
			"Every tool that queries metrics requires a \"cluster\" parameter. Available clusters: %v\n\n"+
			"Use the Documentation tool to look up VictoriaMetrics-related information.\n"+
			"Always specify the cluster parameter when querying metrics.\n"+
			"You can compare metrics across clusters by making multiple tool calls with different cluster values.\n\n",
		names)

	instructions += "IMPORTANT — Query Efficiency:\n" +
		"Many TiDB metrics have high cardinality (hundreds of series per metric). Always use aggregation " +
		"in your queries to keep result sets small and readable:\n" +
		"  - Use sum(), count(), avg(), min(), max() with by() to aggregate across instances/stores.\n" +
		"  - Use topk(N, ...) to return only the top N series instead of all of them.\n" +
		"  - Use {type=~\"specific_type\"} label filters to narrow down multi-type metrics.\n" +
		"Examples of GOOD queries vs BAD queries:\n" +
		"  BAD:  pd_hotspot_status  (returns 500+ series across all types and stores)\n" +
		"  GOOD: sum(pd_hotspot_status{type=~\"hot_write_region_as_leader|hot_read_region_as_leader\"}) by (type)\n" +
		"  GOOD: topk(10, pd_hotspot_status{type=\"hot_write_region_as_leader\"})\n" +
		"  BAD:  tikv_raftstore_region_count  (returns one series per store)\n" +
		"  GOOD: sort_desc(tikv_raftstore_region_count)\n" +
		"  BAD:  tikv_engine_size_bytes  (returns many series per store per column family)\n" +
		"  GOOD: sum(tikv_engine_size_bytes) by (instance)\n\n" +
		"When investigating hotspots, always query aggregated totals first, then drill down to specific stores only if needed.\n\n"

	if grafanaLines != "" && len(cf.Grafana.Dashboards) > 0 {
		dsUID := cf.Grafana.DatasourceUID
		folder := cf.Grafana.DashboardFolder
		if folder == "" {
			folder = "TiDB"
		}

		instructions += "VISUALIZATION — Grafana Dashboards:\n" +
			fmt.Sprintf("Each cluster has a Grafana instance with pre-built TiDB dashboards in the '%s' folder.\n", folder) +
			"When the user asks for visualization, trends, or graphs, suggest a direct Grafana dashboard link.\n" +
			"Grafana URLs by cluster:\n" +
			grafanaLines +
			"\nDashboard deep-link pattern: {grafana_url}/d/{uid}/{cluster}-{slug}\n" +
			"The dashboard UIDs are consistent across all clusters. Available dashboards:\n"

		// Render dashboards from config (ordered by name for deterministic output)
		dashNames := make([]string, 0, len(cf.Grafana.Dashboards))
		for name := range cf.Grafana.Dashboards {
			dashNames = append(dashNames, name)
		}
		sort.Strings(dashNames)
		for _, name := range dashNames {
			uid := cf.Grafana.Dashboards[name]
			instructions += fmt.Sprintf("  %s: %s\n", name, uid)
		}

		if firstGrafanaURL != "" {
			// Build an example deep-link using the first cluster
			exampleUID := ""
			if uid, ok := cf.Grafana.Dashboards["TiKV-Details"]; ok {
				exampleUID = uid
			} else {
				// Use first dashboard UID as fallback
				exampleUID = cf.Grafana.Dashboards[dashNames[0]]
			}
			instructions += fmt.Sprintf("\nExample: for TiKV details on %s → %s/d/%s/%s-tikv-details\n",
				firstGrafanaCluster, firstGrafanaURL, exampleUID, firstGrafanaCluster)
		}

		instructions += "Append ?from=now-7d&to=now (or other range) to set the time window.\n" +
			"Match the dashboard to the user's question:\n" +
			"  - Slow queries / QPS / connections → TiDB or Performance-Overview\n" +
			"  - Disk usage / store size / engine size / CF size → TiKV-Details (Cluster row: Store size, Available size; Server row: CF size)\n" +
			"  - Regions / compaction / TiKV overview → TiKV-Summary\n" +
			"  - Scheduling / hotspots / balance → PD\n" +
			"  - Host disk I/O / CPU / memory (OS-level) → Node_exporter or Disk-Performance\n" +
			"  - Replication / CDC → TiCDC or TiCDC-Summary\n" +
			"  - Resource groups / RU / throttling / workload control → Resource-Control (search Grafana for 'resource')\n" +
			"Prefer suggesting a specific dashboard link over raw metric dumps when visualization would help.\n\n"

		if dsUID != "" {
			instructions += "GRAFANA EXPLORE — Constructing ad-hoc query links:\n" +
				"When you run a PromQL query and want to give the user a clickable Grafana Explore link with the query pre-filled, construct the URL like this:\n" +
				"  {grafana_url}/explore?orgId=1&left={URL_ENCODED_JSON_ARRAY}\n" +
				"The left parameter is a JSON array (URL-encode the whole thing):\n" +
				fmt.Sprintf("  [\"FROM\",\"TO\",\"%s\",{\"expr\":\"YOUR_PROMQL_HERE\",\"range\":true,\"instant\":false}]\n", dsUID) +
				"Replace FROM/TO with relative times like 'now-1h','now' or 'now-6h','now' or 'now-7d','now'.\n" +
				fmt.Sprintf("The datasource name '%s' is consistent across all clusters.\n", dsUID) +
				"Use this whenever you suggest a PromQL query — provide both the raw query result AND a Grafana Explore link so the user can visualize it.\n\n"
		}
	}

	instructions += "MITIGATION ADVICE — When you detect a problem, always suggest actionable remediation steps.\n\n" +

		"Disk Space Critical (>80% used):\n" +
		"  1. Identify which column families are largest: sum(tikv_engine_size_bytes) by (instance, type)\n" +
		"  2. Check for pending compactions: tikv_engine_pending_compaction_bytes — high values mean compaction is behind\n" +
		"  3. Immediate actions: trigger manual compaction via tikv-ctl compact, drop unused tables/indexes, expand PD store limit to speed up region migration\n" +
		"  4. If write CF is bloated: check for long-running transactions holding GC back (tidb_tikvclient_gc_safe_point)\n" +
		"  5. Longer term: add more TiKV stores and wait for PD to rebalance, or increase disk capacity\n\n" +

		"TiKV Memory Quota Exceeded:\n" +
		"  1. Check block cache usage: tikv_engine_block_cache_size_bytes — it should be ~45% of total memory\n" +
		"  2. If OOM-killed: reduce block-cache.capacity in TiKV config, or increase memory quota\n" +
		"  3. Check for coprocessor memory spikes: tikv_coprocessor_mem_lock_duration_seconds, reduce tidb_distsql_scan_concurrency\n" +
		"  4. Reduce raftstore.store-pool-size or apply-pool-size if region count is low\n\n" +

		"High Query Latency (p99 > normal):\n" +
		"  1. Check where time is spent: compare tidb parse/compile/execute durations\n" +
		"  2. If TiKV is slow: check tikv_grpc_msg_duration_seconds — prewrite/commit latency indicates write path issues\n" +
		"  3. If coprocessor is slow: check tikv_coprocessor_request_duration_seconds — may need more TiKV CPU or index optimization\n" +
		"  4. Check for lock contention: tidb_tikvclient_lock_resolver_actions_total — high values mean transaction conflicts\n" +
		"  5. Check PD TSO latency: pd_client_request_handle_requests_duration_seconds — high TSO wait slows everything\n" +
		"  6. Mitigation: identify slow SQL via top_queries, add indexes, reduce scan ranges, or scale out TiDB/TiKV\n\n" +

		"Store Imbalance (leader/region skew):\n" +
		"  1. Compare leader_count and region_count across stores — skew > 20% is concerning\n" +
		"  2. Check PD scheduler status: pd_scheduler_status — make sure balance-leader and balance-region schedulers are running\n" +
		"  3. Check store weights: pd_scheduler_store_status{type=\"store_weight\"} — unequal weights cause intentional imbalance\n" +
		"  4. If scheduling is stuck: check pd_schedule_operators_count — low operator count means PD isn't moving regions\n" +
		"  5. Mitigation: increase PD leader-schedule-limit and region-schedule-limit, or use pd-ctl to transfer leaders manually\n\n" +

		"Hot Regions / Write Hotspots:\n" +
		"  1. Identify hot stores: topk(10, pd_hotspot_status{type=\"hot_write_region_as_leader\"})\n" +
		"  2. Check if auto-split is working: table regions should split automatically on write pressure\n" +
		"  3. Common causes: monotonic inserts (auto-increment PKs), single-row hot updates\n" +
		"  4. Mitigation: use SHARD_ROW_ID_BITS or AUTO_RANDOM for tables with sequential inserts\n" +
		"  5. For existing hot tables: pre-split regions with SPLIT TABLE, or use placement rules to spread hot regions\n\n" +

		"TiCDC Replication Lag / Stuck:\n" +
		"  1. Check resolved TS gap: ticdc_owner_resolved_ts_lag — this is the replication delay in seconds\n" +
		"  2. Check checkpoint lag: ticdc_owner_checkpoint_ts_lag — large gap means downstream is falling behind\n" +
		"  3. If stuck: check ticdc_processor_table_resolved_ts_lag for per-table stalls\n" +
		"  4. Common causes: downstream write bottleneck, large transactions, DDL blocking\n" +
		"  5. Mitigation: increase TiCDC worker count, optimize downstream schema, split large changefeeds\n\n" +

		"Lock Contention / Transaction Conflicts:\n" +
		"  1. Check lock resolver activity: sum(rate(tidb_tikvclient_lock_resolver_actions_total[5m])) by (type)\n" +
		"  2. Check backoff types: sum(rate(tidb_tikvclient_backoff_seconds_count[5m])) by (type) — txnLock is the key indicator\n" +
		"  3. Common causes: long-running transactions, write-write conflicts on same rows\n" +
		"  4. Mitigation: use optimistic transactions for low-conflict workloads, pessimistic for high-conflict\n" +
		"  5. Reduce transaction scope, batch operations, avoid cross-table transactions when possible\n\n" +

		"PD Leader / TSO Issues:\n" +
		"  1. Check TSO latency: pd_client_request_handle_requests_duration_seconds{type=\"tso\"}\n" +
		"  2. If PD leader is switching frequently: check pd_server_tso{type=\"save\"} and system clock sync\n" +
		"  3. High TSO latency (>50ms) impacts all transactions — check PD node CPU, disk I/O, and network\n" +
		"  4. Mitigation: ensure PD runs on low-latency SSDs, check NTP synchronization, consider dedicated PD nodes\n\n" +

		"GC Blocked / Rising MVCC Versions:\n" +
		"  1. Check GC safe point: tidb_tikvclient_gc_safe_point — if it's stuck, old versions are not being cleaned\n" +
		"  2. Check GC duration: tidb_gc_duration_seconds — long GC cycles indicate large volume of old data\n" +
		"  3. Common causes: long-running transactions, stale CDC changefeeds holding GC back\n" +
		"  4. Mitigation: find and kill long-running transactions, check CDC checkpoint_ts, resolve stale locks via tikv-ctl\n\n" +

		"Node-Level Issues (CPU / Memory / Disk I/O):\n" +
		"  1. CPU saturated: check node_cpu_seconds_total — if system+user > 90%, instance is overloaded\n" +
		"  2. Memory pressure: node_memory_MemAvailable_bytes approaching 0 means OOM risk\n" +
		"  3. Disk I/O: high node_disk_io_time_seconds_total means disk is the bottleneck — check Disk-Performance dashboard\n" +
		"  4. Network: node_network_transmit_bytes_total spikes may indicate data rebalancing or backup operations\n" +
		"  5. Mitigation: scale out (add instances), scale up (bigger machines), or reduce workload (throttle non-critical queries)\n\n" +

		"Always provide BOTH the diagnostic query AND the remediation steps. " +
		"If the issue is urgent (>90% disk, OOM, node down), clearly state the severity. " +
		"Suggest the relevant Grafana dashboard for ongoing monitoring after mitigation.\n\n"

	return instructions
}
