// Para biçimleme ve ayrıştırma. Tutarlar tam sayı kuruş/sent olarak taşınır; kayan nokta kullanılmaz.

const symbols: Record<string, string> = { TRY: '₺', USD: '$', EUR: '€' }

/** 123456 → "1.234,56 ₺" (işaret korunur). */
export function formatMoney(minor: number, currency: string): string {
  const neg = minor < 0
  const abs = Math.abs(minor)
  const units = Math.floor(abs / 100)
  const cents = String(abs % 100).padStart(2, '0')
  const grouped = String(units).replace(/\B(?=(\d{3})+(?!\d))/g, '.')
  return `${neg ? '−' : ''}${grouped},${cents} ${symbols[currency] ?? currency}`
}

/** "1.234,56" ya da "1234.5" → kuruş; geçersizse null. En fazla 2 ondalık basamak kabul edilir. */
export function parseMinor(input: string): number | null {
  const s = input.trim().replace(/\s/g, '')
  // Hem "1.234,56" hem "1234.56" kabul edilir: son ayırıcı ondalık, öncekiler binliktir.
  const m = /^(\d{1,3}(?:\.\d{3})+|\d+)(?:[.,](\d{1,2}))?$/.exec(s)
  if (!m) return null
  const units = Number(m[1].replace(/\./g, ''))
  const cents = Number((m[2] ?? '').padEnd(2, '0'))
  const minor = units * 100 + cents
  return Number.isSafeInteger(minor) ? minor : null
}

export function formatDate(iso: string): string {
  return new Date(iso).toLocaleString('tr-TR', { dateStyle: 'short', timeStyle: 'short' })
}

export function formatIban(iban: string | null): string {
  return iban ? iban.replace(/(.{4})/g, '$1 ').trim() : '—'
}
