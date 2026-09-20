// Package application owns the activity-local candidate record: external single-candidate
// import (idempotent, 04-api-contract.md §8), candidate listing/detail/patch, and the
// INELIGIBLE marking that AUTO refill performs (88.3, 88.4).
//
// Isolation invariants enforced here (88.3, 84 章约束 2/19/20):
//   - Candidate is the platform identity keyed by the immutable student_id; the import
//     path only reuses it, never overwrites accepted_offer_id or any other activity's
//     profile. name/email/qq/class_name/score/rank/status live on Application per activity.
//   - UNIQUE(activity_id, candidate_id) backs the idempotent upsert.
//   - Every write path locks the activity row (SELECT ... FOR UPDATE) first, which both
//     serializes import_order allocation and re-reads ranking_frozen without a race.
package application

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"rollin-backend/internal/audit"
	"rollin-backend/internal/candidate"
	"rollin-backend/internal/errs"
	"rollin-backend/internal/importtoken"
	"rollin-backend/internal/model"
	"rollin-backend/internal/validate"
)

// ImportResult reports one idempotent import outcome (04 §8.1).
type ImportResult struct {
	Created       bool
	ApplicationID uint64
	CandidateID   uint64
	ActivityID    uint64
	Status        string
	RankingDirty  bool
}

// Service is the application domain API.
type Service interface {
	// ImportOne handles a single import-token request end to end: token auth, activity
	// state checks, field validation, candidate find-or-create, application upsert,
	// ranking_dirty=1, token usage counters.
	ImportOne(ctx context.Context, rawToken string, in ImportInput) (ImportResult, error)
	// List returns the paged candidate list (status/keyword/sortBy whitelist).
	List(ctx context.Context, activityID uint64, query ListQuery) ([]Item, int64, error)
	// Get returns one application with its offer history.
	Get(ctx context.Context, activityID, applicationID uint64) (*Detail, error)
	// Patch modifies name/email/score (student_id immutable), sets ranking_dirty=1,
	// rejects when ranking is frozen (RANKING_FROZEN).
	Patch(ctx context.Context, actorID uint64, actorRole string, activityID, applicationID uint64, in PatchInput) (*Detail, error)
	// MarkIneligible implements WAITING → INELIGIBLE for candidates that already accepted
	// elsewhere (02 §2.2, detail never reveals the other activity, P6-3). P5.
	MarkIneligible(ctx context.Context, tx *gorm.DB, applicationID uint64) error
	// NextWaitingByRank exposes the refill cursor to the ranking domain: P5's FillByRank
	// calls it inside its own activity-locked transaction (ranking.Service satisfies
	// its dependency through this narrow surface).
	NextWaitingByRank(ctx context.Context, tx *gorm.DB, activityID uint64) (*model.Application, error)
}

// ImportInput is the external payload: exactly one candidate, no rank, no activity id.
type ImportInput struct {
	StudentID string
	Name      string
	Email     string
	QQ        string
	ClassName string
	Score     int
}

// ListQuery is the candidate list filter; SortBy accepts rank|score|importOrder|createdAt.
type ListQuery struct {
	Status   string
	Keyword  string
	SortBy   string
	Order    string
	Page     int
	PageSize int
}

// Item is one candidate list row.
type Item struct {
	ApplicationID uint64
	CandidateID   uint64
	StudentID     string
	Name          string
	Email         string
	QQ            string
	ClassName     string
	Score         int
	Rank          *int
	ImportOrder   uint64
	Status        string
	// Offer is the current active or most recent offer (nil while none exists).
	Offer *OfferSummary
}

// OfferSummary is the offer projection shared by list and detail. The list renders only
// the §5.2 subset; the detail renders everything (04 §5.3).
type OfferSummary struct {
	OfferID    uint64
	Status     string
	Source     string
	Reason     *string
	CreatedAt  string
	ExpiresAt  string
	SentAt     *string
	AcceptedAt *string
	DeclinedAt *string
	ExpiredAt  *string
	MailStatus string
}

// Detail is the candidate detail response with full offer history (04 §5.3).
type Detail struct {
	Item
	CreatedAt string
	Offers    []OfferSummary
}

// PatchInput carries the optional mutable fields. StudentID is accepted only to reject a
// mismatching value explicitly (04 §5.4: 含 studentId 且与现值不同 → VALIDATION_ERROR;
// 相同则忽略).
type PatchInput struct {
	StudentID *string
	Name      *string
	Email     *string
	Score     *int
}

// Deps wires the collaborators of the application domain.
type Deps struct {
	Repo       Repository
	Candidates candidate.Repository
	Tokens     importtoken.Service
	Audits     audit.Service
}

type service struct {
	db   *gorm.DB
	deps Deps
}

// New wires the application service.
func New(db *gorm.DB, deps Deps) Service {
	if deps.Repo == nil {
		deps.Repo = NewGormRepository(db)
	}
	if deps.Candidates == nil {
		deps.Candidates = candidate.NewGormRepository(db)
	}
	return &service{db: db, deps: deps}
}

// activityLock is the pessimistic activity row lock every import/patch transaction takes
// first: it serializes import_order allocation and makes the ranking_frozen re-read race
// free (05 §6, 88.4).
var activityLock = clause.Locking{Strength: "UPDATE"}

// ImportOne implements the §8.1 validation chain: Bearer token → hash hit → ACTIVE and
// unexpired → activity ACTIVE → ranking not frozen → field validation → transactional
// upsert. Contract errors are raised verbatim (TOKEN_INVALID / TOKEN_EXPIRED /
// ACTIVITY_DISABLED / ACTIVITY_ARCHIVED / RANKING_FROZEN / VALIDATION_ERROR).
func (s *service) ImportOne(ctx context.Context, rawToken string, in ImportInput) (ImportResult, error) {
	if strings.TrimSpace(rawToken) == "" {
		return ImportResult{}, errs.New(errs.CodeTokenInvalid, "缺少 Bearer 导入令牌")
	}
	tok, err := s.deps.Tokens.Authenticate(ctx, rawToken)
	if err != nil {
		switch {
		case errors.Is(err, importtoken.ErrInvalid), errors.Is(err, importtoken.ErrRevoked):
			return ImportResult{}, errs.New(errs.CodeTokenInvalid, "导入令牌无效或已吊销")
		case errors.Is(err, importtoken.ErrExpired):
			return ImportResult{}, errs.New(errs.CodeTokenExpired, "导入令牌已过期")
		default:
			return ImportResult{}, err
		}
	}

	// Normalize once; validation and storage use the same values (04 §1.1: 邮箱小写 +
	// 去首尾空格；student_id 保留原样含前导零).
	in.Name = strings.TrimSpace(in.Name)
	in.Email = validate.NormalizeEmail(in.Email)
	in.QQ = strings.TrimSpace(in.QQ)
	in.ClassName = strings.TrimSpace(in.ClassName)

	var result ImportResult
	txErr := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		repo := s.deps.Repo.WithTx(tx)

		var act model.Activity
		if err := tx.Clauses(activityLock).First(&act, tok.ActivityID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return errs.NotFound("活动不存在")
			}
			return err
		}
		switch act.Status {
		case model.ActivityDisabled:
			return errs.New(errs.CodeActivityDisabled, "活动已被禁用，无法导入")
		case model.ActivityArchived:
			return errs.New(errs.CodeActivityArchived, "活动已归档，操作只读")
		}
		if act.RankingFrozen {
			return errs.New(errs.CodeRankingFrozen, "排名已冻结，禁止导入")
		}
		if err := validateImportInput(in); err != nil {
			return err
		}

		cand, _, err := s.deps.Candidates.WithTx(tx).FindOrCreate(ctx, tx, in.StudentID)
		if err != nil {
			return err
		}

		existing, err := repo.FindByActivityAndCandidate(ctx, tx, act.ID, cand.ID)
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if existing != nil {
			// Idempotent hit (04 §8.1, 88.4.1): same content returns the existing result
			// untouched (import_order stays, no extra dirty); changed content updates
			// name/email/score in place and marks the ranking dirty.
			changes := map[string]any{}
			if existing.Name != in.Name {
				changes["name"] = in.Name
			}
			if existing.Email != in.Email {
				changes["email"] = in.Email
			}
			if existing.QQ != in.QQ {
				changes["qq"] = in.QQ
			}
			if existing.ClassName != in.ClassName {
				changes["class_name"] = in.ClassName
			}
			if existing.Score != in.Score {
				changes["score"] = in.Score
			}
			dirty := act.RankingDirty
			if len(changes) > 0 {
				if err := repo.UpdateColumns(ctx, tx, existing.ID, changes); err != nil {
					return err
				}
				if err := setRankingDirty(ctx, tx, act.ID, true); err != nil {
					return err
				}
				dirty = true
			}
			result = ImportResult{
				Created:       false,
				ApplicationID: existing.ID,
				CandidateID:   cand.ID,
				ActivityID:    act.ID,
				Status:        existing.Status,
				RankingDirty:  dirty,
			}
			return s.deps.Tokens.TouchUsage(ctx, tx, tok.ID)
		}

		// New application: WAITING, rank NULL (until recalculation), import order
		// allocated inside the activity lock (05 §6).
		order, err := repo.NextImportOrder(ctx, tx, act.ID)
		if err != nil {
			return err
		}
		app := model.Application{
			ActivityID:  act.ID,
			CandidateID: cand.ID,
			Name:        in.Name,
			Email:       in.Email,
			QQ:          in.QQ,
			ClassName:   in.ClassName,
			Score:       in.Score,
			ImportOrder: order,
			Status:      model.ApplicationWaiting,
		}
		if err := repo.Insert(ctx, tx, &app); err != nil {
			return err
		}
		if err := setRankingDirty(ctx, tx, act.ID, true); err != nil {
			return err
		}
		result = ImportResult{
			Created:       true,
			ApplicationID: app.ID,
			CandidateID:   cand.ID,
			ActivityID:    act.ID,
			Status:        app.Status,
			RankingDirty:  true,
		}
		return s.deps.Tokens.TouchUsage(ctx, tx, tok.ID)
	})
	if txErr != nil {
		return ImportResult{}, txErr
	}
	return result, nil
}

// validateImportInput enforces the §8.1 field rules: studentId shape (leading zeros kept
// as-is), name required ≤100, normalized email shape, optional qq ≤32 and className ≤100,
// score 1..2147483647 (88.3.7).
func validateImportInput(in ImportInput) error {
	if err := validate.ValidateStudentID(in.StudentID); err != nil {
		return errs.Validation(err.Error())
	}
	if in.Name == "" || len([]rune(in.Name)) > 100 {
		return errs.Validation("姓名必填且不超过 100 字")
	}
	if err := validate.ValidateEmail(in.Email); err != nil {
		return errs.Validation(err.Error())
	}
	if len([]rune(in.QQ)) > 32 {
		return errs.Validation("QQ 不超过 32 字")
	}
	if len([]rune(in.ClassName)) > 100 {
		return errs.Validation("班级不超过 100 字")
	}
	if err := validate.ValidateScore(in.Score); err != nil {
		return errs.Validation(err.Error())
	}
	return nil
}

// sortColumns is the §5.2 sortBy whitelist.
var sortColumns = map[string]string{
	"rank":        "application.`rank`",
	"score":       "application.score",
	"importOrder": "application.import_order",
	"createdAt":   "application.created_at",
}

// List implements §5.2: status filter, keyword over name/email/studentId, whitelisted
// sort with a stable id tiebreaker for pagination.
func (s *service) List(ctx context.Context, activityID uint64, query ListQuery) ([]Item, int64, error) {
	if query.Status != "" && !isValidStatus(query.Status) {
		return nil, 0, errs.Validation("状态筛选值不合法")
	}
	column, ok := sortColumns[query.SortBy]
	if !ok {
		if query.SortBy == "" {
			column = sortColumns["importOrder"]
		} else {
			return nil, 0, errs.Validation("排序字段仅支持 rank / score / importOrder / createdAt")
		}
	}
	direction := "ASC"
	switch strings.ToLower(query.Order) {
	case "", "asc":
	case "desc":
		direction = "DESC"
	default:
		return nil, 0, errs.Validation("排序方向只能是 asc 或 desc")
	}
	page, pageSize := clampPage(query.Page, query.PageSize)

	base := s.db.WithContext(ctx).Model(&model.Application{}).
		Joins("JOIN candidate ON candidate.id = application.candidate_id").
		Where("application.activity_id = ?", activityID)
	if query.Status != "" {
		base = base.Where("application.status = ?", query.Status)
	}
	if keyword := strings.TrimSpace(query.Keyword); keyword != "" {
		escaped := "%" + escapeLike(keyword) + "%"
		base = base.Where("application.name LIKE ? OR application.email LIKE ? OR candidate.student_id LIKE ?",
			escaped, escaped, escaped)
	}

	var total int64
	if err := base.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	var rows []listRow
	if err := base.
		Select("application.*, candidate.student_id AS student_id").
		Order(column + " " + direction + ", application.id ASC").
		Offset((page - 1) * pageSize).Limit(pageSize).
		Scan(&rows).Error; err != nil {
		return nil, 0, err
	}

	offers, err := s.currentOffers(ctx, s.db, applicationIDs(rows))
	if err != nil {
		return nil, 0, err
	}
	items := make([]Item, 0, len(rows))
	for i := range rows {
		items = append(items, Item{
			ApplicationID: rows[i].ID,
			CandidateID:   rows[i].CandidateID,
			StudentID:     rows[i].StudentID,
			Name:          rows[i].Name,
			Email:         rows[i].Email,
			QQ:            rows[i].QQ,
			ClassName:     rows[i].ClassName,
			Score:         rows[i].Score,
			Rank:          rows[i].Rank,
			ImportOrder:   rows[i].ImportOrder,
			Status:        rows[i].Status,
			Offer:         offers[rows[i].ID],
		})
	}
	return items, total, nil
}

// listRow is the joined projection: application columns plus the candidate's student_id.
type listRow struct {
	model.Application
	StudentID string `gorm:"column:student_id"`
}

func applicationIDs(rows []listRow) []uint64 {
	ids := make([]uint64, 0, len(rows))
	for i := range rows {
		ids = append(ids, rows[i].ID)
	}
	return ids
}

// Get implements §5.3; a foreign application resolves to NOT_FOUND.
func (s *service) Get(ctx context.Context, activityID, applicationID uint64) (*Detail, error) {
	var row listRow
	err := s.db.WithContext(ctx).Model(&model.Application{}).
		Joins("JOIN candidate ON candidate.id = application.candidate_id").
		Select("application.*, candidate.student_id AS student_id").
		Where("application.activity_id = ? AND application.id = ?", activityID, applicationID).
		Scan(&row).Error
	if err != nil {
		return nil, err
	}
	if row.ID == 0 {
		return nil, errs.NotFound("候选人不存在")
	}
	var history []model.Offer
	if err := s.db.WithContext(ctx).
		Where("application_id = ?", row.ID).
		Order("id ASC").
		Find(&history).Error; err != nil {
		return nil, err
	}
	mailStatus, err := s.latestMailStatus(ctx, s.db, history)
	if err != nil {
		return nil, err
	}
	detail := &Detail{
		Item: Item{
			ApplicationID: row.ID,
			CandidateID:   row.CandidateID,
			StudentID:     row.StudentID,
			Name:          row.Name,
			Email:         row.Email,
			QQ:            row.QQ,
			ClassName:     row.ClassName,
			Score:         row.Score,
			Rank:          row.Rank,
			ImportOrder:   row.ImportOrder,
			Status:        row.Status,
			Offer:         pickCurrentOffer(history, mailStatus),
		},
		CreatedAt: rfc3339(row.CreatedAt),
		Offers:    summarizeOffers(history, mailStatus),
	}
	return detail, nil
}

// Patch implements §5.4: at least one mutable field, studentId immutable, frozen
// rejection, email normalization, ranking_dirty=1 on any effective change, SCORE_UPDATED
// audit with before/after. Identical values are an idempotent no-op (no dirty, no audit).
func (s *service) Patch(ctx context.Context, actorID uint64, actorRole string, activityID, applicationID uint64, in PatchInput) (*Detail, error) {
	if in.Name == nil && in.Email == nil && in.Score == nil {
		return nil, errs.Validation("至少提供一个修改字段（name / email / score）")
	}
	var detail *Detail
	txErr := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		repo := s.deps.Repo.WithTx(tx)
		app, err := repo.FindByActivityAndID(ctx, tx, activityID, applicationID)
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return errs.NotFound("候选人不存在")
			}
			return err
		}

		var act model.Activity
		if err := tx.Clauses(activityLock).First(&act, activityID).Error; err != nil {
			return err
		}
		switch act.Status {
		case model.ActivityArchived:
			return errs.New(errs.CodeActivityArchived, "活动已归档，操作只读")
		case model.ActivityDisabled:
			return errs.New(errs.CodeActivityDisabled, "活动已被禁用，无法修改")
		}
		if act.RankingFrozen {
			return errs.New(errs.CodeRankingFrozen, "排名已冻结，禁止修改")
		}

		cand, err := s.deps.Candidates.WithTx(tx).FindByID(ctx, tx, app.CandidateID)
		if err != nil {
			return err
		}
		if in.StudentID != nil && *in.StudentID != cand.StudentID {
			return errs.Validation("学号不可修改")
		}

		changes := map[string]any{}
		before := map[string]any{"name": app.Name, "email": app.Email, "score": app.Score}
		if in.Name != nil {
			name := strings.TrimSpace(*in.Name)
			if name == "" || len([]rune(name)) > 100 {
				return errs.Validation("姓名必填且不超过 100 字")
			}
			if name != app.Name {
				changes["name"] = name
			}
		}
		if in.Email != nil {
			email := validate.NormalizeEmail(*in.Email)
			if err := validate.ValidateEmail(email); err != nil {
				return errs.Validation(err.Error())
			}
			if email != app.Email {
				changes["email"] = email
			}
		}
		if in.Score != nil {
			if err := validate.ValidateScore(*in.Score); err != nil {
				return errs.Validation(err.Error())
			}
			if *in.Score != app.Score {
				changes["score"] = *in.Score
			}
		}

		if len(changes) == 0 {
			// Idempotent no-op: nothing changed, ranking stays clean, no audit row.
			return s.loadDetail(ctx, tx, activityID, applicationID, &detail)
		}

		if err := repo.UpdateColumns(ctx, tx, app.ID, changes); err != nil {
			return err
		}
		if err := setRankingDirty(ctx, tx, act.ID, true); err != nil {
			return err
		}

		after := map[string]any{"name": app.Name, "email": app.Email, "score": app.Score}
		for field, value := range changes {
			after[field] = value
		}
		info := audit.FromContext(ctx)
		if err := s.deps.Audits.Record(tx, audit.Entry{
			Scope:         model.ScopeActivity,
			ActivityID:    act.ID,
			ActorType:     actorRole,
			ActorUserID:   &actorID,
			Action:        audit.ActionScoreUpdated,
			TargetType:    "APPLICATION",
			TargetID:      &app.ID,
			ChangeSummary: fmt.Sprintf("修改候选人资料（%s，学号 %s）", app.Name, cand.StudentID),
			Detail:        []byte(fmt.Sprintf(`{"applicationId":%d,"before":%s,"after":%s}`, app.ID, marshalJSON(before), marshalJSON(after))),
			RequestID:     info.RequestID,
			IPAddress:     info.IPAddress,
			UserAgent:     info.UserAgent,
		}); err != nil {
			return err
		}
		return s.loadDetail(ctx, tx, activityID, applicationID, &detail)
	})
	if txErr != nil {
		return nil, txErr
	}
	return detail, nil
}

// loadDetail reloads the detail on the caller's transaction handle (never the base
// handle: inside a transaction it must see the uncommitted state and share the lock).
func (s *service) loadDetail(ctx context.Context, tx *gorm.DB, activityID, applicationID uint64, out **Detail) error {
	var row listRow
	err := tx.WithContext(ctx).Model(&model.Application{}).
		Joins("JOIN candidate ON candidate.id = application.candidate_id").
		Select("application.*, candidate.student_id AS student_id").
		Where("application.activity_id = ? AND application.id = ?", activityID, applicationID).
		Scan(&row).Error
	if err != nil {
		return err
	}
	if row.ID == 0 {
		return errs.NotFound("候选人不存在")
	}
	var history []model.Offer
	if err := tx.WithContext(ctx).Where("application_id = ?", row.ID).Order("id ASC").Find(&history).Error; err != nil {
		return err
	}
	mailStatus, err := s.latestMailStatus(ctx, tx, history)
	if err != nil {
		return err
	}
	*out = &Detail{
		Item: Item{
			ApplicationID: row.ID,
			CandidateID:   row.CandidateID,
			StudentID:     row.StudentID,
			Name:          row.Name,
			Email:         row.Email,
			QQ:            row.QQ,
			ClassName:     row.ClassName,
			Score:         row.Score,
			Rank:          row.Rank,
			ImportOrder:   row.ImportOrder,
			Status:        row.Status,
			Offer:         pickCurrentOffer(history, mailStatus),
		},
		CreatedAt: rfc3339(row.CreatedAt),
		Offers:    summarizeOffers(history, mailStatus),
	}
	return nil
}

// MarkIneligible implements WAITING → INELIGIBLE (02 §2.2); P5 refill loops call it.
func (s *service) MarkIneligible(ctx context.Context, tx *gorm.DB, applicationID uint64) error {
	repo := s.deps.Repo.WithTx(tx)
	won, err := repo.UpdateStatus(ctx, tx, applicationID, model.ApplicationWaiting, model.ApplicationIneligible)
	if err != nil {
		return err
	}
	if !won {
		return errs.Conflict("报名记录状态已变化，无法标记失格")
	}
	return nil
}

// NextWaitingByRank delegates the refill cursor to the repository on the caller's
// transaction handle (ranking.FillByRank holds the activity row lock around it).
func (s *service) NextWaitingByRank(ctx context.Context, tx *gorm.DB, activityID uint64) (*model.Application, error) {
	return s.deps.Repo.WithTx(tx).NextWaitingByRank(ctx, tx, activityID)
}

// ---------- offer projection helpers ----------

// currentOffers resolves the "current effective or most recent" offer per application
// (04 §5.2) plus its latest mail task status, in two batched queries. exec is the caller's
// handle (base or transaction).
func (s *service) currentOffers(ctx context.Context, exec *gorm.DB, applicationIDs []uint64) (map[uint64]*OfferSummary, error) {
	out := make(map[uint64]*OfferSummary, len(applicationIDs))
	if len(applicationIDs) == 0 {
		return out, nil
	}
	var history []model.Offer
	if err := exec.WithContext(ctx).
		Where("application_id IN ?", applicationIDs).
		Order("id ASC").
		Find(&history).Error; err != nil {
		return nil, err
	}
	if len(history) == 0 {
		return out, nil
	}
	mailStatus, err := s.latestMailStatus(ctx, exec, history)
	if err != nil {
		return nil, err
	}
	byApplication := make(map[uint64][]model.Offer)
	for _, offer := range history {
		byApplication[offer.ApplicationID] = append(byApplication[offer.ApplicationID], offer)
	}
	for applicationID, offers := range byApplication {
		out[applicationID] = pickCurrentOffer(offers, mailStatus)
	}
	return out, nil
}

// latestMailStatus maps offer id → the newest mail task's status (OFFER type only).
func (s *service) latestMailStatus(ctx context.Context, exec *gorm.DB, history []model.Offer) (map[uint64]string, error) {
	out := make(map[uint64]string)
	if len(history) == 0 {
		return out, nil
	}
	offerIDs := make([]uint64, 0, len(history))
	for i := range history {
		offerIDs = append(offerIDs, history[i].ID)
	}
	var tasks []model.MailTask
	if err := exec.WithContext(ctx).
		Where("mail_type = ? AND offer_id IN ?", model.MailTypeOffer, offerIDs).
		Order("id ASC").
		Find(&tasks).Error; err != nil {
		return nil, err
	}
	for _, task := range tasks {
		if task.OfferID != nil {
			out[*task.OfferID] = task.Status // later id wins: newest task per offer
		}
	}
	return out, nil
}

// pickCurrentOffer prefers the active (PENDING/ACCEPTED) offer, falling back to the most
// recent one; history never exposes more than the summary fields on the list.
func pickCurrentOffer(history []model.Offer, mailStatus map[uint64]string) *OfferSummary {
	if len(history) == 0 {
		return nil
	}
	var active, newest *model.Offer
	for i := range history {
		offer := &history[i]
		if newest == nil || offer.ID > newest.ID {
			newest = offer
		}
		if offer.Status == model.OfferPending || offer.Status == model.OfferAccepted {
			if active == nil || offer.ID > active.ID {
				active = offer
			}
		}
	}
	chosen := active
	if chosen == nil {
		chosen = newest
	}
	if chosen == nil {
		return nil
	}
	summary := summarizeOffer(*chosen, mailStatus)
	return &summary
}

func summarizeOffers(history []model.Offer, mailStatus map[uint64]string) []OfferSummary {
	out := make([]OfferSummary, 0, len(history))
	for i := range history {
		out = append(out, summarizeOffer(history[i], mailStatus))
	}
	return out
}

func summarizeOffer(offer model.Offer, mailStatus map[uint64]string) OfferSummary {
	summary := OfferSummary{
		OfferID:    offer.ID,
		Status:     offer.Status,
		Source:     offer.Source,
		Reason:     offer.Reason,
		CreatedAt:  rfc3339(offer.CreatedAt),
		ExpiresAt:  rfc3339(offer.ExpiresAt),
		SentAt:     rfc3339Ptr(offer.SentAt),
		AcceptedAt: rfc3339Ptr(offer.AcceptedAt),
		DeclinedAt: rfc3339Ptr(offer.DeclinedAt),
		ExpiredAt:  rfc3339Ptr(offer.ExpiredAt),
		MailStatus: mailStatus[offer.ID],
	}
	return summary
}

// ---------- small shared helpers ----------

// setRankingDirty flips activity.ranking_dirty inside the caller's transaction
// (02 §1.2: 导入成功、修改 score 成功 → 置 1；重算成功 → 清 0).
func setRankingDirty(ctx context.Context, tx *gorm.DB, activityID uint64, dirty bool) error {
	return tx.WithContext(ctx).Model(&model.Activity{}).
		Where("id = ?", activityID).
		Update("ranking_dirty", dirty).Error
}

func isValidStatus(status string) bool {
	switch status {
	case model.ApplicationWaiting, model.ApplicationOffered, model.ApplicationAccepted,
		model.ApplicationDeclined, model.ApplicationExpired, model.ApplicationIneligible:
		return true
	}
	return false
}

func clampPage(page, pageSize int) (int, int) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = 20
	}
	if pageSize > 200 {
		pageSize = 200
	}
	return page, pageSize
}

// escapeLike neutralizes LIKE wildcards in user-provided keywords (MySQL default
// backslash escape).
func escapeLike(keyword string) string {
	replacer := strings.NewReplacer("\\", "\\\\", "%", "\\%", "_", "\\_")
	return replacer.Replace(keyword)
}

func rfc3339(t time.Time) string { return t.UTC().Format(time.RFC3339) }

func rfc3339Ptr(t *time.Time) *string {
	if t == nil {
		return nil
	}
	formatted := rfc3339(*t)
	return &formatted
}

// marshalJSON renders a small map for audit detail without importing encoding/json at
// every call site (values are strings/ints only).
func marshalJSON(value map[string]any) string {
	keys := make([]string, 0, len(value))
	for key := range value {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		switch typed := value[key].(type) {
		case string:
			parts = append(parts, fmt.Sprintf(`%q:%q`, key, typed))
		case int:
			parts = append(parts, fmt.Sprintf(`%q:%d`, key, typed))
		default:
			parts = append(parts, fmt.Sprintf(`%q:%v`, key, typed))
		}
	}
	return "{" + strings.Join(parts, ",") + "}"
}
