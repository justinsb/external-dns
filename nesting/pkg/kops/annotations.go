package kops

import "strings"

// AnnotationNameDNSExternal is used to set up a DNS name for accessing the resource from outside the cluster
// For a service of Type=LoadBalancer, it would map to the external LB hostname or IP
const AnnotationNameDNSExternal = "dns.alpha.kubernetes.io/external"

// AnnotationNameDNSInternal is used to set up a DNS name for accessing the resource from inside the cluster
// This is only supported on Pods currently, and maps to the Internal address
const AnnotationNameDNSInternal = "dns.alpha.kubernetes.io/internal"

func SplitAnnotation(v string) []string {
	if v == "" {
		return nil
	}

	tokens := strings.Split(v, ",")
	for i := range tokens {
		tokens[i] = strings.TrimSpace(tokens[i])
		tokens[i] = strings.TrimSuffix(tokens[i], ".")
	}
	return tokens
}
