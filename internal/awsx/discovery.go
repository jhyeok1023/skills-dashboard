package awsx

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	"github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
	elbtypes "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2/types"
	"github.com/aws/aws-sdk-go-v2/service/rds"
	"github.com/aws/aws-sdk-go-v2/service/wafv2"
	waftypes "github.com/aws/aws-sdk-go-v2/service/wafv2/types"

	"github.com/jhyeok1023/skills-dashboard/internal/domain"
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

// Listing is a discovery result together with whether the walk was cut short.
//
// A capped list that says nothing about being capped is worse than a short one:
// the resource the operator is looking for is simply not offered, and the page
// gives them no reason to doubt the list they are reading. Every walk here is
// bounded, so every walk has to be able to say it ran into the bound.
type Listing struct {
	Resources []Resource
	Truncated bool
}

// sorted finishes a walk: the resources in name order, plus whether the page
// cap stopped it early.
func sorted(rs []Resource, truncated bool) Listing {
	sort.Slice(rs, func(i, j int) bool { return rs[i].Name < rs[j].Name })
	return Listing{Resources: rs, Truncated: truncated}
}

// describeLoadBalancers walks every page of load balancers in the region. Both
// the load balancer list and the target group list need them, and paging twice
// against the same API is the kind of duplication that drifts.
func describeLoadBalancers(ctx context.Context, api LoadBalancerAPI) ([]elbtypes.LoadBalancer, bool, error) {
	var out []elbtypes.LoadBalancer
	var in elasticloadbalancingv2.DescribeLoadBalancersInput
	for page := 0; page < maxDiscoveryPages; page++ {
		resp, err := api.DescribeLoadBalancers(ctx, &in)
		if err != nil {
			return nil, false, fmt.Errorf("DescribeLoadBalancers: %w", err)
		}
		out = append(out, resp.LoadBalancers...)
		if resp.NextMarker == nil || *resp.NextMarker == "" {
			return out, false, nil
		}
		in.Marker = resp.NextMarker
	}
	return out, true, nil
}

// describeTargetGroups walks every page of target groups in the region.
func describeTargetGroups(ctx context.Context, api LoadBalancerAPI) ([]elbtypes.TargetGroup, bool, error) {
	var out []elbtypes.TargetGroup
	var in elasticloadbalancingv2.DescribeTargetGroupsInput
	for page := 0; page < maxDiscoveryPages; page++ {
		resp, err := api.DescribeTargetGroups(ctx, &in)
		if err != nil {
			return nil, false, fmt.Errorf("DescribeTargetGroups: %w", err)
		}
		out = append(out, resp.TargetGroups...)
		if resp.NextMarker == nil || *resp.NextMarker == "" {
			return out, false, nil
		}
		in.Marker = resp.NextMarker
	}
	return out, true, nil
}

// LoadBalancers lists the load balancers in the region.
//
// The ID is the CloudWatch dimension value rather than the ARN, so picking one
// in the settings page writes something the metric SEARCH can actually use. An
// ARN pasted into that field passes the value regex and then matches nothing,
// which is why the list exists at all.
func LoadBalancers(ctx context.Context, api LoadBalancerAPI) (Listing, error) {
	lbs, truncated, err := describeLoadBalancers(ctx, api)
	if err != nil {
		return Listing{}, err
	}

	out := make([]Resource, 0, len(lbs))
	for _, lb := range lbs {
		arn := aws.ToString(lb.LoadBalancerArn)
		out = append(out, Resource{
			ID:   domain.LoadBalancerDimension(arn),
			Name: aws.ToString(lb.LoadBalancerName),
			ARN:  arn,
			Extra: map[string]string{
				"type":    string(lb.Type),
				"scheme":  string(lb.Scheme),
				"dnsName": aws.ToString(lb.DNSName),
			},
		})
	}
	return sorted(out, truncated), nil
}

// TargetGroups lists the target groups in the region, annotated with the load
// balancer each one is attached to.
//
// The two walks run at the same time. They are independent — the load balancer
// list is only consumed at the end, to put a name on each group's ARN — and
// with one target group per application the list is long enough that doing them
// in sequence is felt: the settings button spends the sum of the two rather
// than the longer of them. Either failing cancels the other, so a walk whose
// result is already destined for the bin stops paging; the cancelled sibling
// reports "context canceled" alongside the failure that caused it, which is
// what happened.
func TargetGroups(ctx context.Context, api LoadBalancerAPI) (Listing, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var (
		wg          sync.WaitGroup
		lbs         []elbtypes.LoadBalancer
		lbTruncated bool
		lbErr       error

		groups      []elbtypes.TargetGroup
		tgTruncated bool
		tgErr       error
	)

	wg.Add(2)
	go func() {
		defer wg.Done()
		if lbs, lbTruncated, lbErr = describeLoadBalancers(ctx, api); lbErr != nil {
			cancel()
		}
	}()
	go func() {
		defer wg.Done()
		if groups, tgTruncated, tgErr = describeTargetGroups(ctx, api); tgErr != nil {
			cancel()
		}
	}()
	wg.Wait()

	if err := errors.Join(lbErr, tgErr); err != nil {
		return Listing{}, err
	}

	lbNames := make(map[string]string, len(lbs))
	for _, lb := range lbs {
		lbNames[aws.ToString(lb.LoadBalancerArn)] = aws.ToString(lb.LoadBalancerName)
	}

	out := make([]Resource, 0, len(groups))
	for _, tg := range groups {
		arn := aws.ToString(tg.TargetGroupArn)
		name := aws.ToString(tg.TargetGroupName)
		r := Resource{
			ID:    domain.TargetGroupDimension(arn),
			Name:  name,
			ARN:   arn,
			Extra: map[string]string{"friendlyName": domain.FriendlyTargetGroupName(name)},
		}
		if len(tg.LoadBalancerArns) > 0 {
			lbArn := tg.LoadBalancerArns[0]
			r.Extra["loadBalancer"] = domain.LoadBalancerDimension(lbArn)
			r.Extra["loadBalancerName"] = lbNames[lbArn]
		}
		out = append(out, r)
	}
	return sorted(out, lbTruncated || tgTruncated), nil
}

// LogGroups lists log groups whose name starts with prefix.
func LogGroups(ctx context.Context, api LogGroupsAPI, prefix string) (Listing, error) {
	in := &cloudwatchlogs.DescribeLogGroupsInput{Limit: aws.Int32(50)}
	if prefix != "" {
		in.LogGroupNamePrefix = aws.String(prefix)
	}

	var out []Resource
	for page := 0; page < maxDiscoveryPages; page++ {
		resp, err := api.DescribeLogGroups(ctx, in)
		if err != nil {
			return Listing{}, fmt.Errorf("DescribeLogGroups: %w", err)
		}
		for _, lg := range resp.LogGroups {
			name := aws.ToString(lg.LogGroupName)
			out = append(out, Resource{ID: name, Name: name, ARN: aws.ToString(lg.Arn)})
		}
		if resp.NextToken == nil || *resp.NextToken == "" {
			return sorted(out, false), nil
		}
		in.NextToken = resp.NextToken
	}
	return sorted(out, true), nil
}

// RDSProxies lists the RDS proxies in the region.
func RDSProxies(ctx context.Context, api ProxyAPI) (Listing, error) {
	var out []Resource
	var in rds.DescribeDBProxiesInput
	for page := 0; page < maxDiscoveryPages; page++ {
		resp, err := api.DescribeDBProxies(ctx, &in)
		if err != nil {
			return Listing{}, fmt.Errorf("DescribeDBProxies: %w", err)
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
			return sorted(out, false), nil
		}
		in.Marker = resp.Marker
	}
	return sorted(out, true), nil
}

// WebACLs lists web ACLs for one scope. REGIONAL ACLs live in the working
// region; CLOUDFRONT ACLs only exist in us-east-1.
func WebACLs(ctx context.Context, api WAFAPI, scope waftypes.Scope) (Listing, error) {
	var out []Resource
	in := &wafv2.ListWebACLsInput{Scope: scope, Limit: aws.Int32(100)}
	for page := 0; page < maxDiscoveryPages; page++ {
		resp, err := api.ListWebACLs(ctx, in)
		if err != nil {
			return Listing{}, fmt.Errorf("ListWebACLs(%s): %w", scope, err)
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
			return sorted(out, false), nil
		}
		in.NextMarker = resp.NextMarker
	}
	return sorted(out, true), nil
}

// Clusters lists EKS clusters.
func Clusters(ctx context.Context, api ClusterAPI) (Listing, error) {
	var out []Resource
	var in eks.ListClustersInput
	for page := 0; page < maxDiscoveryPages; page++ {
		resp, err := api.ListClusters(ctx, &in)
		if err != nil {
			return Listing{}, fmt.Errorf("ListClusters: %w", err)
		}
		for _, name := range resp.Clusters {
			out = append(out, Resource{
				ID:    name,
				Name:  name,
				Extra: map[string]string{"logGroup": "/aws/containerinsights/" + name + "/application"},
			})
		}
		if resp.NextToken == nil || *resp.NextToken == "" {
			return sorted(out, false), nil
		}
		in.NextToken = resp.NextToken
	}
	return sorted(out, true), nil
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
