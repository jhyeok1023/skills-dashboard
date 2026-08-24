package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/jhyeok1023/skills-dashboard/internal/config"
)

// credentialsResponse is what the settings page shows and edits.
//
// The key comes back in full rather than masked. That is a deliberate trade and
// it has a cost: the secret crosses the wire on every visit to the settings
// page and sits in the browser's memory while it is open. What it buys is an
// editable form — a masked field cannot be corrected, only retyped, and a key
// retyped from memory is a key typed wrong. The dashboard is a local tool on
// one machine talking to itself, so the exposure it adds is the screen.
type credentialsResponse struct {
	config.Credentials
	// Source is which of the two the dashboard is running on, so the page can
	// say it. An operator editing the file that lost is the failure this
	// prevents.
	Source CredentialSource `json:"source"`
	// EnvFile is where the .env was found, empty when there was none.
	EnvFile string `json:"envFile,omitempty"`
	// Saved reports that there is a credentials.json to clear.
	Saved bool `json:"saved"`
}

func (s *Service) handleGetCredentials(w http.ResponseWriter, _ *http.Request) {
	saved, ok := s.credentials().Get()
	resp := credentialsResponse{Source: s.AWS().Source, EnvFile: s.EnvFile, Saved: ok}
	if ok {
		resp.Credentials = saved
		writeJSON(w, http.StatusOK, resp)
		return
	}
	// Nothing saved, so the form is seeded with whatever .env supplies. It is
	// the value in force, and showing it is what lets an operator save the key
	// they already have without going to find the file.
	if creds, err := config.LoadCredentials(s.EnvFile); err == nil {
		resp.Credentials = creds
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Service) handlePutCredentials(w http.ResponseWriter, r *http.Request) {
	var creds config.Credentials
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&creds); err != nil {
		badRequest(w, fmt.Errorf("자격증명을 읽을 수 없습니다: %w", err))
		return
	}
	if err := creds.Validate(); err != nil {
		badRequest(w, err)
		return
	}

	// Tried against AWS before it is written, never after. A file that exists
	// therefore always holds a key that authenticated at least once, and the
	// operator finds out about a typo while the form is still in front of them
	// rather than through an empty panel on another screen.
	ctx, cancel := context.WithTimeout(r.Context(), identityTimeout+5*time.Second)
	defer cancel()
	conn := s.connect(ctx, creds, SourceSaved)
	if conn.Err != nil {
		writeError(w, http.StatusBadGateway, conn.Err,
			"AWS 가 이 키를 받아들이지 않았습니다. 저장하지 않았습니다.")
		return
	}
	if err := s.credentials().Set(creds); err != nil {
		writeError(w, http.StatusInternalServerError, err, "자격증명 파일을 쓰지 못했습니다.")
		return
	}
	s.SetAWS(conn)
	s.log().Info("credentials saved from the settings page", "key", creds.Redacted())

	writeJSON(w, http.StatusOK, credentialsResponse{
		Credentials: creds, Source: conn.Source, EnvFile: s.EnvFile, Saved: true,
	})
}

// handleDeleteCredentials forgets the saved key and falls back to .env.
//
// Reconnecting rather than merely deleting is the point: without it the
// dashboard would keep running on the key that was just discarded until
// someone restarted it, which is the opposite of what the button says.
func (s *Service) handleDeleteCredentials(w http.ResponseWriter, r *http.Request) {
	if err := s.credentials().Clear(); err != nil {
		writeError(w, http.StatusInternalServerError, err, "자격증명 파일을 지우지 못했습니다.")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), identityTimeout+5*time.Second)
	defer cancel()
	conn := s.Resolve(ctx)
	s.SetAWS(conn)
	s.log().Info("saved credentials cleared", "source", conn.Source, "error", conn.Err)

	resp := credentialsResponse{Source: conn.Source, EnvFile: s.EnvFile}
	if creds, err := config.LoadCredentials(s.EnvFile); err == nil {
		resp.Credentials = creds
	}
	writeJSON(w, http.StatusOK, resp)
}
