// Package auth kimlik doğrulama ve yetkilendirmeyi içerir: argon2id parola özeti, JWT (access + refresh),
// rol → izin tablosu (RBAC) ve her uçta izin kontrolü yapan middleware.
package auth

import (
	"slices"
)

// Role kullanıcının rolüdür (proje.md §4).
type Role string

const (
	RoleCustomer    Role = "CUSTOMER"     // Müşteri
	RoleOperations  Role = "OPERATIONS"   // Operasyon uzmanı
	RoleRiskAnalyst Role = "RISK_ANALYST" // Risk analisti
	RoleCompliance  Role = "COMPLIANCE"   // Uyum (compliance) uzmanı
	RoleAuditor     Role = "AUDITOR"      // Denetçi
	RoleSystem      Role = "SYSTEM"       // Sistem (zamanlanmış işler, simülatör)
)

// Roles bütün rollerdir.
var Roles = []Role{RoleCustomer, RoleOperations, RoleRiskAnalyst, RoleCompliance, RoleAuditor, RoleSystem}

// Valid rolün tanımlı olup olmadığını söyler.
func (r Role) Valid() bool { return slices.Contains(Roles, r) }

// Permission tek bir işlem yetkisidir. Uçlar rol değil izin ister; böylece bir rolün yetkisi
// değiştiğinde uç kodları değişmez, yalnız aşağıdaki tablo değişir.
type Permission string

const (
	PermProfileRead       Permission = "profile:read"        // kendi profilini görme (/me) — her rol
	PermOwnBanking        Permission = "own_banking"         // kendi hesap, işlem, kart, ekstre
	PermCustomersWrite    Permission = "customers:write"     // müşteri ve hesap açma
	PermLedgerRead        Permission = "ledger:read"         // tüm hesap ve defteri okuma, zaman yolculuğu
	PermTransfersCreate   Permission = "transfers:create"    // transfer başlatma (müşteri yalnız kendi hesabından)
	PermTransfersApprove  Permission = "transfers:approve"   // dört göz onayı (10.000 TL üzeri)
	PermFXWrite           Permission = "fx:write"            // kur yönetimi
	PermRiskRulesWrite    Permission = "risk_rules:write"    // risk kuralları, what-if
	PermRiskReviewsDecide Permission = "risk_reviews:decide" // inceleme kuyruğu kararı
	PermReconOperate      Permission = "recon:operate"       // mutabakat yükleme, break çözüm önerisi
	PermReconApprove      Permission = "recon:approve"       // break onayı
	PermAuditRead         Permission = "audit:read"          // denetim kaydı
	PermEODRun            Permission = "eod:run"             // gün sonu tetikleme
	PermSystemCallbacks   Permission = "system:callbacks"    // kart ağı çağrıları, banka bildirimleri
)

// rolePermissions, PLAN.md §5.10'daki yetki matrisinin koddaki tek kaynağıdır.
// Denetçi her yerde salt okurdur: listesinde hiçbir yazma izni yoktur (testle korunur).
var rolePermissions = map[Role][]Permission{
	RoleCustomer: {
		PermProfileRead, PermOwnBanking, PermTransfersCreate,
	},
	RoleOperations: {
		PermProfileRead, PermCustomersWrite, PermLedgerRead, PermTransfersCreate, PermTransfersApprove,
		PermFXWrite, PermReconOperate, PermEODRun,
	},
	RoleRiskAnalyst: {
		PermProfileRead, PermLedgerRead, PermRiskRulesWrite, PermRiskReviewsDecide,
	},
	RoleCompliance: {
		PermProfileRead, PermLedgerRead, PermTransfersApprove, PermReconApprove, PermAuditRead,
	},
	RoleAuditor: {
		PermProfileRead, PermLedgerRead, PermAuditRead,
	},
	RoleSystem: {
		PermProfileRead, PermEODRun, PermSystemCallbacks,
	},
}

// Has rolün izne sahip olup olmadığını söyler.
func (r Role) Has(p Permission) bool { return slices.Contains(rolePermissions[r], p) }

// Permissions rolün izinlerini sıralı döndürür (arayüz menüyü buna göre süzer).
func (r Role) Permissions() []Permission {
	out := slices.Clone(rolePermissions[r])
	slices.Sort(out)
	return out
}
