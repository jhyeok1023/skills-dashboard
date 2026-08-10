package awsx

import (
	"context"
	"sync"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	logtypes "github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs/types"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	ekstypes "github.com/aws/aws-sdk-go-v2/service/eks/types"
	"github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
	elbtypes "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2/types"
)

// The ARN-to-dimension conversions these discoveries rely on are tested in
// internal/domain/dimensions_test.go, where they live.

// fakeELB is shared by both of TargetGroups' walks, which now run at the same
// time, so its counters are guarded.
type fakeELB struct {
	tgPages []*elasticloadbalancingv2.DescribeTargetGroupsOutput
	lbPages []*elasticloadbalancingv2.DescribeLoadBalancersOutput

	// arrived, when set, is closed once both walks have entered. A serial
	// implementation never closes it and the test dies on the go test timeout;
	// a concurrent one passes without a sleep anywhere.
	arrived chan struct{}
	tgOnce  sync.Once
	lbOnce  sync.Once

	mu      sync.Mutex
	entered int
	tgCalls int
	lbCalls int
}

// enter records that one of the two walks has started, and blocks until the
// other has too. Only the first page of each walk is counted.
func (f *fakeELB) enter(once *sync.Once) {
	if f.arrived == nil {
		return
	}
	once.Do(func() {
		f.mu.Lock()
		f.entered++
		both := f.entered == 2
		f.mu.Unlock()
		if both {
			close(f.arrived)
		}
	})
	<-f.arrived
}

func (f *fakeELB) DescribeTargetGroups(_ context.Context, _ *elasticloadbalancingv2.DescribeTargetGroupsInput, _ ...func(*elasticloadbalancingv2.Options)) (*elasticloadbalancingv2.DescribeTargetGroupsOutput, error) {
	f.enter(&f.tgOnce)
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.tgCalls < len(f.tgPages) {
		out := f.tgPages[f.tgCalls]
		f.tgCalls++
		return out, nil
	}
	f.tgCalls++
	return &elasticloadbalancingv2.DescribeTargetGroupsOutput{}, nil
}

func (f *fakeELB) DescribeLoadBalancers(_ context.Context, _ *elasticloadbalancingv2.DescribeLoadBalancersInput, _ ...func(*elasticloadbalancingv2.Options)) (*elasticloadbalancingv2.DescribeLoadBalancersOutput, error) {
	f.enter(&f.lbOnce)
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.lbCalls < len(f.lbPages) {
		out := f.lbPages[f.lbCalls]
		f.lbCalls++
		return out, nil
	}
	f.lbCalls++
	return &elasticloadbalancingv2.DescribeLoadBalancersOutput{}, nil
}

func TestTargetGroupsAnnotatesWithTheLoadBalancer(t *testing.T) {
	lbArn := "arn:aws:elasticloadbalancing:ap-northeast-2:1:loadbalancer/app/my-alb/abc"
	tgArn := "arn:aws:elasticloadbalancing:ap-northeast-2:1:targetgroup/k8s-default-product-d6d507c878/def"

	f := &fakeELB{
		lbPages: []*elasticloadbalancingv2.DescribeLoadBalancersOutput{{
			LoadBalancers: []elbtypes.LoadBalancer{{
				LoadBalancerArn: aws.String(lbArn), LoadBalancerName: aws.String("my-alb"),
			}},
		}},
		tgPages: []*elasticloadbalancingv2.DescribeTargetGroupsOutput{{
			TargetGroups: []elbtypes.TargetGroup{{
				TargetGroupArn:   aws.String(tgArn),
				TargetGroupName:  aws.String("k8s-default-product-d6d507c878"),
				LoadBalancerArns: []string{lbArn},
			}},
		}},
	}

	got, err := TargetGroups(context.Background(), f)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Resources) != 1 {
		t.Fatalf("got %d target groups", len(got.Resources))
	}
	if got.Truncated {
		t.Error("a single-page walk reported itself truncated")
	}
	r := got.Resources[0]
	if r.ID != "targetgroup/k8s-default-product-d6d507c878/def" {
		t.Errorf("ID = %q, want the CloudWatch dimension", r.ID)
	}
	if r.Extra["loadBalancer"] != "app/my-alb/abc" {
		t.Errorf("loadBalancer = %q, want the dimension form", r.Extra["loadBalancer"])
	}
	if r.Extra["loadBalancerName"] != "my-alb" {
		t.Errorf("loadBalancerName = %q, want my-alb", r.Extra["loadBalancerName"])
	}
	if r.Extra["friendlyName"] != "product" {
		t.Errorf("friendlyName = %q, want product", r.Extra["friendlyName"])
	}
	if r.ARN != tgArn {
		t.Errorf("ARN was not preserved for copying: %q", r.ARN)
	}
}

// The load balancer walk and the target group walk are independent, and with
// one target group per application the list is long enough that running them in
// sequence is felt on the settings page. Both fakes block until the other has
// been entered, so a serial implementation cannot finish this test.
func TestTargetGroupsWalksBothListsAtOnce(t *testing.T) {
	f := &fakeELB{arrived: make(chan struct{})}
	if _, err := TargetGroups(context.Background(), f); err != nil {
		t.Fatal(err)
	}
}

// A capped list that does not say it was capped offers the operator a short
// list with no reason to doubt it.
func TestTargetGroupsReportsATruncatedWalk(t *testing.T) {
	endless := &elasticloadbalancingv2.DescribeTargetGroupsOutput{
		TargetGroups: []elbtypes.TargetGroup{{
			TargetGroupArn:  aws.String("arn:aws:elasticloadbalancing:ap-northeast-2:1:targetgroup/k8s-default-a-1/x"),
			TargetGroupName: aws.String("k8s-default-a-1"),
		}},
		NextMarker: aws.String("more"),
	}
	pages := make([]*elasticloadbalancingv2.DescribeTargetGroupsOutput, maxDiscoveryPages+5)
	for i := range pages {
		pages[i] = endless
	}

	f := &fakeELB{tgPages: pages}
	got, err := TargetGroups(context.Background(), f)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Truncated {
		t.Error("the walk hit the page cap without saying so")
	}
	if len(got.Resources) == 0 {
		t.Error("the capped walk returned nothing at all")
	}
	if f.tgCalls > maxDiscoveryPages {
		t.Errorf("walked %d pages, above the %d cap", f.tgCalls, maxDiscoveryPages)
	}
}

// The settings page groups target groups by the load balancer each one is
// attached to, which only works if the annotation survives more than one ALB.
func TestTargetGroupsSpanSeveralLoadBalancers(t *testing.T) {
	const (
		albA = "arn:aws:elasticloadbalancing:ap-northeast-2:1:loadbalancer/app/public-alb/aaa"
		albB = "arn:aws:elasticloadbalancing:ap-northeast-2:1:loadbalancer/app/internal-alb/bbb"
	)
	tg := func(name, lb string) elbtypes.TargetGroup {
		return elbtypes.TargetGroup{
			TargetGroupArn:   aws.String("arn:aws:elasticloadbalancing:ap-northeast-2:1:targetgroup/" + name + "/z"),
			TargetGroupName:  aws.String(name),
			LoadBalancerArns: []string{lb},
		}
	}

	f := &fakeELB{
		lbPages: []*elasticloadbalancingv2.DescribeLoadBalancersOutput{{
			LoadBalancers: []elbtypes.LoadBalancer{
				{LoadBalancerArn: aws.String(albA), LoadBalancerName: aws.String("public-alb")},
				{LoadBalancerArn: aws.String(albB), LoadBalancerName: aws.String("internal-alb")},
			},
		}},
		tgPages: []*elasticloadbalancingv2.DescribeTargetGroupsOutput{{
			TargetGroups: []elbtypes.TargetGroup{
				tg("k8s-default-checkout-1111111111", albA),
				tg("k8s-default-search-2222222222", albB),
			},
		}},
	}

	got, err := TargetGroups(context.Background(), f)
	if err != nil {
		t.Fatal(err)
	}
	byApp := map[string]Resource{}
	for _, r := range got.Resources {
		byApp[r.Extra["friendlyName"]] = r
	}
	if byApp["checkout"].Extra["loadBalancerName"] != "public-alb" {
		t.Errorf("checkout is filed under %q", byApp["checkout"].Extra["loadBalancerName"])
	}
	if byApp["search"].Extra["loadBalancerName"] != "internal-alb" {
		t.Errorf("search is filed under %q", byApp["search"].Extra["loadBalancerName"])
	}
}

type endlessLogGroups struct{ calls int }

func (e *endlessLogGroups) DescribeLogGroups(_ context.Context, _ *cloudwatchlogs.DescribeLogGroupsInput, _ ...func(*cloudwatchlogs.Options)) (*cloudwatchlogs.DescribeLogGroupsOutput, error) {
	e.calls++
	return &cloudwatchlogs.DescribeLogGroupsOutput{
		LogGroups: []logtypes.LogGroup{{LogGroupName: aws.String("/aws/x")}},
		NextToken: aws.String("more"),
	}, nil
}

// Discovery runs behind a settings page. A bounded, possibly incomplete list
// beats an unbounded walk that holds the request open.
func TestLogGroupsStopsWalkingRunawayPagination(t *testing.T) {
	e := &endlessLogGroups{}
	got, err := LogGroups(context.Background(), e, "/aws/containerinsights/")
	if err != nil {
		t.Fatal(err)
	}
	if e.calls > maxDiscoveryPages {
		t.Errorf("walked %d pages, above the %d cap", e.calls, maxDiscoveryPages)
	}
	if len(got.Resources) == 0 {
		t.Error("the capped walk returned nothing at all")
	}
	if !got.Truncated {
		t.Error("the walk hit the page cap without saying so")
	}
}

type fakeEKS struct {
	clusters   []string
	nodegroups []string
	scaling    map[string]ekstypes.NodegroupScalingConfig
}

func (f *fakeEKS) ListClusters(_ context.Context, _ *eks.ListClustersInput, _ ...func(*eks.Options)) (*eks.ListClustersOutput, error) {
	return &eks.ListClustersOutput{Clusters: f.clusters}, nil
}

func (f *fakeEKS) ListNodegroups(_ context.Context, _ *eks.ListNodegroupsInput, _ ...func(*eks.Options)) (*eks.ListNodegroupsOutput, error) {
	return &eks.ListNodegroupsOutput{Nodegroups: f.nodegroups}, nil
}

func (f *fakeEKS) DescribeNodegroup(_ context.Context, in *eks.DescribeNodegroupInput, _ ...func(*eks.Options)) (*eks.DescribeNodegroupOutput, error) {
	sc := f.scaling[aws.ToString(in.NodegroupName)]
	return &eks.DescribeNodegroupOutput{Nodegroup: &ekstypes.Nodegroup{ScalingConfig: &sc}}, nil
}

func TestClustersSuggestTheirLogGroup(t *testing.T) {
	got, err := Clusters(context.Background(), &fakeEKS{clusters: []string{"prod", "staging"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Resources) != 2 {
		t.Fatalf("got %d clusters", len(got.Resources))
	}
	if got.Resources[0].Name != "prod" {
		t.Errorf("clusters are not sorted: %v", got.Resources)
	}
	if want := "/aws/containerinsights/prod/application"; got.Resources[0].Extra["logGroup"] != want {
		t.Errorf("logGroup = %q, want %q", got.Resources[0].Extra["logGroup"], want)
	}
}

// The minimum and maximum node counts exist only in the node group scaling
// configuration; CloudWatch publishes the current count and nothing else.
func TestClusterNodeScalingSumsEveryNodeGroup(t *testing.T) {
	f := &fakeEKS{
		nodegroups: []string{"general", "spot"},
		scaling: map[string]ekstypes.NodegroupScalingConfig{
			"general": {MinSize: aws.Int32(2), MaxSize: aws.Int32(6), DesiredSize: aws.Int32(3)},
			"spot":    {MinSize: aws.Int32(0), MaxSize: aws.Int32(10), DesiredSize: aws.Int32(1)},
		},
	}
	got, err := ClusterNodeScaling(context.Background(), f, "prod")
	if err != nil {
		t.Fatal(err)
	}
	if got.Min != 2 || got.Max != 16 || got.Desired != 4 {
		t.Errorf("scaling = %+v, want min 2 max 16 desired 4", got)
	}
	if len(got.Groups) != 2 || got.Groups[0] != "general" {
		t.Errorf("groups = %v", got.Groups)
	}
}

func TestClusterNodeScalingRequiresACluster(t *testing.T) {
	if _, err := ClusterNodeScaling(context.Background(), &fakeEKS{}, ""); err == nil {
		t.Error("accepted an empty cluster name")
	}
}
