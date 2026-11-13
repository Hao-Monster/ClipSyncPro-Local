package domain

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/url"
	"regexp"
	"strings"
	"time"
)

type ContentType string

const (
	ContentText   ContentType = "text"
	ContentURL    ContentType = "url"
	ContentNumber ContentType = "number"
	ContentImage  ContentType = "image"
	ContentOther  ContentType = "other"
)

type SensitiveType string

const (
	SensitiveNone     SensitiveType = ""
	SensitiveAccount  SensitiveType = "account"
	SensitivePassword SensitiveType = "password"
)

type SyncStatus string

const (
	SyncPending SyncStatus = "pending"
	SyncSyncing SyncStatus = "syncing"
	SyncSynced  SyncStatus = "synced"
	SyncFailed  SyncStatus = "failed"
)

type Platform string

const (
	PlatformWindows Platform = "windows"
	PlatformAndroid Platform = "android"
	PlatformServer  Platform = "server"
)

type CipherText struct {
	Algorithm  string `json:"algorithm"`
	KDF        string `json:"kdf"`
	Iterations int    `json:"iterations"`
	Salt       string `json:"salt"`
	Nonce      string `json:"nonce"`
	Ciphertext string `json:"ciphertext"`
}

func (c CipherText) IsZero() bool {
	return c.Ciphertext == ""
}

type ClipboardRecord struct {
	ID              string        `json:"id"`
	UserID          string        `json:"user_id"`
	ContentType     ContentType   `json:"content_type"`
	ContentText     string        `json:"content_text,omitempty"`
	ImageMIMEType   string        `json:"-"`
	ImageData       []byte        `json:"-"`
	ContentCipher   *CipherText   `json:"content_cipher,omitempty"`
	ContentHash     string        `json:"content_hash"`
	IsSensitive     bool          `json:"is_sensitive"`
	SensitiveType   SensitiveType `json:"sensitive_type,omitempty"`
	AccountCipher   *CipherText   `json:"account_cipher,omitempty"`
	PasswordCipher  *CipherText   `json:"password_cipher,omitempty"`
	RemarkCipher    *CipherText   `json:"remark_cipher,omitempty"`
	TwoFactorCipher *CipherText   `json:"two_factor_cipher,omitempty"`
	TargetPlatform  string        `json:"target_platform,omitempty"`
	SearchIndex     []string      `json:"search_index,omitempty"`
	SourceDeviceID  string        `json:"source_device_id"`
	SourcePlatform  Platform      `json:"source_platform"`
	ClientCreatedAt time.Time     `json:"client_created_at"`
	ServerCreatedAt *time.Time    `json:"server_created_at,omitempty"`
	UpdatedAt       time.Time     `json:"updated_at"`
	DeletedAt       *time.Time    `json:"deleted_at,omitempty"`
	IsFavorite      bool          `json:"is_favorite"`
	FavoriteAt      *time.Time    `json:"favorite_at,omitempty"`
	SyncStatus      SyncStatus    `json:"sync_status"`
}

type RevealedSensitive struct {
	RecordID  string `json:"record_id"`
	Content   string `json:"content"`
	Platform  string `json:"platform,omitempty"`
	Account   string `json:"account"`
	Password  string `json:"password"`
	TwoFactor string `json:"two_factor,omitempty"`
	Remark    string `json:"remark"`
	Expires   string `json:"expires"`
}

type SyncEventType string

const (
	EventRecordCreated    SyncEventType = "record_created"
	EventRecordUpdated    SyncEventType = "record_updated"
	EventRecordDeleted    SyncEventType = "record_deleted"
	EventStorageRecovered SyncEventType = "storage_recovered"
)

type SyncEvent struct {
	EventType SyncEventType    `json:"event_type"`
	Record    *ClipboardRecord `json:"record,omitempty"`
	RecordID  string           `json:"record_id,omitempty"`
	Occurred  time.Time        `json:"occurred_at"`
	DeviceID  string           `json:"device_id,omitempty"`
	RequestID string           `json:"request_id,omitempty"`
}

func NewClipboardRecord(userID, deviceID string, platform Platform, content string, now time.Time) (ClipboardRecord, error) {
	id, err := NewRecordID(now)
	if err != nil {
		return ClipboardRecord{}, err
	}
	return ClipboardRecord{
		ID:              id,
		UserID:          userID,
		ContentType:     DetectContentType(content),
		ContentText:     content,
		ContentHash:     SHA256Hex(content),
		IsSensitive:     false,
		SourceDeviceID:  deviceID,
		SourcePlatform:  platform,
		ClientCreatedAt: now,
		UpdatedAt:       now,
		SyncStatus:      SyncPending,
	}, nil
}

func NewRecordID(now time.Time) (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return "rec_" + now.UTC().Format("20060102150405") + "_" + hex.EncodeToString(b[:]), nil
}

func SHA256Hex(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

var numericPattern = regexp.MustCompile(`^[+-]?([0-9]+|[0-9]*\.[0-9]+)$`)

func DetectContentType(content string) ContentType {
	trimmed := strings.TrimSpace(content)
	if trimmed == "" {
		return ContentText
	}
	if numericPattern.MatchString(trimmed) {
		return ContentNumber
	}
	parsed, err := url.ParseRequestURI(trimmed)
	if err == nil && parsed.Scheme != "" {
		return ContentURL
	}
	return ContentText
}

func FormatMinute(t time.Time) string {
	return t.Local().Format("200601021504")
}

func ParseFilterDate(raw string) (time.Time, time.Time, error) {
	if len(raw) != 8 {
		return time.Time{}, time.Time{}, errors.New("请输入有效日期，格式为 yyyyMMdd，例如 20260601")
	}
	for _, r := range raw {
		if r < '0' || r > '9' {
			return time.Time{}, time.Time{}, errors.New("请输入有效日期，格式为 yyyyMMdd，例如 20260601")
		}
	}
	start, err := time.ParseInLocation("20060102", raw, time.Local)
	if err != nil {
		return time.Time{}, time.Time{}, errors.New("请输入有效日期，格式为 yyyyMMdd，例如 20260601")
	}
	return start, start.Add(24*time.Hour - time.Nanosecond), nil
}

func ContainsAll(haystack []string, needles []string) bool {
	if len(needles) == 0 {
		return true
	}
	index := make(map[string]struct{}, len(haystack))
	for _, item := range haystack {
		index[item] = struct{}{}
	}
	for _, item := range needles {
		if _, ok := index[item]; !ok {
			return false
		}
	}
	return true
}

func RedactedSummary(rec ClipboardRecord) string {
	if rec.IsSensitive {
		platform := strings.TrimSpace(rec.TargetPlatform)
		account := strings.TrimSpace(rec.ContentText)
		if platform != "" && account != "" {
			if strings.HasPrefix(account, platform) {
				return account
			}
			return platform + " " + account
		}
		if platform != "" {
			return platform
		}
		if account != "" {
			return account
		}
		return "账户密码"
	}
	if rec.ContentType == ContentImage {
		if strings.TrimSpace(rec.ContentText) != "" && !strings.HasPrefix(rec.ContentText, "data:") {
			text := strings.ReplaceAll(rec.ContentText, "\r\n", " ")
			text = strings.ReplaceAll(text, "\r", " ")
			text = strings.ReplaceAll(text, "\n", " ")
			const max = 60
			if len([]rune(text)) <= max {
				return "图片 · " + text
			}
			return "图片 · " + string([]rune(text)[:max]) + "..."
		}
		return "图片"
	}
	const max = 120
	text := strings.ReplaceAll(rec.ContentText, "\r\n", " ")
	text = strings.ReplaceAll(text, "\r", " ")
	text = strings.ReplaceAll(text, "\n", " ")
	if len([]rune(text)) <= max {
		return text
	}
	runes := []rune(text)
	return string(runes[:max]) + "..."
}
