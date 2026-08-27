package beckn

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"
)

// MaterialTerms are the financially/legally material facts of a Beckn
// transaction that an approval and a decision bind to. They MUST be canonicalized
// by the fabric edge from AUTHORITATIVE offer/order state — never from
// model-authored arguments (§9.6): a model that could author the terms could
// bind an approval to misleading content.
type MaterialTerms struct {
	OfferID  string
	Provider string
	Amount   string
	Currency string
	Payee    string
	Quantity string
	// Extra holds any additional binding facts (e.g. cancellation fee terms).
	Extra map[string]string
}

// Canonical returns a deterministic, order-independent serialization. Two term
// sets that differ in any material field produce different strings; whitespace
// and map iteration order never affect the result.
func (t MaterialTerms) Canonical() string {
	pairs := []string{
		"offer=" + t.OfferID,
		"provider=" + t.Provider,
		"amount=" + t.Amount,
		"currency=" + t.Currency,
		"payee=" + t.Payee,
		"quantity=" + t.Quantity,
	}
	extra := make([]string, 0, len(t.Extra))
	for k, v := range t.Extra {
		extra = append(extra, "x:"+k+"="+v)
	}
	sort.Strings(extra)
	pairs = append(pairs, extra...)
	return strings.Join(pairs, "|")
}

// Hash returns the terms_hash: sha256 over the canonical form. This is the value
// bound into the decision context and the approval digest; a changed quote,
// payee or cancellation term changes it and invalidates a prior approval.
func (t MaterialTerms) Hash() string {
	sum := sha256.Sum256([]byte(t.Canonical()))
	return "sha256:" + hex.EncodeToString(sum[:])
}
