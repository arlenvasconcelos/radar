package helm

import (
	"strings"
	"testing"
)

func TestComputeRedactedValuesDiff(t *testing.T) {
	for _, allValues := range []bool{false, true} {
		name := "user-supplied"
		if allValues {
			name = "computed"
		}
		t.Run(name, func(t *testing.T) {
			left := map[string]any{
				"dbPassword":                "OLD_PASSWORD_SENTINEL",
				"apiKey":                    "sk-live-OLD_APIKEY_SENTINEL",
				"auth":                      map[string]any{"postgresPassword": "OLD_NESTED_SENTINEL"},
				"image":                     map[string]any{"tag": "1.0.0"},
				"replicaCount":              1,
				"existingSecretPasswordKey": "old-password-key",
			}
			right := map[string]any{
				"dbPassword":                "NEW_PASSWORD_SENTINEL",
				"apiKey":                    "sk-live-NEW_APIKEY_SENTINEL",
				"auth":                      map[string]any{"postgresPassword": "NEW_NESTED_SENTINEL"},
				"credentials":               []any{"NEW_CREDENTIALS_SENTINEL"},
				"image":                     map[string]any{"tag": "2.0.0"},
				"replicaCount":              2,
				"existingSecretPasswordKey": "new-password-key",
			}
			values1, values2 := &HelmValues{UserSupplied: left}, &HelmValues{UserSupplied: right}
			if allValues {
				values1 = &HelmValues{Computed: left}
				values2 = &HelmValues{Computed: right}
			}
			rawDiff, err := computeValuesDiff(values1, values2, 1, 2, allValues)
			if err != nil {
				t.Fatal(err)
			}
			for _, sentinel := range []string{"OLD_PASSWORD_SENTINEL", "NEW_PASSWORD_SENTINEL", "sk-live-OLD_APIKEY_SENTINEL", "sk-live-NEW_APIKEY_SENTINEL", "OLD_NESTED_SENTINEL", "NEW_NESTED_SENTINEL", "NEW_CREDENTIALS_SENTINEL"} {
				if !strings.Contains(rawDiff, sentinel) {
					t.Fatalf("unredacted diff missing %q:\n%s", sentinel, rawDiff)
				}
			}
			diff, err := computeRedactedValuesDiff(values1, values2, 1, 2, allValues)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(diff, "SENTINEL") {
				t.Fatalf("redacted diff leaks a revision's credential:\n%s", diff)
			}
			for _, want := range []string{"[REDACTED]", "-replicaCount: 1", "+replicaCount: 2", "-  tag: 1.0.0", "+  tag: 2.0.0", "-existingSecretPasswordKey: old-password-key", "+existingSecretPasswordKey: new-password-key"} {
				if !strings.Contains(diff, want) {
					t.Errorf("redacted diff missing %q:\n%s", want, diff)
				}
			}
		})
	}
}
