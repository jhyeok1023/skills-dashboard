package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jhyeok1023/skills-dashboard/internal/awsx"
	"github.com/jhyeok1023/skills-dashboard/internal/config"
)

// credentialFixture wires a Service with a credentials file of its own and a
// connector that answers without an AWS account. accepts decides which keys
// work, which is what lets the save path be tested for the case that matters:
// a key AWS turns down.
func credentialFixture(t *testing.T, envFile string, accepts map[string]bool) (*Service, http.Handler, string) {
	t.Helper()
	dir := t.TempDir()
	credPath := filepath.Join(dir, "credentials.json")

	svc, _ := newTestService(t)
	svc.EnvFile = envFile
	svc.Credentials = config.LoadCredentialStore(credPath)
	svc.Connector = func(_ context.Context, creds config.Credentials, source CredentialSource) *AWSConn {
		if !accepts[creds.AccessKeyID] {
			return &AWSConn{Source: source, Err: fmt.Errorf("InvalidClientTokenId: %s", creds.AccessKeyID)}
		}
		return &AWSConn{
			Source:   source,
			Clients:  &awsx.Clients{Region: creds.Region, WAFRegion: creds.Region},
			Insights: &awsx.InsightsRunner{},
			Metrics:  &awsx.MetricFetcher{},
			Identity: awsx.Identity{Account: "1234", Region: creds.Region},
		}
	}
	return svc, svc.Handler(), credPath
}

// writeEnv drops a .env with one key in it and returns its path.
func writeEnv(t *testing.T, id string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), ".env")
	body := fmt.Sprintf("AWS_ACCESS_KEY_ID=%s\nAWS_SECRET_ACCESS_KEY=secret\nAWS_REGION=ap-northeast-2\n", id)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func send(t *testing.T, h http.Handler, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(method, path, strings.NewReader(body)))
	return rec
}

func readCredentials(t *testing.T, rec *httptest.ResponseRecorder) credentialsResponse {
	t.Helper()
	var got credentialsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
	return got
}

// With nothing saved, the form is seeded from .env — the key that is actually
// in force. Handing back an empty form would invite an operator to retype a key
// the dashboard already had.
func TestCredentialsSeedFromEnvWhenNothingIsSaved(t *testing.T) {
	_, h, credPath := credentialFixture(t, writeEnv(t, "AKIAENV"), map[string]bool{"AKIAENV": true})

	got := readCredentials(t, get(t, h, "/api/credentials"))
	if got.AccessKeyID != "AKIAENV" {
		t.Errorf("accessKeyId = %q, want the one from .env", got.AccessKeyID)
	}
	if got.Saved {
		t.Error("reports a saved key when nothing was saved")
	}
	if _, err := os.Stat(credPath); !os.IsNotExist(err) {
		t.Error("reading the credentials wrote a file")
	}
}

// The saved key wins, and the response says so. A page that showed the .env key
// while the dashboard ran on another one would send its operator to edit the
// wrong file.
func TestSavedCredentialsWinOverEnvAndSayThatTheyDid(t *testing.T) {
	svc, h, credPath := credentialFixture(t, writeEnv(t, "AKIAENV"),
		map[string]bool{"AKIAENV": true, "AKIASAVED": true})

	body := `{"accessKeyId":"AKIASAVED","secretAccessKey":"s3cret","region":"us-east-1"}`
	rec := send(t, h, http.MethodPut, "/api/credentials", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("save: status %d, body %s", rec.Code, rec.Body)
	}
	if got := readCredentials(t, rec); got.Source != SourceSaved || !got.Saved {
		t.Errorf("save reported source %q saved %v", got.Source, got.Saved)
	}

	// Applied without a restart: the connection in force is the new one.
	if got := svc.AWS().Clients.Region; got != "us-east-1" {
		t.Errorf("clients still point at %q; the save did not take", got)
	}
	if got := readCredentials(t, get(t, h, "/api/credentials")); got.AccessKeyID != "AKIASAVED" {
		t.Errorf("reading back gives %q, want the saved key", got.AccessKeyID)
	}

	// And it survives the process. A fresh store over the same file is what the
	// next start does.
	saved, ok := config.LoadCredentialStore(credPath).Get()
	if !ok || saved.AccessKeyID != "AKIASAVED" {
		t.Errorf("the file does not hold the saved key: %+v (ok=%v)", saved, ok)
	}
}

// A key AWS turns down is not written. The file existing at all is what the
// next start trusts over .env, so writing an unverified key would hand the
// dashboard a rejection that outlives the form it was typed into.
func TestRejectedCredentialsAreNotSaved(t *testing.T) {
	svc, h, credPath := credentialFixture(t, writeEnv(t, "AKIAENV"), map[string]bool{"AKIAENV": true})
	before := svc.AWS()

	body := `{"accessKeyId":"AKIABAD","secretAccessKey":"nope","region":"ap-northeast-2"}`
	rec := send(t, h, http.MethodPut, "/api/credentials", body)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status %d, want 502; body %s", rec.Code, rec.Body)
	}
	var resp errorResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(resp.Detail, "InvalidClientTokenId") {
		t.Errorf("the AWS message did not reach the operator: %q", resp.Detail)
	}
	if _, err := os.Stat(credPath); !os.IsNotExist(err) {
		t.Error("a key AWS rejected was written to disk")
	}
	if svc.AWS() != before {
		t.Error("a rejected key replaced the working connection")
	}
}

// An incomplete key is refused before AWS is called at all.
func TestIncompleteCredentialsAreRefused(t *testing.T) {
	_, h, _ := credentialFixture(t, "", map[string]bool{})

	rec := send(t, h, http.MethodPut, "/api/credentials", `{"accessKeyId":"AKIA","region":"ap-northeast-2"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400; body %s", rec.Code, rec.Body)
	}
}

// Clearing removes the file and reconnects on .env, rather than leaving the
// dashboard running on the key it was just told to forget.
func TestClearingCredentialsFallsBackToEnv(t *testing.T) {
	svc, h, credPath := credentialFixture(t, writeEnv(t, "AKIAENV"),
		map[string]bool{"AKIAENV": true, "AKIASAVED": true})

	body := `{"accessKeyId":"AKIASAVED","secretAccessKey":"s3cret","region":"us-east-1"}`
	if rec := send(t, h, http.MethodPut, "/api/credentials", body); rec.Code != http.StatusOK {
		t.Fatalf("save: status %d, body %s", rec.Code, rec.Body)
	}

	rec := send(t, h, http.MethodDelete, "/api/credentials", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("clear: status %d, body %s", rec.Code, rec.Body)
	}
	if got := readCredentials(t, rec); got.Source != SourceEnv || got.Saved {
		t.Errorf("clear reported source %q saved %v", got.Source, got.Saved)
	}
	if _, err := os.Stat(credPath); !os.IsNotExist(err) {
		t.Error("the credentials file is still there after clearing")
	}
	if got := svc.AWS().Clients.Region; got != "ap-northeast-2" {
		t.Errorf("still connected as %q; the clear did not reconnect", got)
	}
}

// A key saved on the settings page reaches the panels, and the answers the old
// key produced do not survive the swap.
func TestSavingCredentialsEmptiesTheCache(t *testing.T) {
	svc, h, _ := credentialFixture(t, "", map[string]bool{"AKIASAVED": true})

	// Fill the cache through a panel the stub service can answer.
	if rec := get(t, h, "/api/panel/pod-cpu?range=1h&period=5m"); rec.Code != http.StatusOK {
		t.Fatalf("panel: status %d, body %s", rec.Code, rec.Body)
	}
	if svc.Cache.Len() == 0 {
		t.Fatal("the panel cached nothing, so this test would prove nothing")
	}

	body := `{"accessKeyId":"AKIASAVED","secretAccessKey":"s3cret","region":"us-east-1"}`
	if rec := send(t, h, http.MethodPut, "/api/credentials", body); rec.Code != http.StatusOK {
		t.Fatalf("save: status %d, body %s", rec.Code, rec.Body)
	}
	if n := svc.Cache.Len(); n != 0 {
		t.Errorf("%d entries survived the key change; they answer for the old account", n)
	}
}

// A file that cannot be understood is ignored rather than fatal, and it does
// not take .env down with it — the settings page is where such a thing gets
// fixed, and a process that exits serves no settings page.
func TestUnreadableSavedCredentialsFallBackToEnv(t *testing.T) {
	path := filepath.Join(t.TempDir(), "credentials.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	store := config.LoadCredentialStore(path)
	if _, ok := store.Get(); ok {
		t.Error("a corrupt file was accepted as a key")
	}
	if store.Notice() == "" {
		t.Error("a corrupt file was ignored in silence")
	}
}
