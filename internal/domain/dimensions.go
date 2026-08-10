package domain

import "strings"

// Resource identifiers, in the spelling CloudWatch uses.
//
// These live in domain rather than beside the AWS calls that produce them
// because they are also what a saved config must hold: the settings layer
// normalises whatever an operator pasted into the same form discovery yields,
// and awsx already imports config, so the conversion cannot live there.

// TargetGroupDimension converts a target group ARN into the value CloudWatch
// uses as the TargetGroup dimension, which is the ARN's trailing path.
//
//	arn:aws:elasticloadbalancing:ap-northeast-2:1:targetgroup/k8s-default-app/abc
//	                                            ^------------ this part ------^
func TargetGroupDimension(arn string) string {
	if i := strings.Index(arn, ":targetgroup/"); i >= 0 {
		return arn[i+1:]
	}
	return arn
}

// LoadBalancerDimension converts a load balancer ARN into CloudWatch's
// LoadBalancer dimension value.
//
//	arn:...:loadbalancer/app/my-alb/abc  ->  app/my-alb/abc
func LoadBalancerDimension(arn string) string {
	if i := strings.Index(arn, ":loadbalancer/"); i >= 0 {
		return arn[i+len(":loadbalancer/"):]
	}
	return arn
}

// FriendlyTargetGroupName strips the generated suffix off a Kubernetes-managed
// target group so the UI can label it with the service it fronts.
//
//	k8s-default-product-d6d507c878  ->  product
func FriendlyTargetGroupName(name string) string {
	parts := strings.Split(name, "-")
	if len(parts) < 4 || parts[0] != "k8s" {
		return name
	}
	return strings.Join(parts[2:len(parts)-1], "-")
}
