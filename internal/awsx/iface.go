// Package awsx wraps the AWS calls the dashboard makes. Every service is
// reached through a narrow interface declared here so the logic above can be
// tested against fakes instead of against AWS.
package awsx

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	"github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
	"github.com/aws/aws-sdk-go-v2/service/rds"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/aws/aws-sdk-go-v2/service/wafv2"
)

// MetricsAPI is the slice of CloudWatch the metric fetcher uses.
type MetricsAPI interface {
	GetMetricData(context.Context, *cloudwatch.GetMetricDataInput, ...func(*cloudwatch.Options)) (*cloudwatch.GetMetricDataOutput, error)
}

// LogsAPI is the slice of CloudWatch Logs the Insights runner uses.
//
// StopQuery is part of the interface because cancelling a query is not
// optional: an abandoned query keeps holding one of the account's concurrent
// query slots until it finishes on its own.
type LogsAPI interface {
	StartQuery(context.Context, *cloudwatchlogs.StartQueryInput, ...func(*cloudwatchlogs.Options)) (*cloudwatchlogs.StartQueryOutput, error)
	GetQueryResults(context.Context, *cloudwatchlogs.GetQueryResultsInput, ...func(*cloudwatchlogs.Options)) (*cloudwatchlogs.GetQueryResultsOutput, error)
	StopQuery(context.Context, *cloudwatchlogs.StopQueryInput, ...func(*cloudwatchlogs.Options)) (*cloudwatchlogs.StopQueryOutput, error)
}

// LogGroupsAPI lists log groups for the discovery page.
type LogGroupsAPI interface {
	DescribeLogGroups(context.Context, *cloudwatchlogs.DescribeLogGroupsInput, ...func(*cloudwatchlogs.Options)) (*cloudwatchlogs.DescribeLogGroupsOutput, error)
}

// LogsClient is everything the dashboard asks of CloudWatch Logs.
type LogsClient interface {
	LogsAPI
	LogGroupsAPI
}

// IdentityAPI validates the configured credentials.
type IdentityAPI interface {
	GetCallerIdentity(context.Context, *sts.GetCallerIdentityInput, ...func(*sts.Options)) (*sts.GetCallerIdentityOutput, error)
}

// LoadBalancerAPI lists target groups and their load balancers.
type LoadBalancerAPI interface {
	DescribeTargetGroups(context.Context, *elasticloadbalancingv2.DescribeTargetGroupsInput, ...func(*elasticloadbalancingv2.Options)) (*elasticloadbalancingv2.DescribeTargetGroupsOutput, error)
	DescribeLoadBalancers(context.Context, *elasticloadbalancingv2.DescribeLoadBalancersInput, ...func(*elasticloadbalancingv2.Options)) (*elasticloadbalancingv2.DescribeLoadBalancersOutput, error)
}

// ProxyAPI lists RDS proxies.
type ProxyAPI interface {
	DescribeDBProxies(context.Context, *rds.DescribeDBProxiesInput, ...func(*rds.Options)) (*rds.DescribeDBProxiesOutput, error)
}

// WAFAPI lists web ACLs.
type WAFAPI interface {
	ListWebACLs(context.Context, *wafv2.ListWebACLsInput, ...func(*wafv2.Options)) (*wafv2.ListWebACLsOutput, error)
}

// ClusterAPI reads cluster and node group shape. Node group scaling limits are
// the only place the minimum and maximum node counts exist — CloudWatch
// publishes the current count and nothing else.
type ClusterAPI interface {
	ListClusters(context.Context, *eks.ListClustersInput, ...func(*eks.Options)) (*eks.ListClustersOutput, error)
	ListNodegroups(context.Context, *eks.ListNodegroupsInput, ...func(*eks.Options)) (*eks.ListNodegroupsOutput, error)
	DescribeNodegroup(context.Context, *eks.DescribeNodegroupInput, ...func(*eks.Options)) (*eks.DescribeNodegroupOutput, error)
}

// Compile-time proof that the real clients satisfy the interfaces above. If an
// SDK upgrade changes a signature, this fails at build time rather than at the
// first call.
var (
	_ MetricsAPI      = (*cloudwatch.Client)(nil)
	_ LogsClient      = (*cloudwatchlogs.Client)(nil)
	_ IdentityAPI     = (*sts.Client)(nil)
	_ LoadBalancerAPI = (*elasticloadbalancingv2.Client)(nil)
	_ ProxyAPI        = (*rds.Client)(nil)
	_ WAFAPI          = (*wafv2.Client)(nil)
	_ ClusterAPI      = (*eks.Client)(nil)
)
