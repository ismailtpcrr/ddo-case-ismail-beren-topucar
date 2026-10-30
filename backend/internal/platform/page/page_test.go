package page_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"parakopru/internal/platform/apperr"
	"parakopru/internal/platform/page"
)

type cursor struct {
	ID int `json:"id"`
}

func TestCursorRoundTrip(t *testing.T) {
	enc, err := page.Encode(cursor{ID: 42})
	require.NoError(t, err)

	var got cursor
	require.NoError(t, page.Decode(enc, &got))
	require.Equal(t, 42, got.ID)
}

func TestDecodeRejectsGarbage(t *testing.T) {
	for _, bad := range []string{"%%%", "bm90LWpzb24"} { // ikincisi geçerli base64 ama JSON değil
		var c cursor
		err := page.Decode(bad, &c)
		require.ErrorIs(t, err, apperr.ErrValidation, bad)
	}
}

func TestNewWithoutNextPage(t *testing.T) {
	p, err := page.New([]int{1, 2, 3}, 3, func(i int) any { return cursor{ID: i} })
	require.NoError(t, err)
	require.Equal(t, []int{1, 2, 3}, p.Items)
	require.Nil(t, p.NextCursor)
}

func TestNewWithNextPage(t *testing.T) {
	// Repo limit+1 satır döndürdü: sonraki sayfa var.
	p, err := page.New([]int{1, 2, 3, 4}, 3, func(i int) any { return cursor{ID: i} })
	require.NoError(t, err)
	require.Equal(t, []int{1, 2, 3}, p.Items)
	require.NotNil(t, p.NextCursor)

	var c cursor
	require.NoError(t, page.Decode(*p.NextCursor, &c))
	require.Equal(t, 3, c.ID, "imleç sayfanın son satırını göstermeli")
}

func TestEmptyPageSerializesAsEmptyArray(t *testing.T) {
	p, err := page.New[int](nil, 50, func(i int) any { return i })
	require.NoError(t, err)

	b, err := json.Marshal(p)
	require.NoError(t, err)
	require.JSONEq(t, `{"items":[],"next_cursor":null}`, string(b))
}
