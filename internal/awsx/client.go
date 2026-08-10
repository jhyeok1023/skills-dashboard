package awsx

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/retry"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	"github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
	"github.com/aws/aws-sdk-go-v2/service/rds"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/aws/aws-sdk-go-v2/service/wafv2"

	"github.com/jhyeok1023/skills-dashboard/internal/config"
)

// GlobalWAFRegion is where CLOUDFRONT-scoped WAF publishes its logs and
// metrics, no matter where the distribution serves traffic from.
const GlobalWAFRegion = "us-east-1"

// Clients holds one client per service, built once and reused for the life of
// the process.
//
// Building them once matters. The reference implementation constructed a fresh
// http.Transport for every Kubernetes call — several times per refresh, every
// ten seconds — and never closed any of them, so idle TLS connections
// accumulated until the process ran out of descriptors.
// The fields are the interfaces from iface.go rather than the concrete SDK
// clients, so a test can substitute a fake for any one service without needing
// AWS or a network.
type Clients struct {
	Region    string
	WAFRegion string

	CW   MetricsAPI
	Logs LogsClient
	STS  IdentityAPI
	ELB  LoadBalancerAPI
	RDS  ProxyAPI
	WAF  WAFAPI
	EKS  ClusterAPI

	// The CLOUDFRONT-scope equivalents, pinned to us-east-1.
	CWGlobal   MetricsAPI
	LogsGlobal LogsClient
	WAFGlobal  WAFAPI
}

// httpClient is shared by every service client. Its timeouts bound how long a
// single AWS call can hold a request open; the reference implementation left
// both the server and the AWS clients untimed, so a slow dependency simply
// accumulated stuck handlers.
func httpClient() *http.Client {
	return &http.Client{
		Timeout: 60 * time.Second,
		Transport: &http.Transport{
			Proxy: http.ProxyFromEnvironment,
			DialContext: (&net.Dialer{
				Timeout:   10 * time.Second,
				KeepAlive: 30 * time.Second,
			}).DialContext,
			MaxIdleConns:          64,
			MaxIdleConnsPerHost:   16,
			IdleConnTimeout:       90 * time.Second,
			TLSHandshakeTimeout:   10 * time.Second,
			ExpectContinueTimeout: 1 * time.Second,
		},
	}
}

// retryer backs off exponentially with jitter instead of re-issuing the same
// request at a fixed interval. A throttled account that is retried at full rate
// stays throttled.
func retryer() aws.Retryer {
	return retry.NewStandard(func(o *retry.StandardOptions) {
		o.MaxAttempts = 5
		o.MaxBackoff = 15 * time.Second
	})
}

// New builds the client set for the given credentials.
func New(ctx context.Context, creds config.Credentials, wafRegion string) (*Clients, error) {
	if err := creds.Validate(); err != nil {
		return nil, err
	}
	if wafRegion == "" {
		wafRegion = GlobalWAFRegion
	}

	shared := httpClient()
	provider := credentials.NewStaticCredentialsProvider(
		creds.AccessKeyID, creds.SecretAccessKey, creds.SessionToken,
	)

	load := func(region string) (aws.Config, error) {
		return awsconfig.LoadDefaultConfig(ctx,
			awsconfig.WithRegion(region),
			awsconfig.WithCredentialsProvider(provider),
			awsconfig.WithHTTPClient(shared),
			awsconfig.WithRetryer(retryer),
		)
	}

	primary, err := load(creds.Region)
	if err != nil {
		return nil, fmt.Errorf("configure AWS clients for %s: %w", creds.Region, err)
	}

	c := &Clients{
		Region:    creds.Region,
		WAFRegion: wafRegion,
		CW:        cloudwatch.NewFromConfig(primary),
		Logs:      cloudwatchlogs.NewFromConfig(primary),
		STS:       sts.NewFromConfig(primary),
		ELB:       elasticloadbalancingv2.NewFromConfig(primary),
		RDS:       rds.NewFromConfig(primary),
		WAF:       wafv2.NewFromConfig(primary),
		EKS:       eks.NewFromConfig(primary),
	}

	if wafRegion == creds.Region {
		c.CWGlobal, c.LogsGlobal, c.WAFGlobal = c.CW, c.Logs, c.WAF
		return c, nil
	}

	global, err := load(wafRegion)
	if err != nil {
		return nil, fmt.Errorf("configure AWS clients for %s: %w", wafRegion, err)
	}
	c.CWGlobal = cloudwatch.NewFromConfig(global)
	c.LogsGlobal = cloudwatchlogs.NewFromConfig(global)
	c.WAFGlobal = wafv2.NewFromConfig(global)
	return c, nil
}

// Identity is who the configured access key belongs to, and where it is
// pointed. The two regions are reported because they are set outside the UI —
// the working region by the .env file, the WAF one by the config file — and an
// operator looking at an empty WAF panel needs to see which region the
// dashboard actually queried.
type Identity struct {
	Account   string `json:"account"`
	ARN       string `json:"arn"`
	UserID    string `json:"userId"`
	Region    string `json:"region"`
	WAFRegion string `json:"wafRegion,omitempty"`
}

// WhoAmI validates the credentials against AWS.
func WhoAmI(ctx context.Context, api IdentityAPI, region string) (Identity, error) {
	out, err := api.GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
	if err != nil {
		return Identity{}, fmt.Errorf("GetCallerIdentity: %w", err)
	}
	return Identity{
		Account: aws.ToString(out.Account),
		ARN:     aws.ToString(out.Arn),
		UserID:  aws.ToString(out.UserId),
		Region:  region,
	}, nil
}
