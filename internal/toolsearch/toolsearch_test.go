package toolsearch

import (
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

var catalog = []Document{
	{Name: "list_nodes", Title: "List collector nodes", Description: "List the nodes the collector knows: operating system, agent version, last contact.",
		Parameters: []Parameter{{Name: "os_name", Description: "operating system name"}, {Name: "env", Description: "node environment"}}},
	{Name: "count_nodes", Title: "Count collector nodes", Description: "Count the nodes matching filters, grouped by a property such as the operating system.",
		Parameters: []Parameter{{Name: "group_by", Description: "grouping"}}},
	{Name: "list_node_hardware", Title: "List node hardware", Description: "The PCI devices and memory banks a node reports: its hardware inventory.",
		Keywords: []string{"inventaire", "matériel"}},
	{Name: "get_node_san_topology", Title: "Get node SAN topology", Description: "The fibre channel paths of a node to the storage arrays."},
	{Name: "list_alerts", Title: "List alerts", Description: "The open alerts of the collector, by severity."},
	{Name: "list_service_actions", Title: "List service actions", Description: "The actions the agents ran on services, with their status."},
}

func newIndex(t *testing.T, documents []Document) *Index {
	t.Helper()
	index, err := New(documents)
	if err != nil {
		t.Fatal(err)
	}
	return index
}

func names(results []Result) []string {
	out := make([]string, 0, len(results))
	for _, result := range results {
		out = append(out, result.Name)
	}
	return out
}

func TestTerms(t *testing.T) {
	for text, want := range map[string]int{
		"list_node_hardware":         3,
		"get-node.san/topology 2026": 5,
		"to the of and":              0,
	} {
		if got := terms(text); len(got) != want {
			t.Errorf("terms(%q) = %q, want %d terms", text, got, want)
		}
	}
	if got := terms("The NODES of the Services"); !reflect.DeepEqual(got, terms("node service")) {
		t.Errorf("stop words or case kept: %q", got)
	}
}

// The singular and the plural of a word, and its accented forms, give the
// same key; different words keep different keys.
func TestStemJoinsSingularAndPlural(t *testing.T) {
	for _, forms := range [][]string{
		{"node", "nodes"}, {"service", "services"}, {"disk", "disks"}, {"ip", "ips"}, {"cpu", "cpus"},
		{"policy", "policies"}, {"status", "statuses"}, {"address", "addresses"}, {"process", "processes"},
		{"alias", "aliases"}, {"box", "boxes"}, {"patch", "patches"}, {"hash", "hashes"},
		{"cache", "caches"}, {"cause", "causes"}, {"pause", "pauses"}, {"base", "bases"}, {"database", "databases"},
		{"type", "types"}, {"file", "files"}, {"package", "packages"}, {"instance", "instances"},
		{"matériel", "materiel", "mate\u0301riel", "MATÉRIELS"},
	} {
		want := terms(forms[0])
		for _, form := range forms[1:] {
			if got := terms(form); !reflect.DeepEqual(got, want) {
				t.Errorf("terms(%q) = %q, terms(%q) = %q", forms[0], want, form, got)
			}
		}
	}
	for _, pair := range [][2]string{{"node", "note"}, {"cache", "patch"}, {"status", "state"}, {"disk", "disc"}} {
		if reflect.DeepEqual(terms(pair[0]), terms(pair[1])) {
			t.Errorf("%q and %q share a key", pair[0], pair[1])
		}
	}
}

func TestSearchRanksTheToolOfTheQuestion(t *testing.T) {
	index := newIndex(t, catalog)
	for query, want := range map[string]string{
		"hardware inventory of a node":            "list_node_hardware",
		"inventaire matériel du nœud":             "list_node_hardware",
		"count nodes grouped by operating system": "count_nodes",
		"fibre channel paths to the array":        "get_node_san_topology",
		"open alerts by severity":                 "list_alerts",
		"actions run on a service":                "list_service_actions",
	} {
		results, err := index.Search(query, 3)
		if err != nil {
			t.Fatal(err)
		}
		if len(results) == 0 || results[0].Name != want {
			t.Errorf("Search(%q) = %v, want %s first", query, results, want)
		}
	}
}

// BM25 is lexical: a question in other words than the tool finds it only
// through the keywords of its metadata.
func TestSearchNeedsTheWordsOfTheTool(t *testing.T) {
	results, _ := newIndex(t, catalog).Search("how many", 3)
	if len(results) != 0 {
		t.Fatalf("results = %v", results)
	}
	withKeywords := append([]Document(nil), catalog...)
	withKeywords[1].Keywords = []string{"how many", "number", "total", "distribution"}
	results, _ = newIndex(t, withKeywords).Search("how many nodes per OS", 3)
	if len(results) == 0 || results[0].Name != "count_nodes" {
		t.Fatalf("results with keywords = %v", results)
	}
}

func TestSearchMatchesPluralsAndDecomposedAccents(t *testing.T) {
	index := newIndex(t, []Document{
		{Name: "process_list", Description: "the process of a node and its status"},
		{Name: "hardware", Keywords: []string{"matériel"}},
	})
	for query, want := range map[string]string{"processes statuses": "process_list", "mate\u0301riel": "hardware"} {
		if results, _ := index.Search(query, 1); len(results) != 1 || results[0].Name != want {
			t.Errorf("Search(%q) = %v, want %s", query, results, want)
		}
	}
}

func TestSearchWeighsTheNameAboveTheDescription(t *testing.T) {
	index := newIndex(t, []Document{
		{Name: "a_tool", Description: "mentions hardware once among many other words of a long description"},
		{Name: "hardware_tool", Description: "mentions nothing else of interest among many other words"},
	})
	results, err := index.Search("hardware", 5)
	if err != nil {
		t.Fatal(err)
	}
	if got := names(results); !reflect.DeepEqual(got, []string{"hardware_tool", "a_tool"}) {
		t.Fatalf("results = %v", results)
	}
}

func TestSearchOrdersTiesByNameAndIsDeterministic(t *testing.T) {
	index := newIndex(t, []Document{
		{Name: "zeta", Description: "node status"},
		{Name: "alpha", Description: "node status"},
		{Name: "mid", Description: "node status"},
		{Name: "other", Description: "alert"},
	})
	first, err := index.Search("node status", 10)
	if err != nil {
		t.Fatal(err)
	}
	if got := names(first); !reflect.DeepEqual(got, []string{"alpha", "mid", "zeta"}) {
		t.Fatalf("tie order = %v", got)
	}
	for range 20 {
		again, _ := index.Search("status node node", 10)
		if !reflect.DeepEqual(again, first) {
			t.Fatalf("search changed: %v then %v", first, again)
		}
	}
	limited, _ := index.Search("node status", 2)
	if got := names(limited); !reflect.DeepEqual(got, []string{"alpha", "mid"}) {
		t.Fatalf("limited = %v", got)
	}
}

func TestSearchWithoutMatchReturnsNothing(t *testing.T) {
	results, err := newIndex(t, catalog).Search("kubernetes pod", 5)
	if err != nil || len(results) != 0 {
		t.Fatalf("results = %v, %v", results, err)
	}
	results, err = newIndex(t, nil).Search("node", 5)
	if err != nil || len(results) != 0 {
		t.Fatalf("empty index results = %v, %v", results, err)
	}
}

func TestSearchLimits(t *testing.T) {
	index := newIndex(t, catalog)
	for _, test := range []struct {
		query string
		limit int
	}{
		{"node", 0},
		{"node", MaxResults + 1},
		{strings.Repeat("node ", MaxQueryBytes/5+1), 5},
		{"the of and", 5},
		{"", 5},
		{"node \xff", 5},
	} {
		if _, err := index.Search(test.query, test.limit); err == nil {
			t.Errorf("Search(%.30q, %d) succeeded", test.query, test.limit)
		}
	}
	// Terms past MaxQueryTerms are ignored: the hardware term comes too late.
	var words []string
	for i := range MaxQueryTerms {
		words = append(words, fmt.Sprintf("word%d", i))
	}
	results, err := index.Search(strings.Join(append(words, "hardware"), " "), 5)
	if err != nil || len(results) != 0 {
		t.Fatalf("terms past the limit were searched: %v, %v", results, err)
	}
}

func TestNewLimitsAndValidation(t *testing.T) {
	if _, err := New([]Document{{Name: "a"}, {Name: "a"}}); err == nil {
		t.Fatal("duplicate name accepted")
	}
	if _, err := New([]Document{{Description: "nameless"}}); err == nil {
		t.Fatal("empty name accepted")
	}
	if _, err := New(make([]Document, MaxDocuments+1)); err == nil {
		t.Fatal("too many documents accepted")
	}
	long := newIndex(t, []Document{{Name: "long", Description: strings.Repeat("word ", 100000) + "needle"}})
	if long.lengths[0] > MaxDocumentTerms {
		t.Fatalf("document holds %v terms", long.lengths[0])
	}
	// The weighted name stops before the limit, not one weight past it.
	weighted := newIndex(t, []Document{{Name: strings.TrimSpace(strings.Repeat("word ", 2000))}})
	if weighted.lengths[0] > MaxDocumentTerms {
		t.Fatalf("document holds %v terms", long.lengths[0])
	}
	if results, _ := long.Search("needle", 5); len(results) != 0 {
		t.Fatal("text past the field limit was indexed")
	}
}

func TestFromMCP(t *testing.T) {
	documents := FromMCP([]*mcp.Tool{
		{
			Name: "list_node_hardware", Description: "Hardware of a node.",
			Annotations: &mcp.ToolAnnotations{Title: "Node hardware"},
			Meta:        mcp.Meta{KeywordsMeta: []any{"inventaire", 3, "matériel"}},
			InputSchema: map[string]any{"type": "object", "properties": map[string]any{
				"node":  map[string]any{"type": "string", "description": "exact node name"},
				"limit": map[string]any{"type": "integer"},
			}},
		},
		nil,
		{Name: "bare", InputSchema: "not a schema"},
		{
			Name: "typed", Meta: mcp.Meta{KeywordsMeta: []string{"inventory"}},
			InputSchema: map[string]any{"type": "object", "properties": map[string]any{
				"legacy": false,
				"node":   map[string]any{"description": "exact node name"},
				"odd":    map[string]any{"description": 3},
			}},
		},
	})
	want := []Document{
		{Name: "list_node_hardware", Title: "Node hardware", Description: "Hardware of a node.", Keywords: []string{"inventaire", "matériel"},
			Parameters: []Parameter{{Name: "limit"}, {Name: "node", Description: "exact node name"}}},
		{Name: "bare"},
		{Name: "typed", Keywords: []string{"inventory"},
			Parameters: []Parameter{{Name: "legacy"}, {Name: "node", Description: "exact node name"}, {Name: "odd"}}},
	}
	if !reflect.DeepEqual(documents, want) {
		t.Fatalf("documents = %#v", documents)
	}
}

func TestConcurrentSearches(t *testing.T) {
	index := newIndex(t, catalog)
	var group sync.WaitGroup
	for range 8 {
		group.Add(1)
		go func() {
			defer group.Done()
			for range 100 {
				if results, err := index.Search("node hardware", 3); err != nil || results[0].Name != "list_node_hardware" {
					t.Error(results, err)
					return
				}
			}
		}()
	}
	group.Wait()
}
