package discover

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/BeardedBandit94/moonwalk/internal/graph"
)

// arnListing is the output of either of two AWS CLI commands that list
// resources by ARN, for those without `terraform query`:
//
//	aws resourcegroupstaggingapi get-resources
//	aws resource-explorer-2 search --query-string "*"
type arnListing struct {
	// The tagging API. It knows only resources that have, or once had, tags.
	ResourceTagMappingList []struct {
		ResourceARN string
		Tags        []struct{ Key, Value string }
	}
	// Resource Explorer, which must be switched on in the account.
	Resources []struct {
		Arn string
	}
}

// IsARNListing reports whether data looks like the output of one of the
// AWS CLI commands that LoadARNs reads. The keys are compared exactly: a
// state file has a "resources" key, which must not be taken for the
// "Resources" of a Resource Explorer search.
func IsARNListing(data []byte) bool {
	var keys map[string]json.RawMessage
	if json.Unmarshal(data, &keys) != nil {
		return false
	}
	_, tagging := keys["ResourceTagMappingList"]
	_, explorer := keys["Resources"]
	return tagging || explorer
}

// LoadARNs reads a listing of ARNs from r and joins it to g under the given
// source name, as Load does for query output. A listed resource is matched
// to a resource in state by its ARN alone, so a resource whose state holds
// no ARN shows as unmanaged. Nothing is known about a listed resource but
// what its ARN says, so it cannot be placed in a network.
func LoadARNs(g *graph.Graph, source string, r io.Reader) (Result, error) {
	var res Result
	if source == "" {
		return res, fmt.Errorf("discover: source name is empty")
	}
	var listing arnListing
	if err := json.NewDecoder(r).Decode(&listing); err != nil {
		return res, fmt.Errorf("discover: %s: not an AWS CLI resource listing: %w", source, err)
	}
	inState := map[string]bool{}
	for n := range g.AllNodes() {
		if n.Status != graph.StatusDeclared && n.Status != graph.StatusUnmanaged && strings.HasPrefix(n.CloudID, "arn:") {
			inState[n.CloudID] = true
		}
	}

	names := map[string]string{} // ARN to its Name tag, if any; also the set of ARNs seen.
	var arns []string
	add := func(arn, name string) {
		if _, seen := names[arn]; !seen && arn != "" {
			arns = append(arns, arn)
		}
		if name != "" || names[arn] == "" {
			names[arn] = name
		}
	}
	for _, m := range listing.ResourceTagMappingList {
		name := ""
		for _, t := range m.Tags {
			if t.Key == "Name" {
				name = t.Value
			}
		}
		add(m.ResourceARN, name)
	}
	for _, m := range listing.Resources {
		add(m.Arn, "")
	}

	for _, arn := range arns {
		res.Found++
		a, ok := parseARN(arn)
		switch {
		case !ok:
			continue
		case a.service == "iam" && strings.HasPrefix(a.resource, "aws-service-role/"):
			res.Defaults++ // A role the cloud made for one of its services.
			continue
		case inState[arn]:
			res.Matched++
			continue
		}
		region := a.region
		if region == "" {
			region = graph.RegionGlobal
		}
		address := a.terraformType() + "." + a.resource
		n := graph.Node{
			ID:            source + "::" + address + "@" + a.account + "/" + region,
			Source:        source,
			Address:       address,
			ConfigAddress: address,
			Mode:          graph.ModeManaged,
			Type:          a.terraformType(),
			Name:          names[arn],
			Status:        graph.StatusUnmanaged,
			Account:       a.account,
			Region:        region,
			CloudID:       arn,
		}
		if err := g.AddNode(n); err != nil {
			return res, fmt.Errorf("discover: %s: %w", source, err)
		}
		res.Unmanaged++
	}
	return res, nil
}

// arn is an ARN taken apart: arn:partition:service:region:account:resource,
// where resource is "kind/name", "kind:name" or just a name.
type arn struct {
	service, region, account string
	kind, resource           string
}

func parseARN(s string) (arn, bool) {
	parts := strings.SplitN(s, ":", 6)
	if len(parts) != 6 || parts[0] != "arn" || parts[5] == "" {
		return arn{}, false
	}
	a := arn{service: parts[2], region: parts[3], account: parts[4], resource: parts[5]}
	if i := strings.IndexAny(a.resource, "/:"); i > 0 {
		a.kind, a.resource = a.resource[:i], a.resource[i+1:]
	}
	return a, true
}

// terraformType returns the Terraform resource type for the kind of thing
// an ARN names, where it is one of the common ones, so that filters and
// groupings treat it like its managed neighbours. Otherwise it returns a
// name made from the ARN, such as "aws:glue:crawler".
func (a arn) terraformType() string {
	key := a.service
	if a.kind != "" {
		key += ":" + a.kind
	}
	if t, ok := arnTypes[key]; ok {
		return t
	}
	return "aws:" + key
}

var arnTypes = map[string]string{
	"acm:certificate":                   "aws_acm_certificate",
	"cloudfront:distribution":           "aws_cloudfront_distribution",
	"dynamodb:table":                    "aws_dynamodb_table",
	"ec2:instance":                      "aws_instance",
	"ec2:internet-gateway":              "aws_internet_gateway",
	"ec2:natgateway":                    "aws_nat_gateway",
	"ec2:network-interface":             "aws_network_interface",
	"ec2:route-table":                   "aws_route_table",
	"ec2:security-group":                "aws_security_group",
	"ec2:subnet":                        "aws_subnet",
	"ec2:volume":                        "aws_ebs_volume",
	"ec2:vpc":                           "aws_vpc",
	"ecr:repository":                    "aws_ecr_repository",
	"ecs:cluster":                       "aws_ecs_cluster",
	"ecs:service":                       "aws_ecs_service",
	"eks:cluster":                       "aws_eks_cluster",
	"elasticloadbalancing:loadbalancer": "aws_lb",
	"iam:policy":                        "aws_iam_policy",
	"iam:role":                          "aws_iam_role",
	"iam:user":                          "aws_iam_user",
	"kms:key":                           "aws_kms_key",
	"lambda:function":                   "aws_lambda_function",
	"logs:log-group":                    "aws_cloudwatch_log_group",
	"rds:cluster":                       "aws_rds_cluster",
	"rds:db":                            "aws_db_instance",
	"route53:hostedzone":                "aws_route53_zone",
	"s3":                                "aws_s3_bucket",
	"secretsmanager:secret":             "aws_secretsmanager_secret",
	"sns":                               "aws_sns_topic",
	"sqs":                               "aws_sqs_queue",
}
