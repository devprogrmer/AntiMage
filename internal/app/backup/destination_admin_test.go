package backup

import (
	"strings"
	"testing"
)

func TestDestinationAdminAliasAvoidsExistingNamesAndLengthLimit(t *testing.T) {
	used := map[string]bool{
		"administrator_name_that_is_long_local_1": true,
	}
	original := "administrator_name_that_is_longer_than_thirty_four"
	first := destinationAdminAlias(original, used)
	if len([]rune(first)) > 34 || !strings.HasSuffix(first, "_local_1") {
		t.Fatalf("unexpected first alias %q", first)
	}
	used[strings.ToLower(first)] = true
	second := destinationAdminAlias(original, used)
	if len([]rune(second)) > 34 || second == first || !strings.HasSuffix(second, "_local_2") {
		t.Fatalf("unexpected second alias %q", second)
	}
}
