package store

import (
	"context"
	"errors"
	"fmt"
	"time"
	"xprem/internal/auditlog"
	"xprem/internal/database/postgres/pgdb"
	"xprem/internal/types"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// NewIosDeviceInvitation is an iPhone registration link to store; only the hash of its token is kept.
type NewIosDeviceInvitation struct {
	Id           string
	AppId        string
	TokenHash    string
	Challenge    string
	Label        string
	ExpiresAt    time.Time
	ActorType    auditlog.ActorType
	ActorId      string
	ActorDisplay string
}

// IosDeviceInvitation is an iPhone registration link as listed in the dashboard; DeviceName and
// DeviceProduct describe the iPhone a consumed link registered.
type IosDeviceInvitation struct {
	Id            string
	Label         string
	ExpiresAt     time.Time
	RevokedAt     *time.Time
	ConsumedAt    *time.Time
	CreatedAt     time.Time
	CreatedBy     string
	DeviceName    string
	DeviceProduct string
}

// ActiveIosDeviceInvitation is an unexpired, unrevoked registration link resolved from its token.
type ActiveIosDeviceInvitation struct {
	Id        string
	AppId     string
	AppName   string
	Label     string
	Challenge string
	ExpiresAt time.Time
	Consumed  bool
}

// RegisteredIosDevice is the latest successful registration of a UDID through the app's links.
type RegisteredIosDevice struct {
	UDID         string
	Product      string
	OSVersion    string
	InvitationId string
	Label        string
	RegisteredAt time.Time
}

// IosDeviceRegistration is one iPhone that went through a registration link.
type IosDeviceRegistration struct {
	InvitationId  string
	UDID          string
	DeviceName    string
	Product       string
	OSVersion     string
	Status        types.IosDeviceRegistrationStatus
	AppleDeviceId *string
	Error         *string
}

// InsertIosDeviceInvitation stores a hashed registration token and returns the link creation time.
func (s *PostgresIosCredentialsStore) InsertIosDeviceInvitation(ctx context.Context, invitation NewIosDeviceInvitation) (time.Time, error) {
	createdAt, err := s.engine.Queries.InsertIosDeviceInvitation(ctx, pgdb.InsertIosDeviceInvitationParams{
		ID:                    ToPgUUID(invitation.Id),
		AppID:                 ToPgUUID(invitation.AppId),
		TokenHash:             invitation.TokenHash,
		Challenge:             invitation.Challenge,
		Label:                 invitation.Label,
		ExpiresAt:             ToPgTimestamptz(&invitation.ExpiresAt),
		CreatedByActorType:    invitation.ActorType,
		CreatedByActorID:      invitation.ActorId,
		CreatedByActorDisplay: invitation.ActorDisplay,
	})
	if err != nil {
		return time.Time{}, fmt.Errorf("failed to save ios device invitation in database: %w", err)
	}
	return createdAt.Time, nil
}

// ListIosDeviceInvitations returns every link of the app, newest first.
func (s *PostgresIosCredentialsStore) ListIosDeviceInvitations(ctx context.Context, appId string) ([]IosDeviceInvitation, error) {
	rows, err := s.engine.Queries.ListIosDeviceInvitations(ctx, ToPgUUID(appId))
	if err != nil {
		return nil, fmt.Errorf("failed to retrieve ios device invitations from database: %w", err)
	}
	invitations := make([]IosDeviceInvitation, len(rows))
	for i, row := range rows {
		invitations[i] = IosDeviceInvitation{
			Id:            row.ID.String(),
			Label:         row.Label,
			ExpiresAt:     row.ExpiresAt.Time,
			CreatedAt:     row.CreatedAt.Time,
			CreatedBy:     row.CreatedByActorDisplay,
			DeviceName:    row.DeviceName,
			DeviceProduct: row.DeviceProduct,
		}
		if row.RevokedAt.Valid {
			invitations[i].RevokedAt = &row.RevokedAt.Time
		}
		if row.ConsumedAt.Valid {
			invitations[i].ConsumedAt = &row.ConsumedAt.Time
		}
	}
	return invitations, nil
}

// RevokeIosDeviceInvitation returns the label of the revoked link.
func (s *PostgresIosCredentialsStore) RevokeIosDeviceInvitation(ctx context.Context, appId string, invitationId string) (string, error) {
	label, err := s.engine.Queries.RevokeIosDeviceInvitation(ctx, pgdb.RevokeIosDeviceInvitationParams{
		AppID: ToPgUUID(appId),
		ID:    ToPgUUID(invitationId),
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", &ErrResourceNotFound{Resource: "ios device invitation", Identifier: invitationId}
		}
		return "", fmt.Errorf("failed to revoke ios device invitation in database: %w", err)
	}
	return label, nil
}

// ResolveIosDeviceInvitation returns (nil, nil) for an unknown, expired or revoked link; a consumed link is returned.
func (s *PostgresIosCredentialsStore) ResolveIosDeviceInvitation(ctx context.Context, tokenHash string) (*ActiveIosDeviceInvitation, error) {
	row, err := s.engine.Queries.ResolveIosDeviceInvitation(ctx, tokenHash)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to resolve ios device invitation from database: %w", err)
	}
	return &ActiveIosDeviceInvitation{
		Id:        row.ID.String(),
		AppId:     row.AppID.String(),
		AppName:   row.AppName,
		Label:     row.Label,
		Challenge: row.Challenge,
		ExpiresAt: row.ExpiresAt.Time,
		Consumed:  row.Consumed,
	}, nil
}

// ErrIosDeviceInvitationClaimLost means another enrollment owns or has consumed the link.
var ErrIosDeviceInvitationClaimLost = errors.New("ios device invitation claim lost")

// ClaimIosDeviceInvitation returns a unique claim token, or an empty string when the link is unavailable.
func (s *PostgresIosCredentialsStore) ClaimIosDeviceInvitation(ctx context.Context, invitationId string) (string, error) {
	claimToken := uuid.NewString()
	claimed, err := s.engine.Queries.ClaimIosDeviceInvitation(ctx, pgdb.ClaimIosDeviceInvitationParams{
		ID: ToPgUUID(invitationId), ClaimToken: ToPgUUID(claimToken),
	})
	if err != nil {
		return "", fmt.Errorf("failed to claim ios device invitation in database: %w", err)
	}
	if claimed == 0 {
		return "", nil
	}
	return claimToken, nil
}

// FinishIosDeviceRegistration records the outcome of a claimed link: a successful registration consumes
// the link, a failed one releases it. It returns the id of the registration.
func (s *PostgresIosCredentialsStore) FinishIosDeviceRegistration(ctx context.Context, registration IosDeviceRegistration, claimToken string) (string, error) {
	id := uuid.NewString()
	err := s.engine.WithTx(ctx, func(q *pgdb.Queries) error {
		err := q.InsertIosDeviceRegistration(ctx, pgdb.InsertIosDeviceRegistrationParams{
			ID:            ToPgUUID(id),
			InvitationID:  ToPgUUID(registration.InvitationId),
			Udid:          registration.UDID,
			DeviceName:    registration.DeviceName,
			Product:       registration.Product,
			OsVersion:     registration.OSVersion,
			Status:        registration.Status,
			AppleDeviceID: registration.AppleDeviceId,
			Error:         registration.Error,
		})
		if err != nil {
			return fmt.Errorf("failed to save ios device registration in database: %w", err)
		}
		var updated int64
		if registration.Status == types.IosDeviceRegistered {
			updated, err = q.ConsumeIosDeviceInvitation(ctx, pgdb.ConsumeIosDeviceInvitationParams{
				ID: ToPgUUID(registration.InvitationId), RegistrationID: ToPgUUID(id), ClaimToken: ToPgUUID(claimToken),
			})
		} else {
			updated, err = q.ReleaseIosDeviceInvitation(ctx, pgdb.ReleaseIosDeviceInvitationParams{
				ID: ToPgUUID(registration.InvitationId), ClaimToken: ToPgUUID(claimToken),
			})
		}
		if err != nil {
			return fmt.Errorf("failed to update ios device invitation in database: %w", err)
		}
		if updated == 0 {
			return ErrIosDeviceInvitationClaimLost
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	return id, nil
}

// ReleaseIosDeviceInvitation releases only this owner's claim; a stale token is a no-op.
func (s *PostgresIosCredentialsStore) ReleaseIosDeviceInvitation(ctx context.Context, invitationId string, claimToken string) error {
	if _, err := s.engine.Queries.ReleaseIosDeviceInvitation(ctx, pgdb.ReleaseIosDeviceInvitationParams{
		ID: ToPgUUID(invitationId), ClaimToken: ToPgUUID(claimToken),
	}); err != nil {
		return fmt.Errorf("failed to release ios device invitation in database: %w", err)
	}
	return nil
}

// GetIosDeviceRegistration returns (nil, nil) unless the registration belongs to the link, whatever the link's state.
func (s *PostgresIosCredentialsStore) GetIosDeviceRegistration(ctx context.Context, tokenHash string, registrationId string) (*IosDeviceRegistration, error) {
	row, err := s.engine.Queries.GetIosDeviceRegistration(ctx, pgdb.GetIosDeviceRegistrationParams{
		TokenHash: tokenHash,
		ID:        ToPgUUID(registrationId),
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to retrieve ios device registration from database: %w", err)
	}
	return &IosDeviceRegistration{
		DeviceName: row.DeviceName,
		Product:    row.Product,
		Status:     row.Status,
		Error:      row.Error,
	}, nil
}

// ListRegisteredIosDevices returns the latest successful registration of each UDID through the app's
// links, with the UDID in uppercase.
func (s *PostgresIosCredentialsStore) ListRegisteredIosDevices(ctx context.Context, appId string) ([]RegisteredIosDevice, error) {
	rows, err := s.engine.Queries.ListRegisteredIosDevices(ctx, ToPgUUID(appId))
	if err != nil {
		return nil, fmt.Errorf("failed to retrieve registered ios devices from database: %w", err)
	}
	devices := make([]RegisteredIosDevice, len(rows))
	for i, row := range rows {
		devices[i] = RegisteredIosDevice{
			UDID:         row.Udid,
			Product:      row.Product,
			OSVersion:    row.OsVersion,
			InvitationId: row.InvitationID.String(),
			Label:        row.Label,
			RegisteredAt: row.CreatedAt.Time,
		}
	}
	return devices, nil
}
