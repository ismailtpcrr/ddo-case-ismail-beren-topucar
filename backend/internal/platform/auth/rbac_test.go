package auth_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"parakopru/internal/platform/auth"
)

// PLAN.md §5.10 yetki matrisi; her satır: izin → bu izne sahip roller.
// Tablo değişirse bu test de bilinçli olarak güncellenmelidir.
func TestPermissionMatrixMatchesPlan(t *testing.T) {
	C, O, R, U, D, S := auth.RoleCustomer, auth.RoleOperations, auth.RoleRiskAnalyst, auth.RoleCompliance, auth.RoleAuditor, auth.RoleSystem
	matrix := map[auth.Permission][]auth.Role{
		auth.PermProfileRead:       {C, O, R, U, D, S},
		auth.PermOwnBanking:        {C},
		auth.PermCustomersWrite:    {O},
		auth.PermLedgerRead:        {O, R, U, D},
		auth.PermTransfersCreate:   {C, O},
		auth.PermTransfersApprove:  {O, U},
		auth.PermFXWrite:           {O},
		auth.PermRiskRulesWrite:    {R},
		auth.PermRiskReviewsDecide: {R},
		auth.PermReconOperate:      {O},
		auth.PermReconApprove:      {U},
		auth.PermAuditRead:         {U, D},
		auth.PermEODRun:            {O, S},
		auth.PermSystemCallbacks:   {S},
	}

	for perm, allowed := range matrix {
		for _, role := range auth.Roles {
			want := false
			for _, a := range allowed {
				want = want || a == role
			}
			require.Equal(t, want, role.Has(perm), "%s → %s", role, perm)
		}
	}

	// Matriste olmayan bir izin rollere verilmiş mi?
	for _, role := range auth.Roles {
		for _, p := range role.Permissions() {
			require.Contains(t, matrix, p, "%s rolünde tabloda olmayan izin: %s", role, p)
		}
	}
}

func TestAuditorIsReadOnly(t *testing.T) {
	for _, p := range auth.RoleAuditor.Permissions() {
		require.True(t, strings.HasSuffix(string(p), ":read"), "denetçi yalnız okuma iznine sahip olmalı, bulundu: %s", p)
	}
}

func TestRoleValid(t *testing.T) {
	for _, r := range auth.Roles {
		require.True(t, r.Valid())
	}
	require.False(t, auth.Role("ADMIN").Valid())
	require.False(t, auth.Role("").Valid())
}
