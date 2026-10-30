package httpx

import (
	"net/http"
	"strconv"

	"parakopru/internal/platform/apperr"
	"parakopru/internal/platform/page"
)

// ParsePage ?limit=&cursor= sorgu parametrelerini okur.
// limit verilmezse page.DefaultLimit; 1..page.MaxLimit dışındaysa doğrulama hatası.
func ParsePage(r *http.Request) (page.Request, error) {
	q := r.URL.Query()
	req := page.Request{Limit: page.DefaultLimit, Cursor: q.Get("cursor")}

	if s := q.Get("limit"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 1 || n > page.MaxLimit {
			return page.Request{}, apperr.ErrValidation.Messagef("limit 1 ile %d arasında bir tam sayı olmalı.", page.MaxLimit)
		}
		req.Limit = n
	}
	return req, nil
}
