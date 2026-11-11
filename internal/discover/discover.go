// Package discover reads what actually exists in a cloud and joins it to a
// graph built from state: resources that a state already holds are left as
// they are, and the rest are added with the status "unmanaged".
//
// The input is the output of `terraform query -json`. Terraform does the
// listing through the provider, with its own credentials, pagination and
// rate limiting; moonwalk only reads the result.
package discover

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/BeardedBandit94/moonwalk/internal/graph"
	"github.com/BeardedBandit94/moonwalk/internal/tfstate"
)

// Result counts what Load found.
type Result struct {
	Found     int // Resources the query listed.
	Matched   int // Of those, the ones a state already holds.
	Unmanaged int // The ones added as unmanaged.
	Defaults  int // The ones left out because the cloud made them itself.
	// Errors holds what the query reported as errors, such as a type it
	// was not allowed to list. With any, the listing is incomplete.
	Errors []string
}

// IsQueryOutput reports whether data looks like `terraform query -json`
// output: a stream of JSON messages, not a state.
func IsQueryOutput(data []byte) bool {
	var first struct {
		Level *string `json:"@level"`
		Type  string  `json:"type"`
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	return dec.Decode(&first) == nil && first.Level != nil && first.Type != ""
}

// message is one line of the query's output.
type message struct {
	Type       string `json:"type"`
	Found      *found `json:"list_resource_found"`
	Diagnostic *struct {
		Severity string `json:"severity"`
		Summary  string `json:"summary"`
		Detail   string `json:"detail"`
	} `json:"diagnostic"`
}

// found is a resource the query listed.
type found struct {
	DisplayName  string         `json:"display_name"`
	ResourceType string         `json:"resource_type"`
	Identity     map[string]any `json:"identity"`
	// ResourceObject holds the resource's attributes when the list block
	// asked for them with include_resource.
	ResourceObject map[string]any `json:"resource_object"`
}

// Load reads query output from r and joins it to g under the given source
// name. Every state should be loaded first: a resource is unmanaged only
// if no state in g holds it.
//
// A listed resource is matched to a resource in state of the same type
// whose attributes agree with every part of its identity that they both
// have. The attributes of an unmanaged resource are kept for working out
// where it sits, but withheld from output: a listing does not say which of
// them are secret.
func Load(g *graph.Graph, source string, r io.Reader) (Result, error) {
	var res Result
	if source == "" {
		return res, fmt.Errorf("discover: source name is empty")
	}
	idx := newIndex(g)
	dec := json.NewDecoder(r)
	dec.UseNumber()
	for {
		var m message
		if err := dec.Decode(&m); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			return res, fmt.Errorf("discover: %s: not `terraform query -json` output: %w", source, err)
		}
		if d := m.Diagnostic; d != nil && d.Severity == "error" {
			res.Errors = append(res.Errors, strings.TrimSpace(d.Summary+": "+d.Detail))
		}
		f := m.Found
		if m.Type != "list_resource_found" || f == nil || f.ResourceType == "" {
			continue
		}
		res.Found++
		if madeByCloud(f) {
			res.Defaults++
			continue
		}
		if idx.matches(f) {
			res.Matched++
			continue
		}
		n := f.node(source)
		if _, dup := g.Node(n.ID); dup {
			continue // Listed twice, by two list blocks.
		}
		if err := g.AddNode(n); err != nil {
			return res, fmt.Errorf("discover: %s: %w", source, err)
		}
		res.Unmanaged++
	}
	return res, nil
}

// identity returns the parts of a listed resource's identity as strings.
// name is the part that names the resource itself, as opposed to saying
// where it is.
func (f *found) identity() (parts map[string]string, name string) {
	parts = map[string]string{}
	var keys []string
	for k, v := range f.Identity {
		if s := fmt.Sprint(v); v != nil && s != "" {
			parts[k] = s
			if k != "account_id" && k != "region" {
				keys = append(keys, k)
			}
		}
	}
	sort.Strings(keys)
	switch {
	case parts["id"] != "":
		name = "id"
	case len(keys) > 0:
		name = keys[0]
	}
	return parts, name
}

// node returns the unmanaged resource that stands for f.
func (f *found) node(source string) graph.Node {
	parts, name := f.identity()
	label := parts[name]
	if label == "" {
		label = f.DisplayName
	}
	provider, _, _ := strings.Cut(f.ResourceType, "_")
	account, region := tfstate.Partition(provider, f.ResourceType, f.ResourceObject)
	if account == "" {
		account = parts["account_id"]
	}
	if region == "" {
		region = parts["region"]
	}
	cloudID := tfstate.CloudID(provider, f.ResourceObject)
	if cloudID == "" {
		cloudID = label
	}
	address := f.ResourceType + "." + label
	// A name such as a role's or a bucket's repeats across accounts and
	// regions, so the ID says where the resource is.
	id := source + "::" + address
	if account != "" || region != "" {
		id += "@" + account + "/" + region
	}
	return graph.Node{
		ID:                 id,
		Source:             source,
		Address:            address,
		ConfigAddress:      address,
		Mode:               graph.ModeManaged,
		Type:               f.ResourceType,
		Name:               f.DisplayName,
		Status:             graph.StatusUnmanaged,
		Account:            account,
		Region:             region,
		CloudID:            cloudID,
		Attributes:         f.ResourceObject,
		AttributesWithheld: len(f.ResourceObject) > 0,
	}
}

// madeByCloud reports whether a listed resource is one the cloud creates
// by itself, which nobody would expect to find in code. The AWS provider
// already leaves default networks and subnets out of a listing.
func madeByCloud(f *found) bool {
	switch {
	case strings.HasPrefix(f.ResourceType, "aws_default_"):
		return true // A second view of something listed under its own type.
	case f.ResourceType == "aws_security_group":
		name, _ := f.ResourceObject["name"].(string)
		return name == "default" || (name == "" && f.DisplayName == "default")
	case f.ResourceType == "aws_iam_role":
		path, _ := f.ResourceObject["path"].(string)
		return strings.HasPrefix(path, "/aws-service-role/")
	}
	return false
}

// index finds the resources in a graph that a listed resource could be.
type index struct {
	nodes []graph.Node
	// byValue[type][key][value] lists the positions in nodes of the
	// resources of that type whose attribute key has that value. Built per
	// type and key on first use.
	byValue map[string]map[string]map[string][]int
	byType  map[string][]int
}

func newIndex(g *graph.Graph) *index {
	idx := &index{byValue: map[string]map[string]map[string][]int{}, byType: map[string][]int{}}
	for n := range g.AllNodes() {
		// Only what a state holds can make a listed resource "managed".
		if n.Mode != graph.ModeManaged || n.Status == graph.StatusDeclared || n.Status == graph.StatusUnmanaged {
			continue
		}
		idx.byType[n.Type] = append(idx.byType[n.Type], len(idx.nodes))
		idx.nodes = append(idx.nodes, n)
	}
	return idx
}

func (idx *index) candidates(resourceType, key, value string) []int {
	keys := idx.byValue[resourceType]
	if keys == nil {
		keys = map[string]map[string][]int{}
		idx.byValue[resourceType] = keys
	}
	values, built := keys[key]
	if !built {
		values = map[string][]int{}
		for _, i := range idx.byType[resourceType] {
			if v, ok := idx.nodes[i].Attributes[key]; ok && v != nil {
				s := fmt.Sprint(v)
				values[s] = append(values[s], i)
			}
		}
		keys[key] = values
	}
	return values[value]
}

// matches reports whether some resource in state is the listed one.
func (idx *index) matches(f *found) bool {
	parts, name := f.identity()
	if name == "" {
		return false
	}
	for _, i := range idx.candidates(f.ResourceType, name, parts[name]) {
		n := idx.nodes[i]
		same := true
		for k, want := range parts {
			switch k {
			case "account_id":
				same = same && (n.Account == "" || n.Account == want)
			case "region":
				same = same && (n.Region == "" || n.Region == graph.RegionGlobal || n.Region == want)
			default:
				if v, ok := n.Attributes[k]; ok && v != nil {
					same = same && fmt.Sprint(v) == want
				}
			}
		}
		if same {
			return true
		}
	}
	return false
}
