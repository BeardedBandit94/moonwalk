// Package web serves the explorer: a JSON API over a loaded graph and the
// browser front end that draws it.
package web

import (
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/BeardedBandit94/moonwalk/internal/graph"
	"github.com/BeardedBandit94/moonwalk/internal/view"
)

//go:embed static
var static embed.FS

// maxRelated caps each list of related resources in a resource's details.
// A network can contain thousands; the response says how many there are.
const maxRelated = 200

// snapshot is one loaded graph and what was worked out from it. It is never
// changed once built; reloading makes a new one.
type snapshot struct {
	g *graph.Graph
	// out and in hold each node's edges by the end the node is at; via
	// holds, for a glue resource, the link that stands for it.
	out, in, via map[string][]graph.Edge
	// generation counts the graphs this server has been given, so that the
	// page can tell when what it shows is out of date.
	generation int

	mu    sync.Mutex
	trees map[string]*view.Tree // Built trees, by the options that shaped them.
}

func newSnapshot(g *graph.Graph, generation int) *snapshot {
	s := &snapshot{
		g:          g,
		out:        map[string][]graph.Edge{},
		in:         map[string][]graph.Edge{},
		via:        map[string][]graph.Edge{},
		generation: generation,
		trees:      map[string]*view.Tree{},
	}
	for _, e := range g.Edges() { // Sorted, so the lists come out in a stable order.
		s.out[e.From] = append(s.out[e.From], e)
		s.in[e.To] = append(s.in[e.To], e)
		if e.Via != "" {
			s.via[e.Via] = append(s.via[e.Via], e)
		}
	}
	return s
}

// Server answers the explorer's requests.
type Server struct {
	// LocalOnly makes the server refuse requests addressed to any host name
	// but this machine's. A server bound to the loopback address needs it:
	// otherwise a web page elsewhere could reach it through a DNS name that
	// points back at 127.0.0.1.
	LocalOnly bool

	current atomic.Pointer[snapshot]
}

// New returns a server for g. Relationships should already be derived and
// indirect dependencies removed, as the views count what is there. The
// graph must not change once handed over.
func New(g *graph.Graph) *Server {
	s := &Server{}
	s.current.Store(newSnapshot(g, 1))
	return s
}

// Replace makes the server answer from g from now on, as after a state
// file has changed. Requests already under way finish on the old graph.
func (s *Server) Replace(g *graph.Graph) {
	s.current.Store(newSnapshot(g, s.current.Load().generation+1))
}

// Handler returns the HTTP handler for the API and the front end.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	api := func(pattern string, h func(*snapshot, http.ResponseWriter, *http.Request)) {
		mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
			snap := s.current.Load()
			w.Header().Set("X-Moonwalk-Generation", strconv.Itoa(snap.generation))
			h(snap, w, r)
		})
	}
	api("GET /api/meta", (*snapshot).meta)
	api("GET /api/view", (*snapshot).view)
	api("GET /api/resource", (*snapshot).resource)
	api("GET /api/link", (*snapshot).link)
	files, err := fs.Sub(static, "static")
	if err != nil {
		panic(err) // The directory is embedded at build time.
	}
	mux.Handle("GET /", http.FileServerFS(files))

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.LocalOnly && !localHost(r.Host) {
			http.Error(w, "moonwalk only answers requests addressed to localhost", http.StatusForbidden)
			return
		}
		// The page needs nothing from other origins, and nothing else
		// should be able to frame it or read it.
		w.Header().Set("Content-Security-Policy", "default-src 'self'; frame-ancestors 'none'")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Cache-Control", "no-store")
		mux.ServeHTTP(w, r)
	})
}

// localHost reports whether a Host header names this machine.
func localHost(host string) bool {
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host = strings.Trim(host, "[]")
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

type metaResponse struct {
	// Generation changes whenever the server loads its states afresh.
	Generation int      `json:"generation"`
	Resources  int      `json:"resources"`
	Sources    []string `json:"sources"`
	// Accounts, Regions and Statuses list the values present, for filters.
	Accounts       []string         `json:"accounts"`
	Regions        []string         `json:"regions"`
	Statuses       []string         `json:"statuses"`
	Dimensions     []view.Dimension `json:"dimensions"`
	DefaultGroupBy []view.Dimension `json:"default_group_by"`
	Budget         int              `json:"budget"`
}

func (s *snapshot) meta(w http.ResponseWriter, _ *http.Request) {
	sources, accounts, regions, statuses := map[string]bool{}, map[string]bool{}, map[string]bool{}, map[string]bool{}
	resources := 0
	for n := range s.g.AllNodes() {
		resources++
		sources[n.Source] = true
		accounts[n.Account] = true
		regions[n.Region] = true
		statuses[string(n.Status)] = true
	}
	writeJSON(w, metaResponse{
		Generation:     s.generation,
		Resources:      resources,
		Sources:        sortedKeys(sources),
		Accounts:       sortedKeys(accounts),
		Regions:        sortedKeys(regions),
		Statuses:       sortedKeys(statuses),
		Dimensions:     view.AllDimensions(),
		DefaultGroupBy: view.DefaultDimensions,
		Budget:         view.Budget,
	})
}

// sortedKeys returns the non-empty keys of set in order.
func sortedKeys(set map[string]bool) []string {
	keys := make([]string, 0, len(set))
	for k := range set {
		if k != "" {
			keys = append(keys, k)
		}
	}
	slices.Sort(keys)
	return keys
}

// tree returns the tree for the grouping and filters in the query, building
// it on first use.
func (s *snapshot) tree(q url.Values) (*view.Tree, error) {
	dims := view.DefaultDimensions
	if v := q.Get("group_by"); v != "" {
		var err error
		if dims, err = view.ParseDimensions(v); err != nil {
			return nil, err
		}
	}
	o := view.Options{
		Dimensions: dims,
		ShowGlue:   q.Get("glue") == "1",
		Filter: view.Filter{
			Types:    list(q.Get("type")),
			Accounts: list(q.Get("account")),
			Regions:  list(q.Get("region")),
			Sources:  list(q.Get("source")),
			Statuses: list(q.Get("status")),
			Search:   q.Get("search"),
			Around:   q.Get("around"),
		},
	}
	if o.Filter.Around != "" {
		if _, ok := s.g.Node(o.Filter.Around); !ok {
			return nil, fmt.Errorf("no resource has the ID %q", o.Filter.Around)
		}
	}
	keyJSON, err := json.Marshal(o)
	if err != nil {
		return nil, err
	}
	key := string(keyJSON)

	s.mu.Lock()
	defer s.mu.Unlock()
	if t, ok := s.trees[key]; ok {
		return t, nil
	}
	if len(s.trees) >= 8 { // Typing in the search box makes many; keep few.
		clear(s.trees)
	}
	t := view.Build(s.g, o)
	s.trees[key] = t
	return t, nil
}

// list splits a comma-separated parameter; an empty one gives no list.
func list(v string) []string {
	var out []string
	for _, part := range strings.Split(v, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

// view answers GET /api/view. Besides the grouping and filter parameters it
// takes either "depth", or any number of "open" parameters naming groups to
// expand; with neither, it expands as far as the budget allows.
func (s *snapshot) view(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	t, err := s.tree(q)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	switch {
	case len(q["open"]) > 0:
		writeJSON(w, t.DocumentOpen(q["open"]))
	case q.Get("depth") != "":
		depth, err := strconv.Atoi(q.Get("depth"))
		if err != nil || depth < 1 {
			http.Error(w, "depth must be a whole number, 1 or more", http.StatusBadRequest)
			return
		}
		writeJSON(w, t.Document(min(depth, t.Depth())))
	default:
		writeJSON(w, t.Document(t.AutoDepth(view.Budget)))
	}
}

// ref names a related resource in a resource's details.
type ref struct {
	ID      string `json:"id"`
	Address string `json:"address"`
	Source  string `json:"source"`
	Type    string `json:"type"`
	// Via is the glue resource behind a link.
	Via *ref `json:"via,omitempty"`
}

// related is a capped list of refs with the uncapped count.
type related struct {
	Total int   `json:"total"`
	Items []ref `json:"items"`
}

type resourceResponse struct {
	Node graph.Node `json:"node"`
	// Group is the ID of the group the resource sits in for the grouping
	// and filters in the request, and Shown says whether it appears at all;
	// a filter or glue folding can leave it out.
	Group string `json:"group"`
	Shown bool   `json:"shown"`

	DependsOn    related `json:"depends_on"`
	DependedOnBy related `json:"depended_on_by"`
	Container    *ref    `json:"container,omitempty"`
	Contains     related `json:"contains"`
	LinkedTo     related `json:"linked_to"`
	// Joins lists, for a glue resource, the two resources it attaches.
	Joins related `json:"joins"`
}

// resource answers GET /api/resource?id=<node ID>, with the same grouping
// and filter parameters as the view so that it can say where the resource
// is shown.
func (s *snapshot) resource(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	id := q.Get("id")
	n, ok := s.g.Node(id)
	if !ok {
		http.Error(w, "no such resource", http.StatusNotFound)
		return
	}
	t, err := s.tree(q)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	resp := resourceResponse{Node: n, Group: t.GroupOf(id), Shown: t.Shows(id)}
	add := func(to *related, id, via string) {
		to.Total++
		if len(to.Items) < maxRelated {
			item := s.ref(id)
			if via != "" {
				v := s.ref(via)
				item.Via = &v
			}
			to.Items = append(to.Items, item)
		}
	}
	for _, e := range s.out[id] {
		switch e.Kind {
		case graph.EdgeDependsOn:
			add(&resp.DependsOn, e.To, "")
		case graph.EdgeContains:
			add(&resp.Contains, e.To, "")
		case graph.EdgeLinks:
			add(&resp.LinkedTo, e.To, e.Via)
		}
	}
	for _, e := range s.in[id] {
		switch e.Kind {
		case graph.EdgeDependsOn:
			add(&resp.DependedOnBy, e.From, "")
		case graph.EdgeContains:
			c := s.ref(e.From)
			resp.Container = &c
		case graph.EdgeLinks:
			add(&resp.LinkedTo, e.From, e.Via)
		}
	}
	for _, e := range s.via[id] {
		add(&resp.Joins, e.From, "")
		add(&resp.Joins, e.To, "")
	}
	for _, list := range []*related{&resp.DependsOn, &resp.DependedOnBy, &resp.Contains, &resp.LinkedTo, &resp.Joins} {
		if list.Items == nil {
			list.Items = []ref{} // An empty list, not null, for the front end.
		}
	}
	writeJSON(w, resp)
}

func (s *snapshot) ref(id string) ref {
	n, _ := s.g.Node(id)
	return ref{ID: id, Address: n.Address, Source: n.Source, Type: n.Type}
}

// linkedPair is one edge behind a link between two items of a view.
type linkedPair struct {
	From ref            `json:"from"`
	To   ref            `json:"to"`
	Kind graph.EdgeKind `json:"kind"`
	Via  *ref           `json:"via,omitempty"`
}

type linkResponse struct {
	Total int          `json:"total"`
	Pairs []linkedPair `json:"pairs"`
}

// link answers GET /api/link?from=<item ID>&to=<item ID>: the dependencies
// and glue links that run between the resources two items of a view stand
// for, which is what a link drawn between them counts. It takes the same
// grouping and filter parameters as the view.
func (s *snapshot) link(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	t, err := s.tree(q)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	from, to := t.NodesUnder(q.Get("from")), t.NodesUnder(q.Get("to"))
	if from == nil || to == nil {
		http.Error(w, "from and to must each name an item of the view", http.StatusNotFound)
		return
	}
	inTo := make(map[string]bool, len(to))
	for _, id := range to {
		inTo[id] = true
	}
	inFrom := make(map[string]bool, len(from))
	for _, id := range from {
		inFrom[id] = true
	}
	resp := linkResponse{Pairs: []linkedPair{}}
	add := func(e graph.Edge) {
		resp.Total++
		if len(resp.Pairs) >= maxRelated {
			return
		}
		pair := linkedPair{From: s.ref(e.From), To: s.ref(e.To), Kind: e.Kind}
		if e.Via != "" {
			v := s.ref(e.Via)
			pair.Via = &v
		}
		resp.Pairs = append(resp.Pairs, pair)
	}
	glueShown := q.Get("glue") == "1"
	for _, id := range from {
		for _, e := range s.out[id] {
			switch {
			case e.Kind == graph.EdgeDependsOn && inTo[e.To],
				e.Kind == graph.EdgeLinks && !glueShown && inTo[e.To]:
				add(e)
			}
		}
		// A glue link has no direction, so it may be stored the other way.
		for _, e := range s.in[id] {
			if e.Kind == graph.EdgeLinks && !glueShown && inTo[e.From] && !inFrom[e.From] {
				add(e)
			}
		}
	}
	writeJSON(w, resp)
}
