// Package settings holds the platform parameters the super admin may tune at runtime.
// Values live in the database as text, but every key is typed and validated here so the
// HTTP layer never has to guess, and so a bad value can never reach a worker.
// Key names are part of the API contract (04-api-contract.md §2.4).
package settings

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"rollin-backend/internal/errs"
	"rollin-backend/internal/model"
)

const (
	KeySiteName           = "siteName"
	KeyAdminBaseURL       = "adminBaseUrl"
	KeyPublicBaseURL      = "publicBaseUrl"
	KeyDefaultOfferMode   = "defaultOfferMode"
	KeyDefaultOfferExpire = "defaultOfferExpireHours"
	KeyInviteExpireHours  = "inviteExpireHours"
	KeySessionHours       = "sessionHours"
)

// Kind decides how a value is validated and which form control the console renders.
type Kind string

const (
	KindText Kind = "text"
	KindInt  Kind = "int"
	KindURL  Kind = "url"
	KindEnum Kind = "enum"
)

type Definition struct {
	Key         string   `json:"key"`
	Label       string   `json:"label"`
	Kind        Kind     `json:"kind"`
	Default     string   `json:"default"`
	Description string   `json:"description"`
	Unit        string   `json:"unit,omitempty"`
	Min         int      `json:"min,omitempty"`
	Max         int      `json:"max,omitempty"`
	Options     []string `json:"options,omitempty"`
}

// Definitions is the closed whitelist of tunable keys. inviteExpireHours defaults to 72
// now (was 168 pre-V1, replaced per 05-data-model.md §17).
var definitions = []Definition{
	{Key: KeySiteName, Label: "平台名称", Kind: KindText, Default: "Rollin", Description: "显示在登录页、管理后台标题和邀请邮件中的名称。"},
	{Key: KeyAdminBaseURL, Label: "管理后台地址", Kind: KindURL, Default: "", Description: "用于生成账号邀请链接，必须是对外可访问的管理端域名。"},
	{Key: KeyPublicBaseURL, Label: "候选人访问地址", Kind: KindURL, Default: "", Description: "用于生成候选人 Offer 链接，必须是候选人可访问的域名（t.xxx.xxx）。"},
	{Key: KeyDefaultOfferMode, Label: "默认发放模式", Kind: KindEnum, Default: "AUTO", Options: []string{"AUTO", "MANUAL"}, Description: "创建招新活动时的默认发放方式：AUTO 自动递补，MANUAL 逐个确认。"},
	{Key: KeyDefaultOfferExpire, Label: "默认 Offer 有效期", Kind: KindInt, Default: "72", Unit: "小时", Min: 1, Max: 720, Description: "创建招新活动时候选人确认录取的默认时限。"},
	{Key: KeyInviteExpireHours, Label: "邀请链接有效期", Kind: KindInt, Default: "72", Unit: "小时", Min: 1, Max: 720, Description: "邀请邮件中的链接在多长时间内有效，超时后可重新发送。"},
	{Key: KeySessionHours, Label: "登录会话有效期", Kind: KindInt, Default: "24", Unit: "小时", Min: 1, Max: 720, Description: "登录状态的最长保持时间，修改后对新登录生效。"},
}

// Store caches rows briefly: workers read a few keys per tick and the console reads all
// of them on every page load, so a short TTL keeps the load off MySQL without making a
// save visible late.
type Store struct {
	db       *gorm.DB
	fallback map[string]string
	ttl      time.Duration
	mu       sync.RWMutex
	cached   map[string]string
	loadedAt time.Time
}

func NewStore(db *gorm.DB, fallback map[string]string, ttl time.Duration) *Store {
	return &Store{db: db, fallback: fallback, ttl: ttl, cached: map[string]string{}}
}

func Definitions() []Definition { return definitions }

func definition(key string) (Definition, bool) {
	for _, item := range definitions {
		if item.Key == key {
			return item, true
		}
	}
	return Definition{}, false
}

// All returns every known key with its effective value, so a caller can render a form
// without knowing which keys were ever customized.
func (s *Store) All(ctx context.Context) map[string]string {
	values := s.snapshot(ctx)
	out := make(map[string]string, len(definitions))
	for _, item := range definitions {
		out[item.Key] = values[item.Key]
	}
	return out
}

func (s *Store) Get(ctx context.Context, key string) string { return s.snapshot(ctx)[key] }

func (s *Store) Int(ctx context.Context, key string) int {
	value, err := strconv.Atoi(strings.TrimSpace(s.Get(ctx, key)))
	if err != nil {
		return 0
	}
	return value
}

// BaseURL returns a link prefix without a trailing slash so callers can concatenate paths.
func (s *Store) BaseURL(ctx context.Context, key string) string {
	return strings.TrimRight(s.Get(ctx, key), "/")
}

func (s *Store) snapshot(ctx context.Context) map[string]string {
	s.mu.RLock()
	if time.Since(s.loadedAt) < s.ttl && s.cached != nil {
		values := s.cached
		s.mu.RUnlock()
		return values
	}
	s.mu.RUnlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	if time.Since(s.loadedAt) < s.ttl && s.cached != nil {
		return s.cached
	}
	values := make(map[string]string, len(definitions))
	for _, item := range definitions {
		value := strings.TrimSpace(s.fallback[item.Key])
		if value == "" {
			value = item.Default
		}
		values[item.Key] = value
	}
	var rows []model.PlatformSetting
	if err := s.db.WithContext(ctx).Find(&rows).Error; err == nil {
		for _, row := range rows {
			if _, ok := definition(row.Key); ok {
				values[row.Key] = strings.TrimSpace(row.Value)
			}
		}
	}
	s.cached = values
	s.loadedAt = time.Now()
	return values
}

// Update validates the whole payload before writing anything, so a partially valid form
// submission never leaves half of the parameters stored.
func (s *Store) Update(ctx context.Context, input map[string]string) error {
	if len(input) == 0 {
		return errs.Validation("没有需要保存的参数")
	}
	cleaned := make(map[string]string, len(input))
	for key, value := range input {
		item, ok := definition(key)
		if !ok {
			return errs.Validation("存在无法识别的平台参数：" + key)
		}
		normalized, err := normalize(item, value)
		if err != nil {
			return errs.Validation(item.Label + "：" + err.Error())
		}
		cleaned[key] = normalized
	}
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for key, value := range cleaned {
			row := model.PlatformSetting{Key: key, Value: value}
			if err := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "key"}}, DoUpdates: clause.AssignmentColumns([]string{"value", "updated_at"})}).Create(&row).Error; err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return err
	}
	s.Refresh()
	return nil
}

// Refresh drops the cache so the next read observes values written outside this process.
func (s *Store) Refresh() {
	s.mu.Lock()
	s.loadedAt = time.Time{}
	s.mu.Unlock()
}

func normalize(item Definition, value string) (string, error) {
	trimmed := strings.TrimSpace(value)
	switch item.Kind {
	case KindInt:
		parsed, err := strconv.Atoi(trimmed)
		if err != nil {
			return "", errors.New("必须是整数")
		}
		if item.Min > 0 && parsed < item.Min {
			return "", fmt.Errorf("不能小于 %d", item.Min)
		}
		if item.Max > 0 && parsed > item.Max {
			return "", fmt.Errorf("不能大于 %d", item.Max)
		}
		return strconv.Itoa(parsed), nil
	case KindEnum:
		for _, option := range item.Options {
			if trimmed == option {
				return trimmed, nil
			}
		}
		return "", fmt.Errorf("只能是 %s", strings.Join(item.Options, " / "))
	case KindURL:
		if trimmed == "" {
			return "", errors.New("不能为空")
		}
		parsed, err := url.Parse(trimmed)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
			return "", errors.New("必须是完整的 http(s) 地址，例如 https://admin.example.edu.cn")
		}
		return strings.TrimRight(trimmed, "/"), nil
	default:
		if len([]rune(trimmed)) > 60 {
			return "", errors.New("不能超过 60 个字符")
		}
		if trimmed == "" {
			return "", errors.New("不能为空")
		}
		return trimmed, nil
	}
}
