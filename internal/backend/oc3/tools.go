package oc3

// CoreTools are the oc3 tools the model always sees when the MCP catalog is
// searched and marks no core tool: the counts and lists of the nodes and
// services, and the alerts.
var CoreTools = []string{
	"count_nodes",
	"list_nodes",
	"get_node",
	"count_services",
	"list_services",
	"get_service",
	"list_alerts",
}
