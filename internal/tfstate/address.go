package tfstate

import "strings"

// skipIndex takes the position of a '[' in addr and returns the position
// just past its matching ']'. Quoting is respected, because a string key may
// itself contain brackets or dots.
func skipIndex(addr string, i int) int {
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
			return i + 1
		}
	}
	return len(addr)
}

// configAddress removes instance keys from a resource address, so
// `module.a["x"].aws_subnet.s[0]` becomes `module.a.aws_subnet.s`.
func configAddress(addr string) string {
	var b strings.Builder
	for i := 0; i < len(addr); {
		if addr[i] == '[' {
			i = skipIndex(addr, i)
			continue
		}
		b.WriteByte(addr[i])
		i++
	}
	return b.String()
}

// moduleStep is one module call on the way to a resource: its name and, for
// a call with count or for_each, the instance key including brackets.
type moduleStep struct{ name, key string }

// moduleSteps splits a module instance path such as
// `module.a["x"].module.b[0]` into its steps. The root module has none.
func moduleSteps(module string) []moduleStep {
	var steps []moduleStep
	for i := 0; strings.HasPrefix(module[i:], "module."); {
		i += len("module.")
		start := i
		for i < len(module) && module[i] != '.' && module[i] != '[' {
			i++
		}
		step := moduleStep{name: module[start:i]}
		if i < len(module) && module[i] == '[' {
			end := skipIndex(module, i)
			step.key = module[i:end]
			i = end
		}
		steps = append(steps, step)
		if i < len(module) && module[i] == '.' {
			i++
		}
	}
	return steps
}

// moduleNames returns the module call names leading a config address, so
// `module.a.module.b.aws_vpc.main` gives [a b].
func moduleNames(configAddr string) []string {
	parts := strings.Split(configAddr, ".")
	var names []string
	for i := 0; i+1 < len(parts) && parts[i] == "module"; i += 2 {
		names = append(names, parts[i+1])
	}
	return names
}

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
