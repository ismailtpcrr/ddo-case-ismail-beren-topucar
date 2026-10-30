// Package verify, `make verify` komutunun kontrolleridir (proje.md §10).
//
// Ayakta ve seed edilmiş sisteme API (ve frontend) üzerinden gerçek istekler gönderir; şartnamedeki her
// doğrulama kimliği (F-1…F-8, T-1…T-7) ve ek senaryolar (sağlık, kimlik doğrulama, yetki) için PASS/FAIL
// tablosu basar. Henüz uygulanmamış bir kimlik FAIL sayılır: tablo, sistemin gerçek durumunu gösterir.
package verify

import (
	"context"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"
)

// Status bir kontrolün sonucudur.
type Status string

const (
	Pass Status = "PASS"
	Fail Status = "FAIL"
)

// Check bir doğrulama kimliğidir. Run nil ise kimlik henüz uygulanmamıştır (PlannedIn adımında gelecek).
type Check struct {
	ID        string
	Title     string
	PlannedIn string // Run nil iken: PLAN.md'de hangi adımda ekleneceği
	Timeout   time.Duration
	Run       func(ctx context.Context, env *Env) (detail string, err error)
}

// Result bir kontrolün sonucudur.
type Result struct {
	Check    Check
	Status   Status
	Detail   string
	Duration time.Duration
}

const defaultCheckTimeout = 2 * time.Minute

// RunAll kontrolleri sırayla çalıştırır. Bir kontrolün hatası ya da panic'i diğerlerini durdurmaz.
func RunAll(ctx context.Context, env *Env, checks []Check, progress io.Writer) []Result {
	results := make([]Result, 0, len(checks))
	for _, c := range checks {
		_, _ = fmt.Fprintf(progress, "→ %-8s %s\n", c.ID, c.Title)
		results = append(results, runOne(ctx, env, c))
	}
	return results
}

func runOne(ctx context.Context, env *Env, c Check) (res Result) {
	res.Check = c
	if c.Run == nil {
		res.Status, res.Detail = Fail, "henüz uygulanmadı (PLAN.md adım "+c.PlannedIn+")"
		return res
	}
	timeout := c.Timeout
	if timeout == 0 {
		timeout = defaultCheckTimeout
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	start := time.Now()
	defer func() {
		res.Duration = time.Since(start)
		if p := recover(); p != nil {
			res.Status, res.Detail = Fail, fmt.Sprintf("beklenmeyen hata (panic): %v", p)
		}
	}()
	detail, err := c.Run(cctx, env)
	if err != nil {
		res.Status, res.Detail = Fail, err.Error()
		return res
	}
	res.Status, res.Detail = Pass, detail
	return res
}

// PrintTable sonuç tablosunu ve özeti yazar; hepsi PASS ise true döner.
func PrintTable(w io.Writer, results []Result) bool {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "KİMLİK\tSONUÇ\tSÜRE\tKONTROL\tAÇIKLAMA")
	_, _ = fmt.Fprintln(tw, "------\t-----\t----\t-------\t--------")
	passed := 0
	for _, r := range results {
		if r.Status == Pass {
			passed++
		}
		dur := "-"
		if r.Duration > 0 {
			dur = r.Duration.Round(time.Millisecond).String()
		}
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", r.Check.ID, r.Status, dur, r.Check.Title, oneLine(r.Detail))
	}
	_ = tw.Flush()

	failed := len(results) - passed
	_, _ = fmt.Fprintf(w, "\nToplam %d kontrol: %d PASS, %d FAIL\n", len(results), passed, failed)
	return failed == 0
}

func oneLine(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > 160 {
		return string(r[:157]) + "..."
	}
	return s
}
