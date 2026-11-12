// Package azureid takes apart Azure resource IDs, which look like
//
//	/subscriptions/<id>/resourceGroups/<name>/providers/<namespace>/<type>/<name>[/<type>/<name>...]
//
// Azure compares them without regard to case and does not always return
// one spelled the way it was given, so two IDs are the same resource when
// their canonical forms are equal. The ID of a resource that is part of
// another, such as a subnet of a network, starts with the other's.
package azureid

import "strings"

const prefix = "/subscriptions/"

// Is reports whether s is an Azure resource ID.
func Is(s string) bool {
	return len(s) > len(prefix) && strings.EqualFold(s[:len(prefix)], prefix)
}

// Canonical returns s in the form to compare it by: in lower case if it is
// an Azure resource ID, and unchanged otherwise.
func Canonical(s string) string {
	if !Is(s) {
		return s
	}
	return strings.ToLower(s)
}

// Part returns the segment of id that follows the one named, such as the
// subscription after "subscriptions" or the group after "resourceGroups".
// It is empty if id is not an Azure resource ID or has no such segment.
func Part(id, after string) string {
	if !Is(id) {
		return ""
	}
	parts := strings.Split(id, "/")
	for i := 1; i+1 < len(parts); i += 2 {
		if strings.EqualFold(parts[i], after) {
			return parts[i+1]
		}
	}
	return ""
}

// Parent returns the ID of the resource that id is part of, and false if
// it is part of none: a subnet's parent is its network, and a network has
// no parent, since a resource group only holds it.
func Parent(id string) (string, bool) {
	if !Is(id) {
		return "", false
	}
	// Past the provider namespace, an ID is pairs of type and name. The
	// first pair is the resource at the top; each further pair is a part.
	// The last namespace counts: something attached to a resource, such
	// as a lock, has the resource's whole ID in front of its own.
	i := strings.LastIndex(strings.ToLower(id), "/providers/")
	if i < 0 {
		return "", false
	}
	rest := strings.Split(id[i+len("/providers/"):], "/")
	if len(rest) < 5 || len(rest)%2 == 0 {
		return "", false
	}
	return id[:len(id)-len(rest[len(rest)-2])-len(rest[len(rest)-1])-2], true
}
