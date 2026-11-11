package tfstate

import "strings"

// providerSource reduces a raw-state provider reference such as
// `module.a.provider["registry.terraform.io/hashicorp/aws"].west` to the
// provider source address, matching what `terraform show -json` reports.
func providerSource(ref string) string {
	open := strings.Index(ref, `["`)
	end := strings.LastIndex(ref, `"]`)
	if open < 0 || end < open+2 {
		return ref // Includes `["]`, where the two markers overlap.
	}
	return ref[open+2 : end]
}
