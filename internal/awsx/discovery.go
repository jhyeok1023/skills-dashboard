package awsx

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	"github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
	"github.com/aws/aws-sdk-go-v2/service/rds"
	"github.com/aws/aws-sdk-go-v2/service/wafv2"
	waftypes "github.com/aws/aws-sdk-go-v2/service/wafv2/types"
)

// maxDiscoveryPages bounds every List walk. Discovery runs behind a settings
// page, so a bounded, possibly incomplete list beats an unbounded walk that
// holds the request open.
const maxDiscoveryPages = 20

// Resource is one selectable AWS resource.
type Resource struct {
	// ID is what goes into the config: the CloudWatch dimension value, not
	// the full ARN, so it can be used directly in a SEARCH expression.
	ID   string `json:"id"`
	Name string `json:"name"`
	ARN  string `json:"arn,omitempty"`
	// Extra carries whatever else the UI should show, such as the load
	// balancer a target group belongs to.
	Extra map[string]string `json:"extra,omitempty"`
}

func sortResources(rs []Resource) []Resource {
	sort.Slice(rs, func(i, j int) bool { return rs[i].Name < rs[j].Name })
	return rs
}

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

// TargetGroups lists the target groups in the region, annotated with the load
// balancer each one is attached to.
func TargetGroups(ctx context.Context, api LoadBalancerAPI) ([]Resource, error) {
	lbNames := map[string]string{}
	var lbIn elasticloadbalancingv2.DescribeLoadBalancersInput
	for page := 0; page < maxDiscoveryPages; page++ {
		out, err := api.DescribeLoadBalancers(ctx, &lbIn)
		if err != nil {
			return nil, fmt.Errorf("DescribeLoadBalancers: %w", err)
		}
		for _, lb := range out.LoadBalancers {
			lbNames[aws.ToString(lb.LoadBalancerArn)] = aws.ToString(lb.LoadBalancerName)
		}
		if out.NextMarker == nil || *out.NextMarker == "" {
			break
		}
		lbIn.Marker = out.NextMarker
	}

	var out []Resource
	var in elasticloadbalancingv2.DescribeTargetGroupsInput
	for page := 0; page < maxDiscoveryPages; page++ {
		resp, err := api.DescribeTargetGroups(ctx, &in)
		if err != nil {
			return nil, fmt.Errorf("DescribeTargetGroups: %w", err)
		}
		for _, tg := range resp.TargetGroups {
			arn := aws.ToString(tg.TargetGroupArn)
			name := aws.ToString(tg.TargetGroupName)
			r := Resource{
				ID:    TargetGroupDimension(arn),
				Name:  name,
				ARN:   arn,
				Extra: map[string]string{"friendlyName": FriendlyTargetGroupName(name)},
			}
			if len(tg.LoadBalancerArns) > 0 {
				lbArn := tg.LoadBalancerArns[0]
				r.Extra["loadBalancer"] = LoadBalancerDimension(lbArn)
				r.Extra["loadBalancerName"] = lbNames[lbArn]
			}
			out = append(out, r)
		}
		if resp.NextMarker == nil || *resp.NextMarker == "" {
			break
		}
		in.Marker = resp.NextMarker
	}
	return sortResources(out), nil
}

// LogGroups lists log groups whose name starts with prefix.
func LogGroups(ctx context.Context, api LogGroupsAPI, prefix string) ([]Resource, error) {
	in := &cloudwatchlogs.DescribeLogGroupsInput{Limit: aws.Int32(50)}
	if prefix != "" {
		in.LogGroupNamePrefix = aws.String(prefix)
	}

	var out []Resource
	for page := 0; page < maxDiscoveryPages; page++ {
		resp, err := api.DescribeLogGroups(ctx, in)
		if err != nil {
			return nil, fmt.Errorf("DescribeLogGroups: %w", err)
		}
		for _, lg := range resp.LogGroups {
			name := aws.ToString(lg.LogGroupName)
			out = append(out, Resource{ID: name, Name: name, ARN: aws.ToString(lg.Arn)})
		}
		if resp.NextToken == nil || *resp.NextToken == "" {
			break
		}
		in.NextToken = resp.NextToken
	}
	return sortResources(out), nil
}

// RDSProxies lists the RDS proxies in the region.
func RDSProxies(ctx context.Context, api ProxyAPI) ([]Resource, error) {
	var out []Resource
	var in rds.DescribeDBProxiesInput
	for page := 0; page < maxDiscoveryPages; page++ {
		resp, err := api.DescribeDBProxies(ctx, &in)
		if err != nil {
			return nil, fmt.Errorf("DescribeDBProxies: %w", err)
		}
		for _, p := range resp.DBProxies {
			name := aws.ToString(p.DBProxyName)
			out = append(out, Resource{
				ID:    name,
				Name:  name,
				ARN:   aws.ToString(p.DBProxyArn),
				Extra: map[string]string{"engine": aws.ToString(p.EngineFamily), "status": string(p.Status)},
			})
		}
		if resp.Marker == nil || *resp.Marker == "" {
			break
		}
		in.Marker = resp.Marker
	}
	return sortResources(out), nil
}

// WebACLs lists web ACLs for one scope. REGIONAL ACLs live in the working
// region; CLOUDFRONT ACLs only exist in us-east-1.
func WebACLs(ctx context.Context, api WAFAPI, scope waftypes.Scope) ([]Resource, error) {
	var out []Resource
	in := &wafv2.ListWebACLsInput{Scope: scope, Limit: aws.Int32(100)}
	for page := 0; page < maxDiscoveryPages; page++ {
		resp, err := api.ListWebACLs(ctx, in)
		if err != nil {
			return nil, fmt.Errorf("ListWebACLs(%s): %w", scope, err)
		}
		for _, acl := range resp.WebACLs {
			name := aws.ToString(acl.Name)
			out = append(out, Resource{
				ID:    name, // the WebACL CloudWatch dimension is the name
				Name:  name,
				ARN:   aws.ToString(acl.ARN),
				Extra: map[string]string{"scope": string(scope), "id": aws.ToString(acl.Id)},
			})
		}
		if resp.NextMarker == nil || *resp.NextMarker == "" {
			break
		}
		in.NextMarker = resp.NextMarker
	}
	return sortResources(out), nil
}

// Clusters lists EKS clusters.
func Clusters(ctx context.Context, api ClusterAPI) ([]Resource, error) {
	var out []Resource
	var in eks.ListClustersInput
	for page := 0; page < maxDiscoveryPages; page++ {
		resp, err := api.ListClusters(ctx, &in)
		if err != nil {
			return nil, fmt.Errorf("ListClusters: %w", err)
		}
		for _, name := range resp.Clusters {
			out = append(out, Resource{
				ID:    name,
				Name:  name,
				Extra: map[string]string{"logGroup": "/aws/containerinsights/" + name + "/application"},
			})
		}
		if resp.NextToken == nil || *resp.NextToken == "" {
			break
		}
		in.NextToken = resp.NextToken
	}
	return sortResources(out), nil
}

// NodeScaling is a cluster's node count limits, summed across its node groups.
//
// CloudWatch publishes the current node count and nothing else, so this is the
// only source for the minimum and maximum the pod/node count panel shows. It is
// read per request rather than captured once: the reference implementation
// cached these at login, which left the displayed limits permanently wrong the
// moment a node group was rescaled.
type NodeScaling struct {
	Min     int32    `json:"min"`
	Max     int32    `json:"max"`
	Desired int32    `json:"desired"`
	Groups  []string `json:"groups"`
}

// ClusterNodeScaling sums the scaling configuration of every node group.
func ClusterNodeScaling(ctx context.Context, api ClusterAPI, cluster string) (NodeScaling, error) {
	if api == nil {
		return NodeScaling{}, fmt.Errorf("EKS client is not configured")
	}
	if cluster == "" {
		return NodeScaling{}, fmt.Errorf("no cluster configured")
	}

	var names []string
	in := &eks.ListNodegroupsInput{ClusterName: aws.String(cluster)}
	for page := 0; page < maxDiscoveryPages; page++ {
		resp, err := api.ListNodegroups(ctx, in)
		if err != nil {
			return NodeScaling{}, fmt.Errorf("ListNodegroups: %w", err)
		}
		names = append(names, resp.Nodegroups...)
		if resp.NextToken == nil || *resp.NextToken == "" {
			break
		}
		in.NextToken = resp.NextToken
	}

	out := NodeScaling{Groups: names}
	for _, name := range names {
		resp, err := api.DescribeNodegroup(ctx, &eks.DescribeNodegroupInput{
			ClusterName:   aws.String(cluster),
			NodegroupName: aws.String(name),
		})
		if err != nil {
			return NodeScaling{}, fmt.Errorf("DescribeNodegroup %s: %w", name, err)
		}
		if resp.Nodegroup == nil || resp.Nodegroup.ScalingConfig == nil {
			continue
		}
		sc := resp.Nodegroup.ScalingConfig
		out.Min += aws.ToInt32(sc.MinSize)
		out.Max += aws.ToInt32(sc.MaxSize)
		out.Desired += aws.ToInt32(sc.DesiredSize)
	}
	sort.Strings(out.Groups)
	return out, nil
}
