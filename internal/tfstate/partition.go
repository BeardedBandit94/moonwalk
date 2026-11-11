package tfstate

import "strings"

// partition derives the owning account and the region of a resource from its
// attributes. It is best effort and returns empty strings when the provider
// is not recognised or the attributes do not say.
//
// Only these two values leave the adapter; the attributes themselves are not
// kept, because state stores secrets in plain text.
func partition(provider string, attrs map[string]any) (account, region string) {
	switch lastSegment(provider) {
	case "aws":
		return awsPartition(attrs)
	case "azurerm":
		return azurePartition(attrs)
	case "google", "google-beta":
		return googlePartition(attrs)
	}
	return "", ""
}

// awsPartition reads the ARN (arn:partition:service:region:account:resource),
// preferring explicit attributes where the ARN leaves a field empty, as S3
// bucket ARNs do.
func awsPartition(attrs map[string]any) (account, region string) {
	if parts := strings.SplitN(str(attrs, "arn"), ":", 6); len(parts) == 6 && parts[0] == "arn" {
		region, account = parts[3], parts[4]
	}
	if r := str(attrs, "region"); r != "" {
		region = r
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

// str returns attrs[key] if it is a string, and "" otherwise.
func str(attrs map[string]any, key string) string {
	s, _ := attrs[key].(string)
	return s
}

func lastSegment(s string) string {
	return s[strings.LastIndex(s, "/")+1:]
}
