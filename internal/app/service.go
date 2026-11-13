package app

import (
	"context"
	"errors"
	"strings"
	"time"

	"clipsyncpro/internal/domain"
	"clipsyncpro/internal/security"
	"clipsyncpro/internal/storage"
)

var (
	ErrAccountRequired   = errors.New("账户不能为空")
	ErrPasswordRequired  = errors.New("密码不能为空")
	ErrRevealRequired    = errors.New("请先点击查看")
	ErrSensitiveRequired = errors.New("仅账户/密码记录支持修改备注")
	ErrTextRequired      = errors.New("图片不能标记为账户或密码")
)

type Service struct {
	store     storage.Store
	secret    string
	userID    string
	deviceID  string
	platform  domain.Platform
	localOnly bool
	now       func() time.Time
}

type Config struct {
	Secret    string
	UserID    string
	DeviceID  string
	Platform  domain.Platform
	LocalOnly bool
}

const syncStatusLocal domain.SyncStatus = "local"

func NewService(store storage.Store, cfg Config) *Service {
	userID := cfg.UserID
	if userID == "" {
		userID = "local"
	}
	deviceID := cfg.DeviceID
	if deviceID == "" {
		deviceID = "windows"
	}
	platform := cfg.Platform
	if platform == "" {
		platform = domain.PlatformWindows
	}
	return &Service{
		store:     store,
		secret:    cfg.Secret,
		userID:    userID,
		deviceID:  deviceID,
		platform:  platform,
		localOnly: cfg.LocalOnly,
		now:       time.Now,
	}
}

func (s *Service) defaultSyncStatus() domain.SyncStatus {
	if s.localOnly {
		return syncStatusLocal
	}
	return domain.SyncPending
}

func (s *Service) CaptureText(ctx context.Context, content string) (domain.ClipboardRecord, error) {
	now := s.now()
	rec, err := domain.NewClipboardRecord(s.userID, s.deviceID, s.platform, content, now)
	if err != nil {
		return domain.ClipboardRecord{}, err
	}
	rec.SyncStatus = s.defaultSyncStatus()
	if err := s.store.UpsertRecord(ctx, rec); err != nil {
		return domain.ClipboardRecord{}, err
	}
	if err := s.enqueueRecord(ctx, storage.QueueCreate, rec); err != nil {
		return domain.ClipboardRecord{}, err
	}
	return rec, nil
}

func (s *Service) CaptureImage(ctx context.Context, mimeType string, data []byte) (domain.ClipboardRecord, error) {
	now := s.now()
	rec, err := domain.NewClipboardImageRecord(s.userID, s.deviceID, s.platform, mimeType, data, now)
	if err != nil {
		return domain.ClipboardRecord{}, err
	}
	rec.SyncStatus = s.defaultSyncStatus()
	if err := s.store.UpsertRecord(ctx, rec); err != nil {
		return domain.ClipboardRecord{}, err
	}
	if err := s.enqueueRecord(ctx, storage.QueueCreate, rec); err != nil {
		return domain.ClipboardRecord{}, err
	}
	return rec, nil
}

func (s *Service) List(ctx context.Context, filter storage.Filter) ([]domain.ClipboardRecord, error) {
	if filter.UserID == "" {
		filter.UserID = s.userID
	}
	if filter.Keyword != "" {
		filter.KeywordHashes = security.BuildQueryIndex(s.secret, filter.Keyword)
	}
	return s.store.ListRecordSummaries(ctx, filter)
}

func (s *Service) MarkPassword(ctx context.Context, id, account, remark string) (domain.ClipboardRecord, error) {
	account = strings.TrimSpace(account)
	if account == "" {
		return domain.ClipboardRecord{}, ErrAccountRequired
	}
	return s.markSensitive(ctx, id, domain.SensitivePassword, account, "", remark)
}

func (s *Service) MarkAccount(ctx context.Context, id, password, remark string) (domain.ClipboardRecord, error) {
	password = strings.TrimSpace(password)
	if password == "" {
		return domain.ClipboardRecord{}, ErrPasswordRequired
	}
	return s.markSensitive(ctx, id, domain.SensitiveAccount, "", password, remark)
}

func (s *Service) markSensitive(ctx context.Context, id string, kind domain.SensitiveType, account, password, remark string) (domain.ClipboardRecord, error) {
	rec, err := s.store.GetRecord(ctx, id)
	if err != nil {
		return domain.ClipboardRecord{}, err
	}
	if rec.ContentType == domain.ContentImage {
		return domain.ClipboardRecord{}, ErrTextRequired
	}
	content := rec.ContentText
	if rec.IsSensitive {
		revealed, err := s.Reveal(ctx, id)
		if err != nil {
			return domain.ClipboardRecord{}, err
		}
		content = revealed.Content
		if account == "" {
			account = revealed.Account
		}
		if password == "" {
			password = revealed.Password
		}
	}
	if kind == domain.SensitivePassword {
		password = content
	} else {
		account = content
	}
	contentBox, err := security.EncryptString(s.secret, content)
	if err != nil {
		return domain.ClipboardRecord{}, err
	}
	accountBox, err := security.EncryptString(s.secret, account)
	if err != nil {
		return domain.ClipboardRecord{}, err
	}
	passwordBox, err := security.EncryptString(s.secret, password)
	if err != nil {
		return domain.ClipboardRecord{}, err
	}
	remarkBox, err := security.EncryptString(s.secret, remark)
	if err != nil {
		return domain.ClipboardRecord{}, err
	}
	rec.ContentText = ""
	rec.ContentCipher = contentBox
	rec.AccountCipher = accountBox
	rec.PasswordCipher = passwordBox
	rec.RemarkCipher = remarkBox
	rec.IsSensitive = true
	rec.SensitiveType = kind
	rec.SearchIndex = security.BuildSearchIndex(s.secret, content, account, password, remark)
	rec.UpdatedAt = s.now()
	rec.SyncStatus = s.defaultSyncStatus()
	if err := s.store.UpsertRecord(ctx, rec); err != nil {
		return domain.ClipboardRecord{}, err
	}
	if err := s.enqueueRecord(ctx, storage.QueueUpdate, rec); err != nil {
		return domain.ClipboardRecord{}, err
	}
	return rec, nil
}

func (s *Service) Reveal(ctx context.Context, id string) (domain.RevealedSensitive, error) {
	rec, err := s.store.GetRecord(ctx, id)
	if err != nil {
		return domain.RevealedSensitive{}, err
	}
	if !rec.IsSensitive {
		return domain.RevealedSensitive{
			RecordID: rec.ID,
			Content:  rec.ContentText,
			Expires:  s.now().Add(30 * time.Minute).Format(time.RFC3339),
		}, nil
	}
	content, err := security.DecryptString(s.secret, rec.ContentCipher)
	if err != nil {
		return domain.RevealedSensitive{}, err
	}
	account, err := security.DecryptString(s.secret, rec.AccountCipher)
	if err != nil {
		return domain.RevealedSensitive{}, err
	}
	password, err := security.DecryptString(s.secret, rec.PasswordCipher)
	if err != nil {
		return domain.RevealedSensitive{}, err
	}
	remark, err := security.DecryptString(s.secret, rec.RemarkCipher)
	if err != nil {
		return domain.RevealedSensitive{}, err
	}
	twoFactor := ""
	if rec.TwoFactorCipher != nil {
		twoFactor, _ = security.DecryptString(s.secret, rec.TwoFactorCipher)
	}
	return domain.RevealedSensitive{
		RecordID:  rec.ID,
		Content:   content,
		Platform:  rec.TargetPlatform,
		Account:   account,
		Password:  password,
		TwoFactor: twoFactor,
		Remark:    remark,
		Expires:   s.now().Add(30 * time.Minute).Format(time.RFC3339),
	}, nil
}

// UpdateCredentials 更新账户密码五要素凭据：平台、账户、密码、2FA 密钥、备注，并自动加密落库与标记收藏。
func (s *Service) UpdateCredentials(ctx context.Context, id, platform, account, password, twoFactor, remark string, isFavorite bool) (domain.ClipboardRecord, error) {
	rec, err := s.store.GetRecord(ctx, id)
	if err != nil {
		return domain.ClipboardRecord{}, err
	}
	contentBox, err := security.EncryptString(s.secret, password)
	if err != nil {
		return domain.ClipboardRecord{}, err
	}
	accountBox, err := security.EncryptString(s.secret, account)
	if err != nil {
		return domain.ClipboardRecord{}, err
	}
	passwordBox, err := security.EncryptString(s.secret, password)
	if err != nil {
		return domain.ClipboardRecord{}, err
	}
	remarkBox, err := security.EncryptString(s.secret, remark)
	if err != nil {
		return domain.ClipboardRecord{}, err
	}
	twoFactorBox, err := security.EncryptString(s.secret, twoFactor)
	if err != nil {
		return domain.ClipboardRecord{}, err
	}

	rec.IsSensitive = true
	rec.SensitiveType = domain.SensitiveAccount
	rec.TargetPlatform = strings.TrimSpace(platform)
	rec.AccountCipher = accountBox
	rec.PasswordCipher = passwordBox
	rec.TwoFactorCipher = twoFactorBox
	rec.RemarkCipher = remarkBox
	rec.ContentCipher = contentBox

	// ContentText 存储 "平台 账户" 摘要，保证未解密时列表能直接展示
	summaryText := strings.TrimSpace(platform)
	if summaryText != "" && strings.TrimSpace(account) != "" {
		summaryText += " " + strings.TrimSpace(account)
	} else if strings.TrimSpace(account) != "" {
		summaryText = strings.TrimSpace(account)
	}
	rec.ContentText = summaryText
	rec.SearchIndex = security.BuildSearchIndex(s.secret, platform, account, password, twoFactor, remark)

	if isFavorite {
		now := s.now()
		rec.IsFavorite = true
		rec.FavoriteAt = &now
	}
	rec.UpdatedAt = s.now()
	rec.SyncStatus = s.defaultSyncStatus()

	if err := s.store.UpsertRecord(ctx, rec); err != nil {
		return domain.ClipboardRecord{}, err
	}
	if err := s.enqueueRecord(ctx, storage.QueueUpdate, rec); err != nil {
		return domain.ClipboardRecord{}, err
	}
	return rec, nil
}

func (s *Service) UpdateRemark(ctx context.Context, id, remark string) (domain.ClipboardRecord, error) {
	rec, err := s.store.GetRecord(ctx, id)
	if err != nil {
		return domain.ClipboardRecord{}, err
	}
	if !rec.IsSensitive {
		return domain.ClipboardRecord{}, ErrSensitiveRequired
	}
	content, err := security.DecryptString(s.secret, rec.ContentCipher)
	if err != nil {
		return domain.ClipboardRecord{}, err
	}
	account, err := security.DecryptString(s.secret, rec.AccountCipher)
	if err != nil {
		return domain.ClipboardRecord{}, err
	}
	password, err := security.DecryptString(s.secret, rec.PasswordCipher)
	if err != nil {
		return domain.ClipboardRecord{}, err
	}
	remarkBox, err := security.EncryptString(s.secret, remark)
	if err != nil {
		return domain.ClipboardRecord{}, err
	}
	rec.RemarkCipher = remarkBox
	rec.SearchIndex = security.BuildSearchIndex(s.secret, content, account, password, remark)
	rec.UpdatedAt = s.now()
	rec.SyncStatus = s.defaultSyncStatus()
	if err := s.store.UpsertRecord(ctx, rec); err != nil {
		return domain.ClipboardRecord{}, err
	}
	if err := s.enqueueRecord(ctx, storage.QueueUpdate, rec); err != nil {
		return domain.ClipboardRecord{}, err
	}
	return rec, nil
}

// SetFavorite 更新收藏状态并写入持久化同步队列，保证离线切换收藏后仍能最终同步到服务端与其他设备。
func (s *Service) SetFavorite(ctx context.Context, id string, isFavorite bool) (domain.ClipboardRecord, error) {
	if err := s.store.SetFavorite(ctx, id, isFavorite); err != nil {
		return domain.ClipboardRecord{}, err
	}
	rec, err := s.store.GetRecord(ctx, id)
	if err != nil {
		return domain.ClipboardRecord{}, err
	}
	if s.localOnly && rec.SyncStatus != syncStatusLocal {
		rec.SyncStatus = syncStatusLocal
		_ = s.store.UpsertRecord(ctx, rec)
	}
	if err := s.enqueueRecord(ctx, storage.QueueUpdate, rec); err != nil {
		return domain.ClipboardRecord{}, err
	}
	return rec, nil
}

// UpdateRecordContent 更新记录内容，支持普通文本更新、图片备注追加、敏感记录重新加密，并可选标记收藏。
func (s *Service) UpdateRecordContent(ctx context.Context, id, newContent string, isFavorite bool) (domain.ClipboardRecord, error) {
	rec, err := s.store.GetRecord(ctx, id)
	if err != nil {
		return domain.ClipboardRecord{}, err
	}
	if rec.ContentType == domain.ContentImage {
		rec.ContentText = newContent
	} else if rec.IsSensitive {
		contentBox, err := security.EncryptString(s.secret, newContent)
		if err != nil {
			return domain.ClipboardRecord{}, err
		}
		account, _ := security.DecryptString(s.secret, rec.AccountCipher)
		password, _ := security.DecryptString(s.secret, rec.PasswordCipher)
		remark, _ := security.DecryptString(s.secret, rec.RemarkCipher)
		rec.ContentCipher = contentBox
		rec.SearchIndex = security.BuildSearchIndex(s.secret, newContent, account, password, remark)
	} else {
		rec.ContentText = newContent
		rec.ContentHash = domain.SHA256Hex(newContent)
	}

	if isFavorite {
		now := s.now()
		rec.IsFavorite = true
		rec.FavoriteAt = &now
	}
	rec.UpdatedAt = s.now()
	rec.SyncStatus = s.defaultSyncStatus()

	if err := s.store.UpsertRecord(ctx, rec); err != nil {
		return domain.ClipboardRecord{}, err
	}
	if err := s.enqueueRecord(ctx, storage.QueueUpdate, rec); err != nil {
		return domain.ClipboardRecord{}, err
	}
	return rec, nil
}

func (s *Service) Delete(ctx context.Context, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	if err := s.store.DeleteRecords(ctx, ids); err != nil {
		return err
	}
	if s.localOnly {
		return nil
	}
	for _, id := range ids {
		if _, err := s.store.Enqueue(ctx, storage.SyncQueueItem{
			RecordID:  id,
			Action:    storage.QueueDelete,
			Status:    storage.QueuePending,
			CreatedAt: s.now(),
		}); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) enqueueRecord(ctx context.Context, action storage.QueueAction, rec domain.ClipboardRecord) error {
	if s.localOnly {
		return nil
	}
	_, err := s.store.Enqueue(ctx, storage.SyncQueueItem{
		RecordID:  rec.ID,
		Action:    action,
		Status:    storage.QueuePending,
		CreatedAt: s.now(),
	})
	return err
}
