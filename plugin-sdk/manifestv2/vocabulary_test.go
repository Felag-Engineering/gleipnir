package manifestv2

// Vocabulary drift test. manifestv2 copies plugin-sdk/manifest's six credential
// strategies and two Tier-2 capability names rather than importing them (see
// the package doc for why), and a copy that silently drifts from its source
// is worse than no copy at all — an author reading v1's docs and writing a v2
// manifest would get a value this host has never heard of. This test is the
// one place the two vocabularies are compared, so a future v1 addition (or
// typo here) fails loudly instead of surfacing as a confusing validation
// error at install time.

import (
	"sort"
	"testing"

	sdkmanifest "github.com/felag-engineering/gleipnir/plugin-sdk/manifest"
)

func TestVocabulary_AuthStrategiesMatchV1(t *testing.T) {
	v1 := []string{
		sdkmanifest.AuthStrategyNone,
		sdkmanifest.AuthStrategyStaticAPIKey,
		sdkmanifest.AuthStrategyHeaderSet,
		sdkmanifest.AuthStrategyBasicAuth,
		sdkmanifest.AuthStrategyOAuth2Authcode,
		sdkmanifest.AuthStrategyOAuth2Clientcred,
	}
	v2 := []string{
		AuthStrategyNone,
		AuthStrategyStaticAPIKey,
		AuthStrategyHeaderSet,
		AuthStrategyBasicAuth,
		AuthStrategyOAuth2Authcode,
		AuthStrategyOAuth2Clientcred,
	}
	assertSameStringSet(t, "auth strategies", v1, v2)
}

func TestVocabulary_Tier2CapabilitiesMatchV1(t *testing.T) {
	v1 := []string{
		sdkmanifest.Tier2RunHistoryRead,
		sdkmanifest.Tier2UserDirectoryRead,
	}
	v2 := []string{
		Tier2RunHistoryRead,
		Tier2UserDirectoryRead,
	}
	assertSameStringSet(t, "tier2 capabilities", v1, v2)
}

func assertSameStringSet(t *testing.T, label string, v1, v2 []string) {
	t.Helper()
	sortedV1 := append([]string(nil), v1...)
	sortedV2 := append([]string(nil), v2...)
	sort.Strings(sortedV1)
	sort.Strings(sortedV2)

	if len(sortedV1) != len(sortedV2) {
		t.Fatalf("%s: v1=%v v2=%v, want the same set", label, sortedV1, sortedV2)
	}
	for i := range sortedV1 {
		if sortedV1[i] != sortedV2[i] {
			t.Errorf("%s: v1=%v v2=%v, want the same set", label, sortedV1, sortedV2)
			return
		}
	}
}
