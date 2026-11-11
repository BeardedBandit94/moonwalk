package tfstate

import "strings"

// configAddress removes instance keys from a resource address, so
// `module.a["x"].aws_subnet.s[0]` becomes `module.a.aws_subnet.s`.
// Keys are skipped with quoting respected, because a string key may itself
// contain brackets or dots.
func configAddress(addr string) string {
	var b strings.Builder
	for i := 0; i < len(addr); i++ {
		if addr[i] != '[' {
			b.WriteByte(addr[i])
			continue
		}
		inQuote := false
		for i++; i < len(addr); i++ {
			c := addr[i]
			if inQuote {
				if c == '\\' {
					i++
				} else if c == '"' {
					inQuote = false
				}
				continue
			}
			if c == '"' {
				inQuote = true
			} else if c == ']' {
				break
			}
		}
	}
	return b.String()
}

// providerSource reduces a raw-state provider reference such as
// `module.a.provider["registry.terraform.io/hashicorp/aws"].west` to the
// provider source address, matching what `terraform show -json` reports.
func providerSource(ref string) string {
	open := strings.Index(ref, `["`)
	end := strings.LastIndex(ref, `"]`)
	if open < 0 || end <= open {
		return ref
	}
	return ref[open+2 : end]
}
