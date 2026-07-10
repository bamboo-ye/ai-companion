package httpserver

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/windcry1/ai-companion/internal/platform/config"
)

func TestWorkspaceInviteAndAcceptFlow(t *testing.T) {
	server := New(config.Config{HTTPAddr: ":0", ServiceName: "test", Environment: "test", AuthTokenSecret: "team-secret-with-enough-entropy"}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ownerToken := registerTeamUser(t, server, "team-owner@example.com", "owner-device")
	inviteeToken := registerTeamUser(t, server, "team-invitee@example.com", "invitee-device")
	outsiderToken := registerTeamUser(t, server, "team-outsider@example.com", "outsider-device")

	created := performJSON(t, server, http.MethodPost, "/v1/workspaces", ownerToken, map[string]string{"name": "伴AI 工作室"})
	if created.Code != http.StatusCreated || !strings.Contains(created.Body.String(), "伴AI 工作室") {
		t.Fatalf("created workspace=%d %s", created.Code, created.Body.String())
	}
	var workspaceResponse struct {
		Workspace struct {
			ID string `json:"id"`
		} `json:"workspace"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &workspaceResponse); err != nil {
		t.Fatal(err)
	}
	workspaceID := workspaceResponse.Workspace.ID

	ownerList := performJSON(t, server, http.MethodGet, "/v1/workspaces", ownerToken, nil)
	if ownerList.Code != http.StatusOK || !strings.Contains(ownerList.Body.String(), workspaceID) {
		t.Fatalf("owner list=%d %s", ownerList.Code, ownerList.Body.String())
	}
	outsiderMembers := performJSON(t, server, http.MethodGet, "/v1/workspaces/"+workspaceID+"/members", outsiderToken, nil)
	if outsiderMembers.Code != http.StatusNotFound {
		t.Fatalf("outsider members=%d %s", outsiderMembers.Code, outsiderMembers.Body.String())
	}

	invite := performJSON(t, server, http.MethodPost, "/v1/workspaces/"+workspaceID+"/invitations", ownerToken, map[string]string{"email": "team-invitee@example.com", "role": "admin"})
	if invite.Code != http.StatusCreated || !strings.Contains(invite.Body.String(), `"role":"admin"`) {
		t.Fatalf("invite=%d %s", invite.Code, invite.Body.String())
	}
	var inviteResponse struct {
		Invitation struct {
			ID string `json:"id"`
		} `json:"invitation"`
		EmailDelivery struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"email_delivery"`
	}
	if err := json.Unmarshal(invite.Body.Bytes(), &inviteResponse); err != nil {
		t.Fatal(err)
	}
	if inviteResponse.EmailDelivery.ID == "" || inviteResponse.EmailDelivery.Status != "queued" {
		t.Fatalf("email delivery not queued: %s", invite.Body.String())
	}
	processed, err := server.emails.RunDelivery(t.Context(), inviteResponse.EmailDelivery.ID, "email-worker", time.Minute)
	if err != nil || !processed {
		t.Fatalf("email delivery processed=%v err=%v", processed, err)
	}
	deliveries, err := server.emails.ListResourceDeliveries(t.Context(), "workspace_invitation", inviteResponse.Invitation.ID)
	if err != nil || len(deliveries) != 1 || deliveries[0].Status != "sent" {
		t.Fatalf("email deliveries=%#v err=%v", deliveries, err)
	}

	wrongAccept := performJSON(t, server, http.MethodPost, "/v1/workspace-invitations/"+inviteResponse.Invitation.ID+"/accept", outsiderToken, nil)
	if wrongAccept.Code != http.StatusForbidden {
		t.Fatalf("wrong accept=%d %s", wrongAccept.Code, wrongAccept.Body.String())
	}
	accepted := performJSON(t, server, http.MethodPost, "/v1/workspace-invitations/"+inviteResponse.Invitation.ID+"/accept", inviteeToken, nil)
	if accepted.Code != http.StatusOK || !strings.Contains(accepted.Body.String(), `"role":"admin"`) || !strings.Contains(accepted.Body.String(), `"status":"accepted"`) {
		t.Fatalf("accepted=%d %s", accepted.Code, accepted.Body.String())
	}
	members := performJSON(t, server, http.MethodGet, "/v1/workspaces/"+workspaceID+"/members", inviteeToken, nil)
	if members.Code != http.StatusOK || !strings.Contains(members.Body.String(), "team-owner@example.com") || !strings.Contains(members.Body.String(), "team-invitee@example.com") {
		t.Fatalf("members=%d %s", members.Code, members.Body.String())
	}
}

func TestWorkspaceDocumentShareRequiresMembershipAndOwnership(t *testing.T) {
	server := New(config.Config{HTTPAddr: ":0", ServiceName: "test", Environment: "test", AuthTokenSecret: "team-secret-with-enough-entropy", DocumentMaxUploadBytes: 1024}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ownerToken := registerTeamUser(t, server, "share-owner@example.com", "share-owner-device")
	memberToken := registerTeamUser(t, server, "share-member@example.com", "share-member-device")
	outsiderToken := registerTeamUser(t, server, "share-outsider@example.com", "share-outsider-device")

	created := performJSON(t, server, http.MethodPost, "/v1/workspaces", ownerToken, map[string]string{"name": "共享资料库"})
	if created.Code != http.StatusCreated {
		t.Fatalf("create workspace=%d %s", created.Code, created.Body.String())
	}
	var workspaceResponse struct {
		Workspace struct {
			ID string `json:"id"`
		} `json:"workspace"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &workspaceResponse); err != nil {
		t.Fatal(err)
	}
	workspaceID := workspaceResponse.Workspace.ID

	invite := performJSON(t, server, http.MethodPost, "/v1/workspaces/"+workspaceID+"/invitations", ownerToken, map[string]string{"email": "share-member@example.com", "role": "member"})
	if invite.Code != http.StatusCreated {
		t.Fatalf("invite=%d %s", invite.Code, invite.Body.String())
	}
	var inviteResponse struct {
		Invitation struct {
			ID string `json:"id"`
		} `json:"invitation"`
	}
	if err := json.Unmarshal(invite.Body.Bytes(), &inviteResponse); err != nil {
		t.Fatal(err)
	}
	accepted := performJSON(t, server, http.MethodPost, "/v1/workspace-invitations/"+inviteResponse.Invitation.ID+"/accept", memberToken, nil)
	if accepted.Code != http.StatusOK {
		t.Fatalf("accept=%d %s", accepted.Code, accepted.Body.String())
	}

	upload := performUpload(t, server, ownerToken, "workspace-knowledge.txt", []byte("团队共享知识库：只有成员能看到。"))
	if upload.Code != http.StatusAccepted {
		t.Fatalf("upload=%d %s", upload.Code, upload.Body.String())
	}
	var uploaded struct {
		Document struct {
			ID string `json:"id"`
		} `json:"document"`
	}
	if err := json.Unmarshal(upload.Body.Bytes(), &uploaded); err != nil {
		t.Fatal(err)
	}

	memberShare := performJSON(t, server, http.MethodPost, "/v1/workspaces/"+workspaceID+"/documents", memberToken, map[string]string{"document_id": uploaded.Document.ID})
	if memberShare.Code != http.StatusNotFound {
		t.Fatalf("member sharing another user's document=%d %s", memberShare.Code, memberShare.Body.String())
	}
	shared := performJSON(t, server, http.MethodPost, "/v1/workspaces/"+workspaceID+"/documents", ownerToken, map[string]string{"document_id": uploaded.Document.ID})
	if shared.Code != http.StatusNoContent {
		t.Fatalf("share=%d %s", shared.Code, shared.Body.String())
	}
	reshared := performJSON(t, server, http.MethodPost, "/v1/workspaces/"+workspaceID+"/documents", ownerToken, map[string]string{"document_id": uploaded.Document.ID})
	if reshared.Code != http.StatusNoContent {
		t.Fatalf("reshare=%d %s", reshared.Code, reshared.Body.String())
	}

	memberList := performJSON(t, server, http.MethodGet, "/v1/workspaces/"+workspaceID+"/documents", memberToken, nil)
	if memberList.Code != http.StatusOK || !strings.Contains(memberList.Body.String(), "workspace-knowledge.txt") || !strings.Contains(memberList.Body.String(), uploaded.Document.ID) {
		t.Fatalf("member list=%d %s", memberList.Code, memberList.Body.String())
	}
	memberQuery := performJSON(t, server, http.MethodPost, "/v1/workspaces/"+workspaceID+"/documents/query", memberToken, map[string]any{"query": "共享知识库说了什么？"})
	if memberQuery.Code != http.StatusOK || !strings.Contains(memberQuery.Body.String(), `"sufficient":false`) {
		t.Fatalf("member query=%d %s", memberQuery.Code, memberQuery.Body.String())
	}
	outsiderList := performJSON(t, server, http.MethodGet, "/v1/workspaces/"+workspaceID+"/documents", outsiderToken, nil)
	if outsiderList.Code != http.StatusNotFound {
		t.Fatalf("outsider list=%d %s", outsiderList.Code, outsiderList.Body.String())
	}
	outsiderQuery := performJSON(t, server, http.MethodPost, "/v1/workspaces/"+workspaceID+"/documents/query", outsiderToken, map[string]any{"query": "共享知识库说了什么？"})
	if outsiderQuery.Code != http.StatusNotFound {
		t.Fatalf("outsider query=%d %s", outsiderQuery.Code, outsiderQuery.Body.String())
	}

	deleted := performJSON(t, server, http.MethodDelete, "/v1/documents/"+uploaded.Document.ID, ownerToken, nil)
	if deleted.Code != http.StatusNoContent {
		t.Fatalf("delete=%d %s", deleted.Code, deleted.Body.String())
	}
	afterDelete := performJSON(t, server, http.MethodGet, "/v1/workspaces/"+workspaceID+"/documents", memberToken, nil)
	if afterDelete.Code != http.StatusOK || strings.Contains(afterDelete.Body.String(), uploaded.Document.ID) {
		t.Fatalf("after delete=%d %s", afterDelete.Code, afterDelete.Body.String())
	}
}

func registerTeamUser(t *testing.T, server *Server, email, key string) string {
	t.Helper()
	response := performJSON(t, server, http.MethodPost, "/v1/auth/register", "", map[string]any{
		"email": email, "password": "correct-horse-battery", "display_name": email, "timezone": "Asia/Shanghai",
		"device": map[string]any{"device_key": key, "name": "Team Test", "platform": "web"},
	})
	if response.Code != http.StatusCreated {
		t.Fatalf("register=%d %s", response.Code, response.Body.String())
	}
	var tokens struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &tokens); err != nil {
		t.Fatal(err)
	}
	return tokens.AccessToken
}
