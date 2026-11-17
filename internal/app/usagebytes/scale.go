// Package usagebytes applies the same traffic coefficients on the panel and node.
package usagebytes

import (
	"math"
	"math/big"
	"strconv"
)

func NormalizeFactor(factor float64) float64 {
	if factor <= 0 || math.IsNaN(factor) || math.IsInf(factor, 0) {
		return 1
	}
	return factor
}

// Scale rounds to whole bytes, with halves rounded up and saturation on overflow.
func Scale(raw uint64, nodeFactor, inboundFactor float64) uint64 {
	nodeFactor = NormalizeFactor(nodeFactor)
	inboundFactor = NormalizeFactor(inboundFactor)
	if raw == 0 || (nodeFactor == 1 && inboundFactor == 1) {
		return raw
	}

	value := float64(raw) * (nodeFactor * inboundFactor)
	// Use hardware arithmetic when its rounding error cannot cross a half-byte
	// boundary. For ties and very large values, evaluate the configured decimal
	// coefficients against the original integer byte count.
	_, fraction := math.Modf(value)
	if value < 1<<51 && math.Abs(fraction-0.5) > value*1e-15 {
		return uint64(math.Round(value))
	}
	var total, factor big.Rat
	total.SetUint64(raw)
	for _, coefficient := range []float64{nodeFactor, inboundFactor} {
		factor.SetString(strconv.FormatFloat(coefficient, 'g', -1, 64))
		total.Mul(&total, &factor)
	}
	var rounded, remainder big.Int
	rounded.QuoRem(total.Num(), total.Denom(), &remainder)
	if remainder.Lsh(&remainder, 1).Cmp(total.Denom()) >= 0 {
		rounded.Add(&rounded, big.NewInt(1))
	}
	if !rounded.IsUint64() {
		return math.MaxUint64
	}
	return rounded.Uint64()
}
