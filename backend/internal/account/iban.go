package account

import (
	"fmt"
	"math/big"
	"strings"
)

// bankCode kurgusal banka kodudur (TR IBAN'da 5 hane).
const bankCode = "00999"

// NewIBAN hesap sıra numarasından geçerli kontrol haneli bir TR IBAN üretir:
//
//	TR kk BBBBB 0 NNNNNNNNNNNNNNNN   (kk: kontrol hanesi, B: banka kodu, 0: rezerv, N: 16 haneli hesap no)
//
// Kontrol hanesi ISO 13616 (mod 97) ile hesaplanır; dış bankadan gelen transferlerde alıcı IBAN'ı doğrulanabilir.
func NewIBAN(accountNumber int64) string {
	bban := fmt.Sprintf("%s0%016d", bankCode, accountNumber)
	check := 98 - mod97(bban+"TR00")
	return fmt.Sprintf("TR%02d%s", check, bban)
}

// ValidIBAN TR IBAN'ın biçimini ve kontrol hanesini doğrular.
func ValidIBAN(iban string) bool {
	if len(iban) != 26 || !strings.HasPrefix(iban, "TR") {
		return false
	}
	for _, r := range iban[2:] {
		if r < '0' || r > '9' {
			return false
		}
	}
	return mod97(iban[4:]+iban[:4]) == 1
}

// mod97 harfleri sayıya (A=10 … Z=35) çevirip 97'ye göre kalanı hesaplar.
func mod97(s string) int64 {
	var digits strings.Builder
	for _, r := range s {
		if r >= 'A' && r <= 'Z' {
			fmt.Fprintf(&digits, "%d", r-'A'+10)
		} else {
			digits.WriteRune(r)
		}
	}
	n, _ := new(big.Int).SetString(digits.String(), 10)
	return new(big.Int).Mod(n, big.NewInt(97)).Int64()
}
