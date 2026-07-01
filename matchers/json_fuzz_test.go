package matchers

import "testing"

func FuzzNormalizeJSON(f *testing.F) {
	f.Add(`{"name":"commons-test","enabled":true}`)
	f.Add(`[]`)
	f.Add(`null`)

	f.Fuzz(func(t *testing.T, input string) {
		_, _ = NormalizeJSON(input)
	})
}

func FuzzCompareJSON(f *testing.F) {
	f.Add(`{"a":1}`, `{"a":1}`)
	f.Add(`[]`, `[]`)

	f.Fuzz(func(t *testing.T, actual, expected string) {
		_, _ = CompareJSON([]byte(actual), []byte(expected))
	})
}
