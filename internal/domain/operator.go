package domain

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"time"

	"github.com/vance1852/drviercar/internal/apperr"
)

// Role enumerates the two operational roles of the platform. A fleet
// administrator plans campaigns and settles mileage; a safety operator drives
// the vehicle and dispositions captured data.
type Role string

const (
	RoleFleetAdmin     Role = "fleet_admin"
	RoleSafetyOperator Role = "safety_operator"
)

// Valid reports whether the role is known.
func (r Role) Valid() bool {
	return r == RoleFleetAdmin || r == RoleSafetyOperator
}

// CanPlanCampaign reports whether the role may create or schedule campaigns.
func (r Role) CanPlanCampaign() bool { return r == RoleFleetAdmin }

// CanSettleMileage reports whether the role may compute settlements.
func (r Role) CanSettleMileage() bool { return r == RoleFleetAdmin }

// CanDrive reports whether the role may open drive sessions and report takeovers.
func (r Role) CanDrive() bool { return r == RoleSafetyOperator }

// CanDispositionCapture reports whether the role may triage captured batches.
func (r Role) CanDispositionCapture() bool {
	return r == RoleSafetyOperator || r == RoleFleetAdmin
}

// CanSealDataset reports whether the role may seal a dataset.
func (r Role) CanSealDataset() bool { return r == RoleFleetAdmin }

// Operator is an authenticated platform user.
type Operator struct {
	ID           int64
	Username     string
	DisplayName  string
	Role         Role
	Salt         string
	PasswordHash string
	Active       bool
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// Validate checks the operator invariants that the storage layer relies on.
func (o *Operator) Validate() error {
	if strings.TrimSpace(o.Username) == "" {
		return apperr.Invalidf("operator_username_required", "操作员登录名不能为空")
	}
	if len(o.Username) > 64 {
		return apperr.Invalidf("operator_username_too_long", "操作员登录名不能超过 64 个字符")
	}
	if !o.Role.Valid() {
		return apperr.Invalidf("operator_role_invalid", "未知的操作员角色 %q", string(o.Role))
	}
	if o.Salt == "" || o.PasswordHash == "" {
		return apperr.Invalidf("operator_credential_required", "操作员凭据不完整")
	}
	return nil
}

// NewSalt generates a random credential salt.
func NewSalt() (string, error) {
	buffer := make([]byte, 16)
	if _, err := rand.Read(buffer); err != nil {
		return "", apperr.Wrap(err, apperr.KindInternal, "salt_generation_failed", "无法生成凭据盐值")
	}
	return hex.EncodeToString(buffer), nil
}

// HashPassword derives the stored credential digest.
func HashPassword(salt, password string) string {
	mac := hmac.New(sha256.New, []byte(salt))
	mac.Write([]byte(password))
	return hex.EncodeToString(mac.Sum(nil))
}

// VerifyPassword compares a candidate password in constant time.
func (o *Operator) VerifyPassword(password string) bool {
	expected := HashPassword(o.Salt, password)
	return hmac.Equal([]byte(expected), []byte(o.PasswordHash))
}

// Clone returns an independent copy so that repository callers cannot mutate
// cached state.
func (o *Operator) Clone() *Operator {
	if o == nil {
		return nil
	}
	copied := *o
	return &copied
}
