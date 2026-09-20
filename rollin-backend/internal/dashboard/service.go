// Package dashboard owns the §5.1 statistics aggregation (04-api-contract.md, 需求 70).
// D6: every count is computed live at query time — no snapshot table. The Offer-caliber
// occupancy is NOT recomputed here: it delegates to offer.Service.Occupied (PENDING +
// ACCEPTED offers, INV-1), the exact function the admission engine, quota checks and the
// candidate list share, so dashboard, list and export can never drift (P6-2/P6-6).
// SPECIAL re-issues keep the old terminal offer, so they add to offersTotal but never to
// the occupancy — the unique key uk_application_active_offer admits at most one
// PENDING/ACCEPTED offer per application in the first place.
package dashboard

import (
	"context"

	"gorm.io/gorm"
	"rollin-backend/internal/application"
	"rollin-backend/internal/model"
	"rollin-backend/internal/offer"
)

// Stats is the §5.1 stats block. quota is NOT part of this struct: the handler renders
// it from the live activity row the session middleware already re-read. Application-status
// counters (accepted…ineligible) use the same enum the candidate list filters on;
// occupied is the Offer caliber.
type Stats struct {
	Accepted            int64
	Pending             int64
	Declined            int64
	Expired             int64
	Waiting             int64
	Ineligible          int64
	Occupied            int64
	OffersTotal         int64 // historical issues, including SPECIAL re-issues
	CandidatesWithOffer int64 // distinct applications that ever received an offer
	MailFailed          int64
	MailPending         int64
}

// Deps wires the shared caliber functions. Offers provides Occupied (the INV-1 entry
// point); Applications provides the per-status distribution the candidate list uses.
type Deps struct {
	Offers       offer.Service
	Applications application.Repository
}

// Service is the dashboard domain API.
type Service interface {
	// Stats aggregates one activity's live counts in a handful of queries (D6:
	// 单活动 ≤ 5000 候选人，查询时聚合无性能风险).
	Stats(ctx context.Context, activityID uint64) (Stats, error)
}

type service struct {
	db   *gorm.DB
	deps Deps
}

// New wires the dashboard service. Nil collaborators fall back to their default GORM
// implementations over db so tests can construct it minimally.
func New(db *gorm.DB, deps Deps) Service {
	if deps.Offers == nil {
		deps.Offers = offer.New(db, nil)
	}
	if deps.Applications == nil {
		deps.Applications = application.NewGormRepository(db)
	}
	return &service{db: db, deps: deps}
}

// Stats implements the §5.1 aggregation.
func (s *service) Stats(ctx context.Context, activityID uint64) (Stats, error) {
	// 1. Application status distribution — the candidate-list caliber.
	byStatus, err := s.deps.Applications.CountByStatus(ctx, activityID)
	if err != nil {
		return Stats{}, err
	}
	stats := Stats{
		Accepted:   byStatus[model.ApplicationAccepted],
		Pending:    byStatus[model.ApplicationOffered],
		Declined:   byStatus[model.ApplicationDeclined],
		Expired:    byStatus[model.ApplicationExpired],
		Waiting:    byStatus[model.ApplicationWaiting],
		Ineligible: byStatus[model.ApplicationIneligible],
	}
	// 2. Occupancy — the shared Offer caliber (PENDING+ACCEPTED), never a local copy.
	// The base handle (no tx) is the documented read path.
	occupied, err := s.deps.Offers.Occupied(ctx, s.db, activityID)
	if err != nil {
		return Stats{}, err
	}
	stats.Occupied = occupied

	// 3. Offer history counters (offersTotal includes SPECIAL re-issues by design,
	// 04 §5.1: offersTotal − candidatesWithOffer = 被特殊重发者).
	if err := s.db.WithContext(ctx).Model(&model.Offer{}).
		Joins("JOIN application ON application.id = offer.application_id").
		Where("application.activity_id = ?", activityID).
		Count(&stats.OffersTotal).Error; err != nil {
		return Stats{}, err
	}
	if err := s.db.WithContext(ctx).Model(&model.Offer{}).
		Joins("JOIN application ON application.id = offer.application_id").
		Where("application.activity_id = ?", activityID).
		Distinct("offer.application_id").
		Count(&stats.CandidatesWithOffer).Error; err != nil {
		return Stats{}, err
	}
	// 4. Mail counters for the activity scope (OFFER + INVITE tasks alike: 需求 70
	// counts the activity's failed mail, and §5.16 lists both types). SENDING is an
	// in-flight lease, reported as pending.
	if err := s.db.WithContext(ctx).Model(&model.MailTask{}).
		Where("scope = ? AND activity_id = ? AND status = ?", model.ScopeActivity, activityID, model.MailTaskFailed).
		Count(&stats.MailFailed).Error; err != nil {
		return Stats{}, err
	}
	if err := s.db.WithContext(ctx).Model(&model.MailTask{}).
		Where("scope = ? AND activity_id = ? AND status IN ?", model.ScopeActivity, activityID,
			[]string{model.MailTaskPending, model.MailTaskSending}).
		Count(&stats.MailPending).Error; err != nil {
		return Stats{}, err
	}
	return stats, nil
}
