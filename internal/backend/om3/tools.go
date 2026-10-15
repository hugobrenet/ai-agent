package om3

// CoreTools are the om3 tools the model always sees when the MCP catalog is
// searched and marks no core tool: the state of the cluster, of its objects
// and nodes, and the identity of the caller.
var CoreTools = []string{
	"get_cluster_status",
	"list_cluster_objects",
	"get_object_status",
	"get_instance_status",
	"get_node_status",
	"get_caller_identity",
}
