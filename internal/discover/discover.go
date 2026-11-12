// Package discover reads what actually exists in a cloud and joins it to a
// graph built from state: resources that a state already holds are left as
// they are, and the rest are added with the status "unmanaged".
//
// Reading an estate as managed, unmanaged or drifted follows driftctl
// (github.com/snyk/driftctl), which made that the way to describe one.
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
	"regexp"
	"slices"
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
	// NotListed counts the resources in state that the listing should have
	// found, going by the types and places it covered, and did not.
	NotListed int
	// Errors holds what the query reported as errors, such as a type it
	// was not allowed to list. With any, the listing is incomplete.
	Errors []string
	// Denied holds the actions that the errors say the listing was not
	// allowed, such as "ec2:DescribeVpcs": what its credentials' policy
	// lacks.
	Denied []string
	// Capped names the list blocks that returned exactly as many resources
	// as a list block returns when it has no limit of its own, and so have
	// probably been cut short.
	Capped []string
}

// denied finds the action in the way AWS words a refusal: "... is not
// authorized to perform: ec2:DescribeVpcs because no identity-based
// policy allows ...".
var denied = regexp.MustCompile(`not authorized to perform: ([a-z0-9-]+:[A-Za-z0-9]+)`)

// defaultLimit is how many resources a list block returns unless it sets
// a limit. Terraform stops there without saying so.
const defaultLimit = 100

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
	Type  string `json:"type"`
	Found *found `json:"list_resource_found"`
	Start *struct {
		Address      string         `json:"address"`
		ResourceType string         `json:"resource_type"`
		InputConfig  map[string]any `json:"input_config"`
	} `json:"list_start"`
	Complete *struct {
		Address string `json:"address"`
		Total   int    `json:"total"`
	} `json:"list_complete"`
	Diagnostic *struct {
		Severity string `json:"severity"`
		Summary  string `json:"summary"`
		Detail   string `json:"detail"`
	} `json:"diagnostic"`
}

// found is a resource the query listed.
type found struct {
	Address      string         `json:"address"` // Of the list block.
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
	blocks := map[string]*listBlock{}
	matched := map[string]bool{}
	accounts := map[string]bool{}

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
			for _, match := range denied.FindAllStringSubmatch(d.Summary+" "+d.Detail, -1) {
				if !slices.Contains(res.Denied, match[1]) {
					res.Denied = append(res.Denied, match[1])
					sort.Strings(res.Denied)
				}
			}
		}
		if st := m.Start; st != nil {
			region, _ := st.InputConfig["region"].(string)
			blocks[st.Address] = &listBlock{resourceType: st.ResourceType, region: region, regions: map[string]bool{}}
		}
		if c := m.Complete; c != nil {
			if blk := blocks[c.Address]; blk != nil {
				blk.complete = true
			}
			if c.Total == defaultLimit {
				res.Capped = append(res.Capped, c.Address)
				delete(blocks, c.Address) // Not a full listing of anything.
			}
		}
		f := m.Found
		if m.Type != "list_resource_found" || f == nil || f.ResourceType == "" {
			continue
		}
		res.Found++
		parts, _ := f.identity()
		if a := accountOf(parts); a != "" {
			accounts[a] = true
		}
		if blk := blocks[f.Address]; blk != nil {
			r := regionOf(parts)
			if r == "" {
				r = graph.RegionGlobal // An identity with no region: a global resource.
			}
			blk.regions[r] = true
		}
		if madeByCloud(f) {
			res.Defaults++
			continue
		}
		if id := idx.match(f); id != "" {
			matched[id] = true
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

	res.NotListed = markNotListed(g, idx, blocks, matched, accounts, len(res.Errors) > 0)
	return res, nil
}

// listBlock is one list block of the query and what it covered.
type listBlock struct {
	resourceType string
	// region is the region the block was configured with, if any; regions
	// holds those of the resources it found.
	region   string
	regions  map[string]bool
	complete bool
}

// markNotListed marks the resources in state that the listing covered and
// did not find. A resource is covered only when all of this is known: the
// listing finished without errors, some list block listed the resource's
// type in full in its region, and the listing was made in its account.
// Anything less and the resource is left alone.
func markNotListed(g *graph.Graph, idx *index, blocks map[string]*listBlock, matched, accounts map[string]bool, failed bool) int {
	if failed || len(accounts) != 1 {
		return 0
	}
	var account string
	for a := range accounts {
		account = a
	}
	// The regions each type was listed in. A block with no region of its
	// own ran in the provider's, which shows only in what it found.
	covered := map[string]map[string]bool{}
	for _, blk := range blocks {
		if !blk.complete {
			continue
		}
		region := blk.region
		if region == "" && len(blk.regions) == 1 {
			for r := range blk.regions {
				region = r
			}
		}
		if region == "" {
			continue
		}
		if covered[blk.resourceType] == nil {
			covered[blk.resourceType] = map[string]bool{}
		}
		covered[blk.resourceType][region] = true
	}
	n := 0
	for _, node := range idx.nodes {
		if matched[node.ID] || node.Status != graph.StatusManaged || node.Account != account || !covered[node.Type][node.Region] {
			continue
		}
		g.Update(node.ID, func(n *graph.Node) { n.NotListed = true })
		n++
	}
	return n
}

// accountOf returns the account, subscription or project in an identity.
func accountOf(parts map[string]string) string {
	for _, key := range accountKeys {
		if parts[key] != "" {
			return parts[key]
		}
	}
	return ""
}

// regionOf returns the region in an identity. Azure calls it a location,
// and Google may give a zone, which is a region with a suffix.
func regionOf(parts map[string]string) string {
	switch {
	case parts["region"] != "":
		return parts["region"]
	case parts["location"] != "":
		return parts["location"]
	case parts["zone"] != "":
		if i := strings.LastIndex(parts["zone"], "-"); i > 0 {
			return parts["zone"][:i]
		}
	}
	return ""
}

// accountKeys are the names under which providers put the account in a
// resource's identity: AWS, Azure and Google in turn.
var accountKeys = []string{"account_id", "subscription_id", "project"}

// identity returns the parts of a listed resource's identity as strings.
// name is the part that names the resource itself, as opposed to saying
// where it is.
func (f *found) identity() (parts map[string]string, name string) {
	parts = map[string]string{}
	var keys []string
	for k, v := range f.Identity {
		if s := fmt.Sprint(v); v != nil && s != "" {
			parts[k] = s
			if k != "region" && !slices.Contains(accountKeys, k) {
				keys = append(keys, k)
			}
		}
	}
	sort.Strings(keys)
	// The part most likely to be unique comes first, so that few
	// resources in state need a closer look.
	for _, key := range []string{"id", "arn", "name"} {
		if parts[key] != "" {
			return parts, key
		}
	}
	if len(keys) > 0 {
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
		account = accountOf(parts)
	}
	if region == "" {
		region = regionOf(parts)
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
	// In Azure it repeats across resource groups as well.
	group := tfstate.ResourceGroup(provider, f.ResourceObject)
	if group == "" && provider == "azurerm" {
		group = parts["resource_group_name"]
	}
	if group != "" {
		id += "/" + group
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
		ResourceGroup:      group,
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

// match returns the ID of the resource in state that is the listed one,
// or "" if there is none. A resource with nothing in its identity but
// where it is, of which there is one per account and region, is matched on
// that alone.
func (idx *index) match(f *found) string {
	parts, name := f.identity()
	var candidates []int
	if name == "" {
		candidates = idx.byType[f.ResourceType]
	} else {
		candidates = idx.candidates(f.ResourceType, name, parts[name])
	}
	for _, i := range candidates {
		n := idx.nodes[i]
		same := true
		for k, want := range parts {
			v, has := n.Attributes[k]
			switch {
			case has && v != nil:
				same = same && fmt.Sprint(v) == want
			case slices.Contains(accountKeys, k):
				same = same && (n.Account == "" || n.Account == want)
			case k == "region":
				same = same && (n.Region == "" || n.Region == graph.RegionGlobal || n.Region == want)
			}
		}
		if same {
			return n.ID
		}
	}
	return ""
}
