// Package googleid takes apart the names Google Cloud gives resources.
//
// One resource is written several ways. Terraform records a self link,
//
//	https://www.googleapis.com/compute/v1/projects/acme/regions/europe-west1/subnetworks/web
//
// and an ID, which is the same without the address in front:
//
//	projects/acme/regions/europe-west1/subnetworks/web
//
// A resource that refers to another may hold either, or only the last
// part, the name. The asset inventory puts the service in front instead:
//
//	//compute.googleapis.com/projects/acme/regions/europe-west1/subnetworks/web
//
// Canonical reduces the first, second and last to one form, and Name gives
// the last part.
package googleid

import "strings"

// Canonical returns s from "projects/" on, without the address, service
// or API version in front, if s names something in a project. Anything
// else is returned unchanged.
func Canonical(s string) string {
	if !strings.HasPrefix(s, "https://") && !strings.HasPrefix(s, "//") && !strings.HasPrefix(s, "projects/") {
		return s
	}
	if i := strings.Index(s, "projects/"); i >= 0 && (i == 0 || s[i-1] == '/') {
		return s[i:]
	}
	return s
}

// Is reports whether s names something in a project, in any of the ways
// Canonical understands.
func Is(s string) bool {
	return strings.HasPrefix(Canonical(s), "projects/")
}

// Part returns the segment of a name that follows the one given, such as
// the project after "projects" or the region after "regions". It is empty
// if there is none.
func Part(s, after string) string {
	parts := strings.Split(Canonical(s), "/")
	for i := 0; i+1 < len(parts); i++ {
		if parts[i] == after {
			return parts[i+1]
		}
	}
	return ""
}

// Global reports whether the name is of a resource that belongs to no
// region, which Google writes as "global" where the region would be.
func Global(s string) bool {
	return strings.Contains(Canonical(s), "/global/")
}

// Name returns the last part of s: the resource's own name.
func Name(s string) string {
	return s[strings.LastIndex(s, "/")+1:]
}
