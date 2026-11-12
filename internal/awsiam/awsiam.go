// Package awsiam writes the IAM policies that reading an AWS account with
// moonwalk needs, and nothing beyond them.
//
// What listing each resource type takes is not written by hand. It was
// recorded: the Terraform AWS provider was run against a stand-in for AWS
// that notes every call, once for each type it can list, and each call was
// turned into its IAM action and checked against AWS's catalogue of
// actions. The result is permissions.json; scripts/aws-permissions makes
// it again for a newer provider.
//
// The policies follow three rules. Every action is named in full: there
// is no wildcard in any action. Every action is one that AWS classes as
// List or Read. And a statement's resources are as narrow as AWS lets
// them be: an action that can be limited to resources of one kind is
// limited to that kind, in one account if the account is given, and only
// the actions that AWS does not let anyone limit apply to "*".
package awsiam

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"
)

//go:embed permissions.json
var embedded []byte

// Permissions says which IAM actions listing each resource type takes.
type Permissions struct {
	// Provider is the provider and version the calls were recorded from.
	Provider string `json:"provider"`
	// Catalog is where the actions' access levels and resources are from.
	Catalog string                     `json:"catalog"`
	Types   map[string]TypePermissions `json:"types"`
	Actions map[string]Action          `json:"actions"`
}

// TypePermissions is what one resource type takes.
type TypePermissions struct {
	// List holds the actions that finding the resources takes.
	List []string `json:"list"`
	// Read holds the further actions that reading each one's attributes
	// takes, which is what a list block with include_resource does.
	Read []string `json:"read,omitempty"`
	// ListUnconfirmed and ReadUnconfirmed are true where the recording
	// did not run to the end, because the provider did not accept the
	// stand-in's made-up answer. The calls up to that point are recorded;
	// a real account may show more.
	ListUnconfirmed bool `json:"list_unconfirmed,omitempty"`
	ReadUnconfirmed bool `json:"read_unconfirmed,omitempty"`
	// Global is true for a type that has no region: its service answers
	// from one region for all of them.
	Global bool `json:"global,omitempty"`
}

// Action is what AWS's catalogue says of one action.
type Action struct {
	Level string `json:"level"` // "List" or "Read".
	// Resources holds the ARN formats of the kinds of resource the
	// action can be limited to. It is empty for an action that can only
	// be allowed on every resource.
	Resources []string `json:"resources"`
	// DataPlane is true for an action that AWS marks as working on what
	// a service stores, not on how it is set up.
	DataPlane bool `json:"data_plane,omitempty"`
}

// Withheld are the actions that the provider calls when it lists a type
// and that hand over what is stored in a service, or the means to fetch
// it, rather than how the service is set up. Each is a read by AWS's
// classification, and none belongs in a policy for discovery unless
// someone decides it does: they are left out, with the types that cannot
// be listed without them, unless Options.Allow names them.
var Withheld = map[string]string{
	"lambda:GetFunction": "returns a link to download the function's code",
	"s3:ListBucket":      "lists the objects in a bucket",
	"ssm:GetParameter":   "returns a parameter's value",
}

// Load returns the permissions that ship with moonwalk.
func Load() (*Permissions, error) {
	var p Permissions
	if err := json.Unmarshal(embedded, &p); err != nil {
		return nil, fmt.Errorf("awsiam: %w", err)
	}
	return &p, nil
}

// Document is an IAM policy document.
type Document struct {
	Version   string      `json:"Version"`
	Statement []Statement `json:"Statement"`
}

// Statement is one statement of a policy. Every statement here allows.
type Statement struct {
	Sid       string                         `json:"Sid"`
	Effect    string                         `json:"Effect"`
	Action    []string                       `json:"Action"`
	Resource  []string                       `json:"Resource"`
	Condition map[string]map[string][]string `json:"Condition,omitempty"`
}

// Options says what the policies are to cover.
type Options struct {
	// Types are the resource types to be listed.
	Types []string
	// IdentityOnly leaves out what reading each resource's attributes
	// takes, for list blocks without include_resource.
	IdentityOnly bool
	// Regions limits every action of a regional service to these
	// regions. The actions of types that have no region are left alone:
	// their service answers from one region for all. Global says which
	// those are, for a provider that differs from the one recorded; nil
	// means as recorded.
	Regions []string
	Global  map[string]bool
	// Account limits resources to one account where an action can be
	// limited at all. Partition is "aws" unless said otherwise.
	Account   string
	Partition string
	// Allow names the actions in Withheld that are wanted all the same.
	Allow []string
}

// Plan says how each of the types asked for can be listed, given what a
// policy may hold. A query file and its policy are made from the same
// plan, so that neither asks for what the other leaves out.
type Plan struct {
	// Full are listed with their attributes, and Identity without, either
	// because that was asked for or because reading the attributes takes
	// a withheld action, which IdentityBecause names.
	Full, Identity  []string
	IdentityBecause map[string]string
	// LeftOut cannot be listed at all without the withheld action named.
	LeftOut map[string]string
	// Unknown are types nothing is recorded about. They are listed, and
	// the listing reports what it is refused.
	Unknown []string
}

// Plan works out how the types in o can be listed.
func (p *Permissions) Plan(o Options) (Plan, error) {
	for _, a := range o.Allow {
		if _, ok := Withheld[a]; !ok {
			names := make([]string, 0, len(Withheld))
			for w := range Withheld {
				names = append(names, w)
			}
			sort.Strings(names)
			return Plan{}, fmt.Errorf("%q is not an action that is withheld; those are %s", a, strings.Join(names, ", "))
		}
	}
	withheld := func(actions []string) string {
		for _, a := range actions {
			if _, ok := Withheld[a]; ok && !slices.Contains(o.Allow, a) {
				return a
			}
		}
		return ""
	}
	plan := Plan{IdentityBecause: map[string]string{}, LeftOut: map[string]string{}}
	types := slices.Clone(o.Types)
	sort.Strings(types)
	for _, t := range slices.Compact(types) {
		tp, ok := p.Types[t]
		switch {
		case !ok:
			plan.Unknown = append(plan.Unknown, t)
		case withheld(tp.List) != "":
			plan.LeftOut[t] = withheld(tp.List)
		case o.IdentityOnly:
			plan.Identity = append(plan.Identity, t)
		case withheld(tp.Read) != "":
			plan.Identity = append(plan.Identity, t)
			plan.IdentityBecause[t] = withheld(tp.Read)
		default:
			plan.Full = append(plan.Full, t)
		}
	}
	return plan, nil
}

// MaxSize is the most characters, not counting white space, that IAM
// accepts in one managed policy.
const MaxSize = 6144

// Policies returns the policy documents that listing the given types
// takes, each within the size IAM accepts, and the plan they follow.
// Statements are grouped by service.
func (p *Permissions) Policies(o Options) (docs []Document, plan Plan, err error) {
	partition := o.Partition
	if partition == "" {
		partition = "aws"
	}
	if o.Account != "" && !regexp.MustCompile(`^\d{12}$`).MatchString(o.Account) {
		return nil, plan, fmt.Errorf("an AWS account ID is 12 digits, not %q", o.Account)
	}
	if plan, err = p.Plan(o); err != nil {
		return nil, plan, err
	}

	// The actions in use, and those that a type without a region uses:
	// such a type's service answers from one region for every region, so
	// its actions cannot be held to the regions asked for.
	used, global := map[string]bool{}, map[string]bool{}
	use := func(types []string, actions func(TypePermissions) []string) {
		for _, t := range types {
			isGlobal := p.Types[t].Global
			if o.Global != nil {
				isGlobal = o.Global[t]
			}
			for _, a := range actions(p.Types[t]) {
				used[a] = true
				global[a] = global[a] || isGlobal
			}
		}
	}
	use(plan.Identity, func(tp TypePermissions) []string { return tp.List })
	use(plan.Full, func(tp TypePermissions) []string { return append(slices.Clone(tp.List), tp.Read...) })

	type group struct {
		service            string
		limited, inRegions bool
	}
	groups := map[group][]string{}
	for a := range used {
		info, ok := p.Actions[a]
		if !ok {
			return nil, plan, fmt.Errorf("awsiam: %s is used and not described", a)
		}
		service, _, _ := strings.Cut(a, ":")
		g := group{service, limitable(info.Resources), !global[a] && len(o.Regions) > 0}
		groups[g] = append(groups[g], a)
	}
	keys := make([]group, 0, len(groups))
	for g := range groups {
		keys = append(keys, g)
	}
	sort.Slice(keys, func(i, j int) bool {
		a, b := keys[i], keys[j]
		if a.service != b.service {
			return a.service < b.service
		}
		if a.limited != b.limited {
			return !a.limited
		}
		return !a.inRegions && b.inRegions
	})

	var statements []Statement
	for _, g := range keys {
		actions := groups[g]
		sort.Strings(actions)
		s := Statement{Effect: "Allow", Action: actions, Resource: []string{"*"}}
		s.Sid = "Moonwalk" + sidPart(g.service)
		if g.limited {
			s.Sid += "Limited"
			s.Resource = nil
			for _, a := range actions {
				for _, format := range p.Actions[a].Resources {
					if arn := fill(format, partition, o.Account); !slices.Contains(s.Resource, arn) {
						s.Resource = append(s.Resource, arn)
					}
				}
			}
			s.Resource = summarize(s.Resource)
		}
		if g.inRegions {
			s.Sid += "InRegions"
			s.Condition = map[string]map[string][]string{"StringEquals": {"aws:RequestedRegion": o.Regions}}
		}
		statements = append(statements, s)
	}
	return pack(statements), plan, nil
}

// limitable reports whether an action with these ARN formats can be held
// to resources of its own service. One that applies to any ARN at all, as
// a service that tags other services' resources has, cannot.
func limitable(formats []string) bool {
	for _, f := range formats {
		if parts := strings.SplitN(f, ":", 4); len(parts) < 4 || strings.Contains(parts[2], "$") || parts[2] == "*" {
			return false
		}
	}
	return len(formats) > 0
}

// mostKinds is how many kinds of resource a statement names one by one.
const mostKinds = 6

// summarize sorts a statement's ARN patterns and, where there are more
// than a handful, replaces them with one pattern for each service,
// region and account among them. An action that applies to every kind of
// resource a service has, as its tag listing does, would otherwise name
// dozens, which says no more than the service does.
func summarize(arns []string) []string {
	sort.Strings(arns)
	if len(arns) <= mostKinds {
		return arns
	}
	var out []string
	for _, arn := range arns {
		parts := strings.SplitN(arn, ":", 6)
		if len(parts) < 6 {
			return arns // Not an ARN in six parts: leave the list as it is.
		}
		whole := strings.Join(parts[:5], ":") + ":*"
		if !slices.Contains(out, whole) {
			out = append(out, whole)
		}
	}
	return out
}

// placeholder is a variable in one of the catalogue's ARN formats.
var placeholder = regexp.MustCompile(`\$\{[^}]+\}`)

// fill turns an ARN format from the catalogue into a pattern: the
// partition and, if given, the account are filled in, and every other
// part stands for anything.
func fill(format, partition, account string) string {
	if account == "" {
		account = "*"
	}
	arn := strings.ReplaceAll(format, "${Partition}", partition)
	arn = strings.ReplaceAll(arn, "${Account}", account)
	arn = placeholder.ReplaceAllString(arn, "*")
	// "*/*" and "*:*" say no more than "*".
	for _, run := range []string{"*/*", "*:*"} {
		for strings.HasSuffix(arn, run) {
			arn = strings.TrimSuffix(arn, run[1:])
		}
	}
	return arn
}

// sidPart turns a service prefix into what a statement ID may hold:
// letters and digits.
func sidPart(service string) string {
	var b strings.Builder
	upper := true
	for _, r := range service {
		switch {
		case r >= 'a' && r <= 'z' && upper:
			b.WriteRune(r - 'a' + 'A')
			upper = false
		case (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9'):
			b.WriteRune(r)
			upper = false
		default:
			upper = true
		}
	}
	return b.String()
}

// pack puts statements into as few documents as their size allows, in
// order. A statement too large for a document of its own is split.
func pack(statements []Statement) []Document {
	var docs []Document
	current := Document{Version: "2012-10-17"}
	flush := func() {
		if len(current.Statement) > 0 {
			docs = append(docs, current)
		}
		current = Document{Version: "2012-10-17"}
	}
	fits := func(d Document) bool { return Size(d) <= MaxSize }
	for _, s := range statements {
		for _, part := range split(s) {
			trial := current
			trial.Statement = append(slices.Clone(current.Statement), part)
			if !fits(trial) {
				flush()
				trial = Document{Version: "2012-10-17", Statement: []Statement{part}}
			}
			current = trial
		}
	}
	flush()
	return docs
}

// split returns s whole if it fits a document by itself, and otherwise in
// parts that each do, with the actions divided among them.
func split(s Statement) []Statement {
	if Size(Document{Version: "2012-10-17", Statement: []Statement{s}}) <= MaxSize {
		return []Statement{s}
	}
	var parts []Statement
	part := s
	part.Action = nil
	for _, a := range s.Action {
		trial := part
		trial.Action = append(slices.Clone(part.Action), a)
		trial.Sid = fmt.Sprintf("%s%d", s.Sid, len(parts)+1)
		if len(part.Action) > 0 && Size(Document{Version: "2012-10-17", Statement: []Statement{trial}}) > MaxSize {
			parts = append(parts, part)
			trial = s
			trial.Action = []string{a}
			trial.Sid = fmt.Sprintf("%s%d", s.Sid, len(parts)+1)
		}
		part = trial
	}
	return append(parts, part)
}

// Size returns the size of a document as IAM counts it: its characters
// without white space.
func Size(d Document) int {
	data, err := json.Marshal(d)
	if err != nil {
		return MaxSize + 1
	}
	return len(data)
}

// StateLocation is where states are kept in S3: one object, or every
// state under a prefix if Key is empty or ends in "/".
type StateLocation struct {
	Bucket, Key string
}

// keyARN is the ARN of a KMS key, as a policy has to name it.
var keyARN = regexp.MustCompile(`^arn:[a-z-]+:kms:([a-z0-9-]+):\d{12}:key/[A-Za-z0-9-]+$`)

// StatePolicy returns the policy that reading the given states from S3
// takes: listing the bucket under each prefix, reading the state objects
// there and no others, and using each KMS key to decrypt what S3 hands
// over. Reading states is reading secrets, which is why this is a policy
// of its own, to attach only where states are to be read.
func StatePolicy(locations []StateLocation, kmsKeys []string, partition string) (Document, error) {
	if partition == "" {
		partition = "aws"
	}
	d := Document{Version: "2012-10-17"}
	if len(locations) == 0 {
		return d, fmt.Errorf("no state locations given")
	}
	// Per bucket: the prefixes to list under, and the objects to read.
	type access struct {
		prefixes []string
		whole    bool
		objects  []string
	}
	buckets := map[string]*access{}
	var order []string
	for _, l := range locations {
		if l.Bucket == "" || strings.ContainsAny(l.Bucket+l.Key, "*?") {
			return d, fmt.Errorf("s3://%s/%s is not the address of a state or of a prefix", l.Bucket, l.Key)
		}
		a := buckets[l.Bucket]
		if a == nil {
			a = &access{}
			buckets[l.Bucket] = a
			order = append(order, l.Bucket)
		}
		bucket := "arn:" + partition + ":s3:::" + l.Bucket
		switch {
		case l.Key == "":
			a.whole = true
			a.objects = append(a.objects, bucket+"/*.tfstate")
		case strings.HasSuffix(l.Key, "/"):
			a.prefixes = append(a.prefixes, l.Key+"*")
			a.objects = append(a.objects, bucket+"/"+l.Key+"*.tfstate")
		default:
			a.objects = append(a.objects, bucket+"/"+l.Key)
		}
	}
	sort.Strings(order)
	var objects []string
	for i, name := range order {
		a := buckets[name]
		objects = append(objects, a.objects...)
		if !a.whole && len(a.prefixes) == 0 {
			continue // Single objects are read by name; nothing is listed.
		}
		s := Statement{
			Sid:      fmt.Sprintf("MoonwalkListStates%d", i+1),
			Effect:   "Allow",
			Action:   []string{"s3:ListBucket"},
			Resource: []string{"arn:" + partition + ":s3:::" + name},
		}
		if !a.whole {
			sort.Strings(a.prefixes)
			s.Condition = map[string]map[string][]string{"StringLike": {"s3:prefix": slices.Compact(a.prefixes)}}
		}
		d.Statement = append(d.Statement, s)
	}
	sort.Strings(objects)
	d.Statement = append(d.Statement, Statement{
		Sid: "MoonwalkReadStates", Effect: "Allow", Action: []string{"s3:GetObject"}, Resource: slices.Compact(objects),
	})

	// A key is used through S3 only, in the key's own region.
	byRegion := map[string][]string{}
	for _, key := range kmsKeys {
		m := keyARN.FindStringSubmatch(key)
		if m == nil {
			return d, fmt.Errorf("%q is not the ARN of a KMS key (arn:aws:kms:<region>:<account>:key/<id>); an alias cannot be named in a policy", key)
		}
		byRegion[m[1]] = append(byRegion[m[1]], key)
	}
	regions := make([]string, 0, len(byRegion))
	for r := range byRegion {
		regions = append(regions, r)
	}
	sort.Strings(regions)
	for i, r := range regions {
		sort.Strings(byRegion[r])
		d.Statement = append(d.Statement, Statement{
			Sid:       fmt.Sprintf("MoonwalkDecryptStates%d", i+1),
			Effect:    "Allow",
			Action:    []string{"kms:Decrypt"},
			Resource:  slices.Compact(byRegion[r]),
			Condition: map[string]map[string][]string{"StringEquals": {"kms:ViaService": {"s3." + r + ".amazonaws.com"}}},
		})
	}
	if Size(d) > MaxSize {
		return d, fmt.Errorf("the policy for these states is %d characters, over the %d that IAM accepts; split the locations between two policies", Size(d), MaxSize)
	}
	return d, nil
}
