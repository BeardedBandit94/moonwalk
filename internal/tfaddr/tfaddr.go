// Package tfaddr reads Terraform resource addresses such as
// `module.a["x"].aws_subnet.private[0]`.
package tfaddr

import "strings"

// SkipIndex takes the position of a '[' in addr and returns the position
// just past its matching ']'. Quoting is respected, because a string key may
// itself contain brackets or dots.
func SkipIndex(addr string, i int) int {
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

// ConfigAddress removes instance keys from a resource address, so
// `module.a["x"].aws_subnet.s[0]` becomes `module.a.aws_subnet.s`.
func ConfigAddress(addr string) string {
	var b strings.Builder
	for i := 0; i < len(addr); {
		if addr[i] == '[' {
			i = SkipIndex(addr, i)
			continue
		}
		b.WriteByte(addr[i])
		i++
	}
	return b.String()
}

// Block removes the instance key of the resource itself, keeping those of
// its modules: `module.a["x"].aws_subnet.s[0]` becomes
// `module.a["x"].aws_subnet.s`. keyed reports whether there was a key.
func Block(addr string) (block string, keyed bool) {
	for i := 0; i < len(addr); i++ {
		if addr[i] != '[' {
			continue
		}
		end := SkipIndex(addr, i)
		if end == len(addr) {
			return addr[:i], true
		}
		i = end - 1
	}
	return addr, false
}

// ModuleStep is one module call on the way to a resource: its name and, for
// a call with count or for_each, the instance key including brackets.
type ModuleStep struct{ Name, Key string }

// ModuleSteps splits a module instance path such as
// `module.a["x"].module.b[0]` into its steps. The root module has none.
func ModuleSteps(module string) []ModuleStep {
	var steps []ModuleStep
	for i := 0; strings.HasPrefix(module[i:], "module."); {
		i += len("module.")
		start := i
		for i < len(module) && module[i] != '.' && module[i] != '[' {
			i++
		}
		step := ModuleStep{Name: module[start:i]}
		if i < len(module) && module[i] == '[' {
			end := SkipIndex(module, i)
			step.Key = module[i:end]
			i = end
		}
		steps = append(steps, step)
		if i < len(module) && module[i] == '.' {
			i++
		}
	}
	return steps
}

// ModuleNames returns the module call names leading a config address, so
// `module.a.module.b.aws_vpc.main` gives [a b].
func ModuleNames(configAddr string) []string {
	parts := strings.Split(configAddr, ".")
	var names []string
	for i := 0; i+1 < len(parts) && parts[i] == "module"; i += 2 {
		names = append(names, parts[i+1])
	}
	return names
}
