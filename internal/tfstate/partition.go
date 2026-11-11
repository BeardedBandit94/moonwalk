package tfstate

import (
	"strings"

	"github.com/BeardedBandit94/moonwalk/internal/graph"
)

// partition derives the owning account and the region of a resource from its
// attributes. It is best effort and returns empty strings when the provider
// is not recognised or the attributes do not say.
//
// It runs on redacted attributes, so a value the state marks as sensitive is
// never used.
func partition(provider, resourceType string, attrs map[string]any) (account, region string) {
	switch lastSegment(provider) {
	case "aws":
		return awsPartition(resourceType, attrs)
	case "azurerm":
		return azurePartition(attrs)
	case "google", "google-beta":
		return googlePartition(attrs)
	}
	return "", ""
}

// infersRegion reports whether a resource of this provider that states no
// region can take the one its provider's other resources agree on. That
// holds for AWS: a provider configuration works in one region, unless the
// resource names another, which it then states.
func infersRegion(provider string) bool {
	return lastSegment(provider) == "aws"
}

// awsPartition reads the ARN (arn:partition:service:region:account:resource),
// preferring explicit attributes where the ARN leaves a field empty, as S3
// bucket ARNs do. A resource of a service that has no regions, or whose ARN
// has none and which names none, is global.
func awsPartition(resourceType string, attrs map[string]any) (account, region string) {
	arn := strings.SplitN(str(attrs, "arn"), ":", 6)
	hasARN := len(arn) == 6 && arn[0] == "arn"
	if hasARN {
		region, account = arn[3], arn[4]
	}
	if r := str(attrs, "region"); r != "" {
		region = r
	}
	if region == "" && (hasARN || awsGlobalType(resourceType)) {
		region = graph.RegionGlobal
	}
	if !isAWSAccountID(account) {
		// Some ARNs carry "aws" or nothing in the account field.
		account = ""
		for _, key := range []string{"owner_id", "account_id"} {
			if v := str(attrs, key); isAWSAccountID(v) {
				account = v
				break
			}
		}
	}
	return account, region
}

// awsGlobalType reports whether a resource type belongs to an AWS service
// that is not tied to a region. Many such resources have no ARN attribute
// to tell by.
func awsGlobalType(resourceType string) bool {
	for _, prefix := range []string{
		"aws_iam_", "aws_cloudfront_", "aws_organizations_", "aws_account_",
		"aws_globalaccelerator_", "aws_budgets_",
	} {
		if strings.HasPrefix(resourceType, prefix) {
			return true
		}
	}
	// Route 53 is global except for its resolver, which lives in a VPC.
	return strings.HasPrefix(resourceType, "aws_route53_") && !strings.HasPrefix(resourceType, "aws_route53_resolver_")
}

func isAWSAccountID(s string) bool {
	if len(s) != 12 {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// azurePartition takes the subscription from the resource ID
// (/subscriptions/<id>/resourceGroups/...) and the region from location.
func azurePartition(attrs map[string]any) (account, region string) {
	parts := strings.Split(str(attrs, "id"), "/")
	if len(parts) >= 3 && parts[0] == "" && strings.EqualFold(parts[1], "subscriptions") {
		account = parts[2]
	}
	return account, str(attrs, "location")
}

// googlePartition uses project as the account. Region comes from region,
// else from the zone with its suffix removed, else from location. Values may
// be self links, so only the last path segment counts.
func googlePartition(attrs map[string]any) (account, region string) {
	account = lastSegment(str(attrs, "project"))
	if r := lastSegment(str(attrs, "region")); r != "" {
		return account, r
	}
	if z := lastSegment(str(attrs, "zone")); z != "" {
		if i := strings.LastIndex(z, "-"); i > 0 {
			return account, z[:i]
		}
	}
	return account, lastSegment(str(attrs, "location"))
}

// str returns attrs[key] if it is a string that was not redacted, and ""
// otherwise.
func str(attrs map[string]any, key string) string {
	s, _ := attrs[key].(string)
	if s == Redacted {
		return ""
	}
	return s
}

func lastSegment(s string) string {
	return s[strings.LastIndex(s, "/")+1:]
}
