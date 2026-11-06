package execution

import (
	"math"
	"testing"

	"github.com/astilbahq/astilba/services/platform/internal/portability"
)

func TestGenerationClaimLimit(t *testing.T) {
	for _, test := range []struct{ issued, allowance, want int64 }{
		{0, networkReceiptMargin, networkReceiptMargin},
		{generationClaimCeiling - 1, exportClaimAllowance, generationClaimCeiling},
		{generationClaimCeiling, exportClaimAllowance, generationClaimCeiling},
		{portability.MaxCounter, networkReceiptMargin, portability.MaxCounter},
		{1 << 60, exportClaimAllowance, 1 << 60},
		{math.MaxInt64, exportClaimAllowance, math.MaxInt64},
	} {
		if got := generationClaimLimit(test.issued, test.allowance); got != test.want {
			t.Errorf("generationClaimLimit(%d, %d) = %d, want %d", test.issued, test.allowance, got, test.want)
		}
	}
}
