package email

import (
	"context"
	"errors"
	"fmt"
	"net/mail"
	"strings"
	"time"

	"github.com/windcry1/ai-companion/internal/identity"
	"github.com/windcry1/ai-companion/internal/platform/id"
	"github.com/windcry1/ai-companion/internal/team"
)

var (
	ErrNotFound   = errors.New("email delivery not found")
	ErrValidation = errors.New("email delivery validation failed")
	ErrNoDelivery = errors.New("no email delivery available")
	ErrConflict   = errors.New("email delivery state conflict")
)

type Delivery struct {
	ID                string     `json:"id"`
	ActorID           string     `json:"actor_id,omitempty"`
	ResourceType      string     `json:"resource_type"`
	ResourceID        string     `json:"resource_id"`
	Template          string     `json:"template"`
	RecipientEmail    string     `json:"recipient_email"`
	Subject           string     `json:"subject"`
	BodyText          string     `json:"body_text,omitempty"`
	Status            string     `json:"status"`
	Provider          string     `json:"provider,omitempty"`
	ProviderMessageID string     `json:"provider_message_id,omitempty"`
	FailureCode       string     `json:"failure_code,omitempty"`
	Attempts          int        `json:"attempts"`
	CreatedAt         time.Time  `json:"created_at"`
	UpdatedAt         time.Time  `json:"updated_at"`
	SentAt            *time.Time `json:"sent_at,omitempty"`
	AvailableAt       time.Time  `json:"-"`
	WorkerID          string     `json:"-"`
	LeaseExpiresAt    *time.Time `json:"-"`
}

type Message struct {
	To       string
	Subject  string
	BodyText string
}

type Sender interface {
	Send(context.Context, Message) (string, error)
}

type Store interface {
	CreateDelivery(context.Context, Delivery) error
	ListDeliveries(context.Context, string, int) ([]Delivery, error)
	GetDelivery(context.Context, string) (Delivery, error)
	ListDeliveriesByResource(context.Context, string, string) ([]Delivery, error)
	ReplayDelivery(context.Context, string, time.Time) (Delivery, error)
	ClaimDelivery(context.Context, string, time.Time, time.Duration) (Delivery, error)
	ClaimDeliveryByID(context.Context, string, string, time.Time, time.Duration) (Delivery, error)
	CompleteDelivery(context.Context, Delivery, string, string, time.Time) error
	FailDelivery(context.Context, Delivery, string, string, time.Time) error
}

type Service struct {
	store  Store
	sender Sender
	now    func() time.Time
}

func NewService(store Store, sender Sender) *Service {
	if sender == nil {
		sender = NoopSender{}
	}
	return &Service{store: store, sender: sender, now: time.Now}
}

func (s *Service) QueueWorkspaceInvitation(ctx context.Context, inviter identity.User, invitation team.Invitation) (Delivery, error) {
	if _, err := mail.ParseAddress(invitation.Email); err != nil {
		return Delivery{}, fmt.Errorf("%w: invalid recipient email", ErrValidation)
	}
	deliveryID, err := id.New()
	if err != nil {
		return Delivery{}, err
	}
	now := s.now().UTC()
	body := fmt.Sprintf("你好，%s 邀请你加入伴AI工作区。\n\n邀请 ID：%s\n角色：%s\n请登录应用后使用该邀请完成加入。\n", inviter.DisplayName, invitation.ID, invitation.Role)
	item := Delivery{
		ID: deliveryID, ActorID: inviter.ID, ResourceType: "workspace_invitation", ResourceID: invitation.ID,
		Template: "workspace.invitation.v1", RecipientEmail: strings.ToLower(strings.TrimSpace(invitation.Email)),
		Subject: "你收到了一封伴AI工作区邀请", BodyText: body, Status: "queued",
		CreatedAt: now, UpdatedAt: now, AvailableAt: now,
	}
	if err = s.store.CreateDelivery(ctx, item); err != nil {
		return Delivery{}, err
	}
	return item, nil
}

func (s *Service) ListResourceDeliveries(ctx context.Context, resourceType, resourceID string) ([]Delivery, error) {
	return s.store.ListDeliveriesByResource(ctx, resourceType, resourceID)
}

func (s *Service) ListDeliveries(ctx context.Context, status string, limit int) ([]Delivery, error) {
	status = strings.TrimSpace(status)
	if status != "" && status != "queued" && status != "processing" && status != "sent" && status != "failed" {
		return nil, ErrValidation
	}
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	return s.store.ListDeliveries(ctx, status, limit)
}

func (s *Service) GetDelivery(ctx context.Context, deliveryID string) (Delivery, error) {
	return s.store.GetDelivery(ctx, strings.TrimSpace(deliveryID))
}

func (s *Service) ReplayDelivery(ctx context.Context, deliveryID string) (Delivery, error) {
	deliveryID = strings.TrimSpace(deliveryID)
	if deliveryID == "" {
		return Delivery{}, ErrValidation
	}
	return s.store.ReplayDelivery(ctx, deliveryID, s.now().UTC())
}

func (s *Service) RunNext(ctx context.Context, workerID string, lease time.Duration) (bool, error) {
	item, err := s.store.ClaimDelivery(ctx, workerID, s.now().UTC(), lease)
	if errors.Is(err, ErrNoDelivery) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return s.send(ctx, item, workerID)
}

func (s *Service) RunDelivery(ctx context.Context, deliveryID, workerID string, lease time.Duration) (bool, error) {
	item, err := s.store.ClaimDeliveryByID(ctx, deliveryID, workerID, s.now().UTC(), lease)
	if errors.Is(err, ErrNoDelivery) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return s.send(ctx, item, workerID)
}

func (s *Service) RunReconciler(ctx context.Context, workerID string, lease, interval time.Duration) error {
	if interval <= 0 {
		interval = time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		processed, err := s.RunNext(ctx, workerID, lease)
		if err != nil {
			return err
		}
		if processed {
			continue
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

func (s *Service) send(ctx context.Context, item Delivery, workerID string) (bool, error) {
	providerID, err := s.sender.Send(ctx, Message{To: item.RecipientEmail, Subject: item.Subject, BodyText: item.BodyText})
	if err != nil {
		_ = s.store.FailDelivery(ctx, item, workerID, failureCode(err), s.now().UTC())
		return true, nil
	}
	return true, s.store.CompleteDelivery(ctx, item, workerID, providerID, s.now().UTC())
}

func failureCode(err error) string {
	message := strings.ToLower(err.Error())
	switch {
	case strings.Contains(message, "timeout"), strings.Contains(message, "deadline"):
		return "send_timeout"
	case strings.Contains(message, "auth"):
		return "smtp_auth_failed"
	default:
		return "send_failed"
	}
}
