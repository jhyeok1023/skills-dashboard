package domain

import "testing"

// The CloudWatch dimension is not the ARN. Getting this wrong yields a SEARCH
// that matches nothing, which looks exactly like a target group with no traffic.
func TestTargetGroupDimension(t *testing.T) {
	tests := []struct{ in, want string }{
		{
			"arn:aws:elasticloadbalancing:ap-northeast-2:123456789012:targetgroup/k8s-default-product-d6d507c878/73e2d6bc24d8a067",
			"targetgroup/k8s-default-product-d6d507c878/73e2d6bc24d8a067",
		},
		{"targetgroup/already-a-dimension/abc", "targetgroup/already-a-dimension/abc"},
		{"", ""},
	}
	for _, tc := range tests {
		if got := TargetGroupDimension(tc.in); got != tc.want {
			t.Errorf("TargetGroupDimension(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestLoadBalancerDimension(t *testing.T) {
	tests := []struct{ in, want string }{
		{
			"arn:aws:elasticloadbalancing:ap-northeast-2:123456789012:loadbalancer/app/my-alb/50dc6c495c0c9188",
			"app/my-alb/50dc6c495c0c9188",
		},
		{"app/my-alb/abc", "app/my-alb/abc"},
	}
	for _, tc := range tests {
		if got := LoadBalancerDimension(tc.in); got != tc.want {
			t.Errorf("LoadBalancerDimension(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestFriendlyTargetGroupName(t *testing.T) {
	tests := []struct{ in, want string }{
		{"k8s-default-product-d6d507c878", "product"},
		{"k8s-default-user-api-a1b2c3d4e5", "user-api"},
		{"my-hand-made-group", "my-hand-made-group"},
		{"k8s-short", "k8s-short"},
		{"", ""},
	}
	for _, tc := range tests {
		if got := FriendlyTargetGroupName(tc.in); got != tc.want {
			t.Errorf("FriendlyTargetGroupName(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
